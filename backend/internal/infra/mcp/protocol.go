package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	protocolVersion           = "2025-06-18"
	maxResponseBytes    int64 = 8 << 20
	maxSSEEventBytes          = 1 << 20
	maxSSEEvents              = 1024
	maxSSEResumes             = 2
	maxToolListPages          = 128
	maxAccumulatedTools       = 10000
	maxSessionIDBytes         = 4096
	maxLastEventIDBytes       = 4096
)

var (
	ErrResponseTooLarge           = errors.New("mcp response exceeds limit")
	ErrSSEEventTooLarge           = errors.New("mcp sse event exceeds limit")
	ErrTooManySSEEvents           = errors.New("mcp sse event count exceeds limit")
	ErrSSEInterrupted             = errors.New("mcp sse stream interrupted")
	ErrUnsupportedContentType     = errors.New("mcp response content type is unsupported")
	ErrMismatchedResponseID       = errors.New("mcp response id does not match request")
	ErrUnsupportedServerRequest   = errors.New("mcp server request is unsupported")
	ErrSessionInvalid             = errors.New("mcp session is invalid")
	ErrInvalidSessionID           = errors.New("mcp session id is invalid")
	ErrInvalidLastEventID         = errors.New("mcp last event id is invalid")
	ErrPaginationCursorLoop       = errors.New("mcp tools list cursor repeated")
	ErrTooManyToolPages           = errors.New("mcp tools list page limit exceeded")
	ErrTooManyTools               = errors.New("mcp tools list result limit exceeded")
	ErrUnsupportedProtocolVersion = errors.New("mcp protocol version is unsupported")

	errInvalidRPCResponse      = errors.New("mcp json-rpc response is invalid")
	errInvalidTransportRequest = errors.New("mcp transport request is invalid")
)

type OperationKind uint8

const (
	OperationInitialize OperationKind = iota + 1
	OperationInitialized
	OperationListTools
	OperationCallTool
	OperationResumeSSE
	OperationTerminate
)

type DeliveryState uint8

const (
	DeliveryUnknown DeliveryState = iota
	DeliveryNotSent
	DeliverySent
)

type RequestError struct {
	Operation  OperationKind
	Delivery   DeliveryState
	StatusCode int
	Class      ClientErrorKind
	cause      error
}

func (e *RequestError) Error() string {
	if e == nil {
		return "mcp request failed"
	}
	return fmt.Sprintf(
		"mcp request failed: operation=%d delivery=%d status=%d class=%s",
		e.Operation,
		e.Delivery,
		e.StatusCode,
		e.Class,
	)
}

func (e *RequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newRequestError(
	operation OperationKind,
	delivery DeliveryState,
	statusCode int,
	class ClientErrorKind,
	cause error,
) *RequestError {
	return &RequestError{
		Operation:  operation,
		Delivery:   delivery,
		StatusCode: statusCode,
		Class:      class,
		cause:      cause,
	}
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code int `json:"code"`
}

type sessionState struct {
	ID              string
	ProtocolVersion string
}

type TransportRequest struct {
	Operation       OperationKind
	HTTPMethod      string
	Endpoint        string
	AuthToken       string
	Body            []byte
	RequestID       json.RawMessage
	Session         sessionState
	CustomHeaders   map[string]string
	TemplateContext TemplateContext
	SignedContext   *SignedContextConfig
	LastEventID     string
}

type TransportResponse struct {
	Message     rpcMessage
	SessionID   string
	LastEventID string
}

type Transport interface {
	Do(context.Context, TransportRequest) (TransportResponse, error)
}
