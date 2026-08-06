package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"

	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/outboundhttp"
	"golang.org/x/net/http/httpguts"
)

type httpTransport struct {
	client        *http.Client
	contextSigner ContextSigner
	owner         *Client
}

func newHTTPTransport(client *http.Client, signer ContextSigner) Transport {
	return &httpTransport{client: client, contextSigner: signer}
}

func newClientHTTPTransport(owner *Client) Transport {
	return &httpTransport{owner: owner}
}

func (t *httpTransport) dependencies() (*http.Client, *outboundhttp.Pool, ContextSigner) {
	if t != nil && t.owner != nil {
		return t.owner.httpClient, t.owner.httpClients, t.owner.contextSigner
	}
	if t == nil {
		return nil, nil, nil
	}
	return t.client, nil, t.contextSigner
}

func (t *httpTransport) Do(ctx context.Context, request TransportRequest) (TransportResponse, error) {
	customHeaders := cloneCustomHeaders(request.CustomHeaders)
	if err := validateCustomHeadersWithSignedContext(
		customHeaders,
		request.SignedContextHeader,
		"",
		false,
	); err != nil {
		return TransportResponse{}, transportError(request, DeliveryNotSent, 0, ClientErrorProtocol, err)
	}
	if !validTransportRequestID(request.Operation, request.RequestID) {
		return TransportResponse{}, transportError(request, DeliveryNotSent, 0, ClientErrorProtocol, errInvalidTransportRequest)
	}
	if !validSessionID(request.Session.ID) {
		return TransportResponse{}, transportError(request, DeliveryNotSent, 0, ClientErrorProtocol, ErrInvalidSessionID)
	}
	if !validLastEventID(request.LastEventID) {
		return TransportResponse{}, transportError(request, DeliveryNotSent, 0, ClientErrorProtocol, ErrInvalidLastEventID)
	}

	var body io.Reader
	if len(request.Body) != 0 {
		body = bytes.NewReader(request.Body)
	}
	req, err := http.NewRequestWithContext(ctx, request.HTTPMethod, request.Endpoint, body)
	if err != nil {
		return TransportResponse{}, transportError(request, DeliveryNotSent, 0, ClientErrorProtocol, errInvalidRPCResponse)
	}
	for name, value := range customHeaders {
		req.Header.Set(name, value)
	}
	if token := strings.TrimSpace(request.AuthToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if request.HTTPMethod == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if request.Operation != OperationInitialize {
		if request.Session.ProtocolVersion != "" {
			req.Header.Set("MCP-Protocol-Version", request.Session.ProtocolVersion)
		}
		if request.Session.ID != "" {
			req.Header.Set("MCP-Session-Id", request.Session.ID)
		}
	}
	if request.LastEventID != "" {
		req.Header.Set("Last-Event-ID", request.LastEventID)
	}

	client, clientPool, signer := t.dependencies()
	if client == nil && clientPool == nil {
		return TransportResponse{}, transportError(request, DeliveryNotSent, 0, ClientErrorProtocol, errInvalidRPCResponse)
	}
	signedContextValue := ""
	hasSignedContextValue := request.SignedContextHeader != "" && request.SignedContext != nil
	if hasSignedContextValue {
		if signer == nil {
			return TransportResponse{}, transportError(
				request,
				DeliveryNotSent,
				0,
				ClientErrorProtocol,
				ErrContextSignerUnavailable,
			)
		}
		token, signErr := signer.Sign(request.TemplateContext, *request.SignedContext)
		if signErr != nil {
			return TransportResponse{}, transportError(
				request,
				DeliveryNotSent,
				0,
				ClientErrorProtocol,
				signErr,
			)
		}
		signedContextValue = token
	}
	if err := validateCustomHeadersWithSignedContext(
		customHeaders,
		request.SignedContextHeader,
		signedContextValue,
		hasSignedContextValue,
	); err != nil {
		return TransportResponse{}, transportError(request, DeliveryNotSent, 0, ClientErrorProtocol, err)
	}
	if hasSignedContextValue {
		req.Header.Set(request.SignedContextHeader, signedContextValue)
	}

	var writeState atomic.Uint32
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				writeState.Store(1)
				return
			}
			if writeState.Load() != 1 {
				writeState.Store(2)
			}
		},
	}))

	var resp *http.Response
	if client != nil {
		resp, err = client.Do(req)
	} else {
		resp, err = clientPool.Do(req, request.Endpoint, "")
	}
	if err != nil {
		delivery := DeliveryNotSent
		switch writeState.Load() {
		case 1:
			delivery = DeliverySent
		case 2:
			delivery = DeliveryUnknown
		}
		return TransportResponse{}, transportError(
			request,
			delivery,
			0,
			ClientErrorNetwork,
			safeContextCause(req.Context(), err),
		)
	}
	if resp == nil {
		return TransportResponse{}, transportError(request, DeliveryUnknown, 0, ClientErrorNetwork, nil)
	}
	defer resp.Body.Close() //nolint:errcheck

	response := TransportResponse{}
	sessionID, hasSessionID, singleSessionID := singleHeaderValue(resp.Header, "MCP-Session-Id")
	if !singleSessionID {
		return response, transportError(request, DeliverySent, resp.StatusCode, ClientErrorProtocol, ErrInvalidSessionID)
	}
	if hasSessionID && sessionID != "" {
		if !validSessionID(sessionID) {
			return response, transportError(request, DeliverySent, resp.StatusCode, ClientErrorProtocol, ErrInvalidSessionID)
		}
		response.SessionID = sessionID
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if resp.StatusCode == http.StatusNotFound && request.Session.ID != "" {
			return response, transportError(request, DeliverySent, resp.StatusCode, ClientErrorProtocol, ErrSessionInvalid)
		}
		if _, readErr := readBoundedResponseBody(req.Context(), resp.Body); readErr != nil {
			return response, transportBodyError(request, resp.StatusCode, readErr)
		}
		return response, transportError(request, DeliverySent, resp.StatusCode, ClientErrorHTTP, nil)
	}

	if len(request.RequestID) == 0 {
		payload, readErr := readBoundedResponseBody(req.Context(), resp.Body)
		if readErr != nil {
			return response, transportBodyError(request, resp.StatusCode, readErr)
		}
		if len(bytes.TrimSpace(payload)) != 0 {
			return response, transportError(request, DeliverySent, resp.StatusCode, ClientErrorProtocol, errInvalidRPCResponse)
		}
		return response, nil
	}

	mediaType, _, parseErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if parseErr != nil {
		return response, transportError(request, DeliverySent, resp.StatusCode, ClientErrorProtocol, ErrUnsupportedContentType)
	}
	switch {
	case strings.EqualFold(mediaType, "application/json"):
		payload, readErr := readBoundedResponseBody(req.Context(), resp.Body)
		if readErr != nil {
			return response, transportBodyError(request, resp.StatusCode, readErr)
		}
		message, matched, decodeErr := decodeRPCMessage(payload, request.RequestID)
		if decodeErr != nil {
			return response, transportError(request, DeliverySent, resp.StatusCode, ClientErrorProtocol, decodeErr)
		}
		if !matched {
			return response, transportError(request, DeliverySent, resp.StatusCode, ClientErrorProtocol, ErrMismatchedResponseID)
		}
		response.Message = message
		return response, nil

	case strings.EqualFold(mediaType, "text/event-stream"):
		limited := &io.LimitedReader{R: resp.Body, N: maxResponseBytes + 1}
		cursor, decodeErr := decodeSSE(req.Context(), limited, func(event sseEvent) (bool, error) {
			if event.Event != "" && event.Event != "message" {
				return false, nil
			}
			if len(event.Data) == 0 {
				return false, nil
			}
			message, matched, messageErr := decodeRPCMessage(event.Data, request.RequestID)
			if messageErr != nil {
				return false, messageErr
			}
			if matched {
				response.Message = message
			}
			return matched, nil
		})
		response.LastEventID = cursor.LastEventID
		if limited.N == 0 {
			return response, transportError(request, DeliverySent, resp.StatusCode, ClientErrorProtocol, ErrResponseTooLarge)
		}
		if decodeErr != nil {
			class := ClientErrorProtocol
			if errors.Is(decodeErr, context.Canceled) || errors.Is(decodeErr, context.DeadlineExceeded) || errors.Is(decodeErr, ErrSSEInterrupted) {
				class = ClientErrorNetwork
			}
			return response, transportError(request, DeliverySent, resp.StatusCode, class, decodeErr)
		}
		return response, nil

	default:
		return response, transportError(request, DeliverySent, resp.StatusCode, ClientErrorProtocol, ErrUnsupportedContentType)
	}
}

