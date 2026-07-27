package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

const cleanupTimeout = 5 * time.Second

type Operation interface {
	ListTools(context.Context) ([]Tool, error)
	CallTool(context.Context, CallInput) (string, error)
}

type listToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type listAccumulator struct {
	tools         []Tool
	seenCursors   map[string]struct{}
	retainedBytes int64
}

func (a *listAccumulator) reset() {
	a.tools = nil
	a.seenCursors = nil
	a.retainedBytes = 0
}

func (a *listAccumulator) appendPage(tools []Tool, nextCursor string, pageLimitReached bool) error {
	if len(tools) > maxAccumulatedTools-len(a.tools) {
		return ErrTooManyTools
	}
	nextBytes, err := accumulatedListToolBytes(a.retainedBytes, tools)
	if err != nil {
		return err
	}
	if nextCursor != "" {
		if _, exists := a.seenCursors[nextCursor]; exists {
			return ErrPaginationCursorLoop
		}
		if pageLimitReached {
			return ErrTooManyToolPages
		}
		nextBytes, err = addRetainedListBytes(nextBytes, len(nextCursor))
		if err != nil {
			return err
		}
	}

	a.tools = append(a.tools, tools...)
	if nextCursor != "" {
		if a.seenCursors == nil {
			a.seenCursors = make(map[string]struct{})
		}
		a.seenCursors[nextCursor] = struct{}{}
	}
	a.retainedBytes = nextBytes
	return nil
}