func transportError(
	request TransportRequest,
	delivery DeliveryState,
	statusCode int,
	class ClientErrorKind,
	cause error,
) *RequestError {
	return newRequestError(request.Operation, delivery, statusCode, class, cause)
}

func transportBodyError(request TransportRequest, statusCode int, err error) *RequestError {
	class := ClientErrorNetwork
	if errors.Is(err, ErrResponseTooLarge) {
		class = ClientErrorProtocol
	}
	return transportError(request, DeliverySent, statusCode, class, err)
}

func readBoundedResponseBody(ctx context.Context, body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	limited := &io.LimitedReader{R: body, N: maxResponseBytes + 1}
	payload, err := io.ReadAll(limited)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, errInvalidRPCResponse
	}
	if int64(len(payload)) > maxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	return payload, nil
}

func decodeRPCMessage(payload []byte, requestID json.RawMessage) (rpcMessage, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var message rpcMessage
	if err := decoder.Decode(&message); err != nil {
		return rpcMessage{}, false, errInvalidRPCResponse
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return rpcMessage{}, false, errInvalidRPCResponse
	}
	if message.JSONRPC != "2.0" {
		return rpcMessage{}, false, errInvalidRPCResponse
	}
	if message.Method != "" {
		if len(message.ID) != 0 {
			return rpcMessage{}, false, ErrUnsupportedServerRequest
		}
		return message, false, nil
	}
	if !equalRPCID(message.ID, requestID) {
		return rpcMessage{}, false, ErrMismatchedResponseID
	}
	return message, true, nil
}

func equalRPCID(left json.RawMessage, right json.RawMessage) bool {
	var normalizedLeft bytes.Buffer
	if err := json.Compact(&normalizedLeft, left); err != nil {
		return false
	}
	var normalizedRight bytes.Buffer
	if err := json.Compact(&normalizedRight, right); err != nil {
		return false
	}
	return bytes.Equal(normalizedLeft.Bytes(), normalizedRight.Bytes())
}

func validSessionID(value string) bool {
	if len(value) > maxSessionIDBytes || !httpguts.ValidHeaderFieldValue(value) {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func validTransportRequestID(operation OperationKind, requestID json.RawMessage) bool {
	switch operation {
	case OperationInitialized, OperationTerminate:
		return len(requestID) == 0
	default:
		return len(requestID) != 0
	}
}

func singleHeaderValue(header http.Header, name string) (string, bool, bool) {
	var value string
	found := false
	for key, values := range header {
		if !strings.EqualFold(key, name) {
			continue
		}
		if found || len(values) != 1 {
			return "", true, false
		}
		value = values[0]
		found = true
	}
	return value, found, true
}