func accumulatedListToolBytes(current int64, tools []Tool) (int64, error) {
	total, err := addRetainedListBytes(current, 0)
	if err != nil {
		return 0, err
	}
	for _, tool := range tools {
		for _, size := range [...]int{
			len(tool.Name),
			len(tool.Title),
			len(tool.Description),
			len(tool.InputSchema),
		} {
			total, err = addRetainedListBytes(total, size)
			if err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

func addRetainedListBytes(current int64, size int) (int64, error) {
	if current < 0 || current > maxRetainedListBytes || size < 0 {
		return 0, ErrRetainedListTooLarge
	}
	additional := int64(size)
	if additional > maxRetainedListBytes-current {
		return 0, ErrRetainedListTooLarge
	}
	return current + additional, nil
}

type operation struct {
	transport     Transport
	config        CallConfig
	signedContext *SignedContextConfig
	session       sessionState
	nextID        atomic.Int64
	retryPolicy   RetryPolicy
	retryBudget   int
	gate          chan struct{}
	isInitialized bool
}

var _ Operation = (*operation)(nil)

func newOperation(transport Transport, cfg CallConfig, retryCount int) (*operation, error) {
	if transport == nil {
		return nil, newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	snapshot, err := snapshotCallConfig(cfg)
	if err != nil {
		return nil, err
	}
	endpoint, err := buildEndpointURL(snapshot)
	if err != nil {
		return nil, err
	}
	snapshot.BaseURL = endpoint
	signedContext := snapshot.SignedContext
	snapshot.SignedContext = nil
	if retryCount < 0 {
		retryCount = 0
	}
	return &operation{
		transport:     transport,
		config:        snapshot,
		signedContext: signedContext,
		retryPolicy:   ClassifiedRetryPolicy{},
		retryBudget:   retryCount,
		gate:          make(chan struct{}, 1),
	}, nil
}

func (o *operation) ListTools(ctx context.Context) ([]Tool, error) {
	if err := o.acquire(ctx); err != nil {
		return nil, err
	}
	defer o.release()

	return o.listToolsLocked(ctx)
}

func (o *operation) CallTool(ctx context.Context, input CallInput) (string, error) {
	if err := o.acquire(ctx); err != nil {
		return "", err
	}
	defer o.release()
	params, err := buildCallToolParams(o.config, input)
	if err != nil {
		return "", err
	}

	attempt := 0
	for {
		if err = o.ensureInitializedLocked(ctx, &attempt); err != nil {
			return "", err
		}
		var result json.RawMessage
		result, err = o.rpcLocked(ctx, OperationCallTool, "tools/call", params, false)
		if err == nil {
			return normalizeToolCallResult(result)
		}
		decision := o.retryPolicy.Decide(RetryInput{
			Operation: OperationCallTool,
			Attempt:   attempt,
			Budget:    o.retryBudget,
			Delivery:  errorDelivery(err),
			Err:       err,
		})
		if decision == RetryStop {
			return "", err
		}
		attempt++
	}
}

func (o *operation) listToolsLocked(ctx context.Context) ([]Tool, error) {
	attempt := 0
	accumulator := listAccumulator{}

restart:
	accumulator.reset()
	if err := o.ensureInitializedLocked(ctx, &attempt); err != nil {
		return nil, err
	}
	cursor := ""
	pageCount := 0
	for {
		var params interface{} = map[string]interface{}{}
		if cursor != "" {
			params = map[string]string{"cursor": cursor}
		}
		result, err := o.rpcLocked(ctx, OperationListTools, "tools/list", params, false)
		if err != nil {
			decision := o.retryPolicy.Decide(RetryInput{
				Operation: OperationListTools,
				Attempt:   attempt,
				Budget:    o.retryBudget,
				Delivery:  errorDelivery(err),
				Err:       err,
			})
			switch decision {
			case RetrySameSession:
				attempt++
				continue
			case RetryNewSession:
				attempt++
				goto restart
			default:
				return nil, err
			}
		}

		var page listToolsResult
		if err = json.Unmarshal(result, &page); err != nil {
			return nil, newClientError(ClientErrorProtocol, 0, 0, nil)
		}
		pageCount++
		if err = accumulator.appendPage(page.Tools, page.NextCursor, pageCount >= maxToolListPages); err != nil {
			return nil, err
		}
		if page.NextCursor == "" {
			return accumulator.tools, nil
		}
		cursor = page.NextCursor
	}
}

func (o *operation) terminate(ctx context.Context) error {
	if err := o.acquire(ctx); err != nil {
		return err
	}
	defer o.release()
	return o.terminateLocked(ctx)
}

func (o *operation) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case o.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			o.release()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *operation) release() {
	<-o.gate
}

func (o *operation) ensureInitializedLocked(ctx context.Context, attempt *int) error {
	if o.isInitialized {
		return nil
	}
	for {
		if o.session.ProtocolVersion == "" {
			err := o.initializeLocked(ctx)
			if err != nil {
				decision := o.retryPolicy.Decide(RetryInput{
					Operation: OperationInitialize,
					Attempt:   *attempt,
					Budget:    o.retryBudget,
					Delivery:  errorDelivery(err),
					Err:       err,
				})
				if decision == RetryStop {
					return err
				}
				*attempt = *attempt + 1
				continue
			}
		}

		_, err := o.rpcLocked(ctx, OperationInitialized, "notifications/initialized", nil, true)
		if err == nil {
			o.isInitialized = true
			return nil
		}
		invalidSession := errors.Is(err, ErrSessionInvalid)
		decision := o.retryPolicy.Decide(RetryInput{
			Operation: OperationInitialized,
			Attempt:   *attempt,
			Budget:    o.retryBudget,
			Delivery:  errorDelivery(err),
			Err:       err,
		})
		if invalidSession {
			o.resetSessionLocked()
		}
		switch decision {
		case RetrySameSession:
			*attempt = *attempt + 1
			continue
		case RetryNewSession:
			if !invalidSession {
				_ = o.terminateLocked(ctx)
			}
			*attempt = *attempt + 1
			continue
		default:
			if !invalidSession {
				_ = o.terminateLocked(ctx)
			}
			return err
		}
	}
}

func (o *operation) initializeLocked(ctx context.Context) error {
	params := map[string]interface{}{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "deeix-chat",
			"version": "0.1.0",
		},
	}
	result, err := o.rpcLocked(ctx, OperationInitialize, "initialize", params, false)
	if err != nil {
		if o.session.ID != "" {
			_ = o.terminateLocked(ctx)
		} else {
			o.resetSessionLocked()
		}
		return err
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err = json.Unmarshal(result, &initialized); err != nil {
		_ = o.terminateLocked(ctx)
		return newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	if initialized.ProtocolVersion != protocolVersion {
		_ = o.terminateLocked(ctx)
		return newClientError(ClientErrorProtocol, 0, 0, ErrUnsupportedProtocolVersion)
	}
	return nil
}

func (o *operation) rpcLocked(
	ctx context.Context,
	kind OperationKind,
	method string,
	params interface{},
	notification bool,
) (json.RawMessage, error) {
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		payload["params"] = params
	}
	var requestID json.RawMessage
	if !notification {
		id := o.nextID.Add(1)
		payload["id"] = id
		requestID = json.RawMessage(strconv.FormatInt(id, 10))
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, newClientError(ClientErrorProtocol, 0, 0, nil)
	}

	session := o.session
	if kind == OperationInitialize {
		session = sessionState{}
	}
	request := TransportRequest{
		Operation:           kind,
		HTTPMethod:          http.MethodPost,
		Endpoint:            o.config.BaseURL,
		AuthToken:           o.config.AuthToken,
		Body:                body,
		RequestID:           requestID,
		Session:             session,
		CustomHeaders:       o.config.CustomHeaders,
		TemplateContext:     o.config.Context,
		SignedContextHeader: o.config.SignedContextHeader,
		SignedContext:       o.signedContext,
	}
	response, requestErr := o.doTransportLocked(ctx, request)
	if kind == OperationInitialize {
		o.session.ProtocolVersion = protocolVersion
	}
	if errors.Is(requestErr, ErrSSEInterrupted) {
		originalErr := requestErr
		var resumed bool
		response, requestErr, resumed = o.resumeSSELocked(ctx, requestID, response, requestErr)
		if resumed && requestErr != nil && kind == OperationCallTool {
			requestErr = logicalCallContinuationError(originalErr, requestErr)
		}
	}
	if requestErr != nil {
		if errors.Is(requestErr, ErrSessionInvalid) {
			o.resetSessionLocked()
		}
		return nil, requestErr
	}
	if response.Message.Error != nil {
		return nil, newClientError(ClientErrorJSONRPC, 0, response.Message.Error.Code, nil)
	}
	if notification {
		return nil, nil
	}
	if len(response.Message.Result) == 0 {
		return json.RawMessage("{}"), nil
	}
	return response.Message.Result, nil
}

func (o *operation) doTransportLocked(ctx context.Context, request TransportRequest) (TransportResponse, error) {
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(resolveRequestTimeoutMS(o.config.TimeoutMS))*time.Millisecond)
	defer cancel()
	response, err := o.transport.Do(requestCtx, request)
	if errors.Is(err, ErrSessionInvalid) {
		return response, err
	}
	if response.SessionID != "" {
		if retainErr := o.retainSessionLocked(ctx, response.SessionID); retainErr != nil {
			return response, retainErr
		}
	}
	return response, err
}

func (o *operation) resumeSSELocked(
	ctx context.Context,
	requestID json.RawMessage,
	response TransportResponse,
	requestErr error,
) (TransportResponse, error, bool) {
	cursor := response.LastEventID
	if o.session.ID == "" || cursor == "" || !validLastEventID(cursor) {
		return response, requestErr, false
	}
	for range maxSSEResumes {
		resumeRequest := TransportRequest{
			Operation:           OperationResumeSSE,
			HTTPMethod:          http.MethodGet,
			Endpoint:            o.config.BaseURL,
			AuthToken:           o.config.AuthToken,
			RequestID:           append(json.RawMessage(nil), requestID...),
			Session:             o.session,
			CustomHeaders:       o.config.CustomHeaders,
			TemplateContext:     o.config.Context,
			SignedContextHeader: o.config.SignedContextHeader,
			SignedContext:       o.signedContext,
			LastEventID:         cursor,
		}
		response, requestErr = o.doTransportLocked(ctx, resumeRequest)
		if requestErr == nil {
			return response, nil, true
		}
		if errors.Is(requestErr, ErrSessionInvalid) {
			o.resetSessionLocked()
			return response, requestErr, true
		}
		if !errors.Is(requestErr, ErrSSEInterrupted) {
			return response, requestErr, true
		}
		cursor = response.LastEventID
		if cursor == "" || !validLastEventID(cursor) || o.session.ID == "" {
			return response, requestErr, true
		}
	}
	return response, requestErr, true
}

func logicalCallContinuationError(originalErr error, continuationErr error) error {
	delivery := errorDelivery(originalErr)
	if delivery == DeliveryNotSent {
		delivery = DeliveryUnknown
	}
	class := ClientErrorNetwork
	statusCode := 0
	var requestErr *RequestError
	if errors.As(continuationErr, &requestErr) {
		class = requestErr.Class
		statusCode = requestErr.StatusCode
	} else {
		var clientErr *ClientError
		if errors.As(continuationErr, &clientErr) {
			class = clientErr.Kind
			statusCode = clientErr.StatusCode
		}
	}
	return newRequestError(OperationCallTool, delivery, statusCode, class, continuationErr)
}

func (o *operation) retainSessionLocked(ctx context.Context, sessionID string) error {
	if !validSessionID(sessionID) || sessionID == "" {
		return newClientError(ClientErrorProtocol, 0, 0, ErrInvalidSessionID)
	}
	if o.session.ID == sessionID {
		return nil
	}
	if o.session.ID != "" {
		if err := o.deleteSessionLocked(ctx, o.session); err != nil {
			_ = o.deleteSessionLocked(ctx, sessionState{ID: sessionID, ProtocolVersion: protocolVersion})
			return err
		}
	}
	o.session.ID = sessionID
	o.session.ProtocolVersion = protocolVersion
	return nil
}

func (o *operation) terminateLocked(ctx context.Context) error {
	current := o.session
	o.resetSessionLocked()
	if current.ID == "" {
		return nil
	}
	return o.deleteSessionLocked(ctx, current)
}

func (o *operation) deleteSessionLocked(ctx context.Context, current sessionState) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	_, err := o.transport.Do(cleanupCtx, TransportRequest{
		Operation:           OperationTerminate,
		HTTPMethod:          http.MethodDelete,
		Endpoint:            o.config.BaseURL,
		AuthToken:           o.config.AuthToken,
		Session:             current,
		CustomHeaders:       o.config.CustomHeaders,
		TemplateContext:     o.config.Context,
		SignedContextHeader: o.config.SignedContextHeader,
		SignedContext:       o.signedContext,
	})
	if err == nil {
		return nil
	}
	var requestErr *RequestError
	if errors.As(err, &requestErr) &&
		(requestErr.StatusCode == http.StatusNotFound || requestErr.StatusCode == http.StatusMethodNotAllowed) {
		return nil
	}
	return err
}

func (o *operation) resetSessionLocked() {
	o.session = sessionState{}
	o.isInitialized = false
}

func errorDelivery(err error) DeliveryState {
	var requestErr *RequestError
	if errors.As(err, &requestErr) {
		return requestErr.Delivery
	}
	return DeliveryUnknown
}
