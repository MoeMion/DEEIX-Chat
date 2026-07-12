package mcp

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestClassifiedRetryPolicyMatrix(t *testing.T) {
	transient := newRequestError(OperationListTools, DeliveryNotSent, 0, ClientErrorNetwork, errors.New("connect failed"))
	initializeTransient := newRequestError(OperationInitialize, DeliveryNotSent, 0, ClientErrorNetwork, errors.New("connect failed"))
	initializedTransient := newRequestError(OperationInitialized, DeliveryNotSent, 0, ClientErrorNetwork, errors.New("connect failed"))
	callTransient := newRequestError(OperationCallTool, DeliveryNotSent, 0, ClientErrorNetwork, errors.New("connect failed"))
	listInvalid := newRequestError(OperationListTools, DeliverySent, http.StatusNotFound, ClientErrorProtocol, ErrSessionInvalid)
	initializeInvalid := newRequestError(OperationInitialize, DeliverySent, http.StatusNotFound, ClientErrorProtocol, ErrSessionInvalid)
	initializedInvalid := newRequestError(OperationInitialized, DeliverySent, http.StatusNotFound, ClientErrorProtocol, ErrSessionInvalid)
	callNotSentInvalid := newRequestError(OperationCallTool, DeliveryNotSent, 0, ClientErrorProtocol, ErrSessionInvalid)
	callSent := newRequestError(OperationCallTool, DeliverySent, http.StatusBadGateway, ClientErrorHTTP, errors.New("remote failed"))
	callUnknown := newRequestError(OperationCallTool, DeliveryUnknown, 0, ClientErrorNetwork, errors.New("partial write"))
	listSent := newRequestError(OperationListTools, DeliverySent, http.StatusBadGateway, ClientErrorHTTP, errors.New("remote failed"))
	deterministic := newRequestError(OperationListTools, DeliveryNotSent, 0, ClientErrorProtocol, ErrInvalidSessionID)
	signingFailure := newRequestError(OperationCallTool, DeliveryNotSent, 0, ClientErrorProtocol, ErrInvalidSignedContext)
	configFailure := newClientError(ClientErrorProtocol, 0, 0, errors.New("invalid config"))
	jsonRPC := newClientError(ClientErrorJSONRPC, 0, -32603, nil)
	toolResult := newClientError(ClientErrorToolResult, 0, 0, nil)
	tlsPolicy := newRequestError(OperationListTools, DeliveryNotSent, 0, ClientErrorNetwork, errTLSPolicyFailure)
	for _, tt := range []struct {
		name string
		in   RetryInput
		want RetryDecision
	}{
		{name: "transient not sent", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: transient}, want: RetrySameSession},
		{name: "initialize transient not sent", in: RetryInput{Operation: OperationInitialize, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: initializeTransient}, want: RetrySameSession},
		{name: "initialized transient not sent", in: RetryInput{Operation: OperationInitialized, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: initializedTransient}, want: RetrySameSession},
		{name: "call transient not sent", in: RetryInput{Operation: OperationCallTool, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: callTransient}, want: RetrySameSession},
		{name: "list invalid", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 1, Delivery: DeliverySent, Err: listInvalid}, want: RetryNewSession},
		{name: "initialize invalid", in: RetryInput{Operation: OperationInitialize, Attempt: 0, Budget: 1, Delivery: DeliverySent, Err: initializeInvalid}, want: RetryNewSession},
		{name: "initialized invalid", in: RetryInput{Operation: OperationInitialized, Attempt: 0, Budget: 1, Delivery: DeliverySent, Err: initializedInvalid}, want: RetryNewSession},
		{name: "call not sent invalid", in: RetryInput{Operation: OperationCallTool, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: callNotSentInvalid}, want: RetryNewSession},
		{name: "call sent", in: RetryInput{Operation: OperationCallTool, Attempt: 0, Budget: 1, Delivery: DeliverySent, Err: callSent}, want: RetryStop},
		{name: "call unknown", in: RetryInput{Operation: OperationCallTool, Attempt: 0, Budget: 1, Delivery: DeliveryUnknown, Err: callUnknown}, want: RetryStop},
		{name: "list sent http", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 1, Delivery: DeliverySent, Err: listSent}, want: RetryStop},
		{name: "deterministic protocol", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: deterministic}, want: RetryStop},
		{name: "signing failure", in: RetryInput{Operation: OperationCallTool, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: signingFailure}, want: RetryStop},
		{name: "config failure", in: RetryInput{Operation: OperationInitialize, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: configFailure}, want: RetryStop},
		{name: "json rpc", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 1, Delivery: DeliverySent, Err: jsonRPC}, want: RetryStop},
		{name: "tool result", in: RetryInput{Operation: OperationCallTool, Attempt: 0, Budget: 1, Delivery: DeliverySent, Err: toolResult}, want: RetryStop},
		{name: "tls policy failure", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: tlsPolicy}, want: RetryStop},
		{name: "context canceled", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: context.Canceled}, want: RetryStop},
		{name: "context deadline", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: context.DeadlineExceeded}, want: RetryStop},
		{name: "zero budget", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 0, Delivery: DeliveryNotSent, Err: transient}, want: RetryStop},
		{name: "unknown operation", in: RetryInput{Operation: 0, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: transient}, want: RetryStop},
		{name: "budget exhausted", in: RetryInput{Operation: OperationListTools, Attempt: 1, Budget: 1, Delivery: DeliveryNotSent, Err: transient}, want: RetryStop},
		{name: "delete never retries", in: RetryInput{Operation: OperationTerminate, Attempt: 0, Budget: 5, Delivery: DeliveryNotSent, Err: transient}, want: RetryStop},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := (ClassifiedRetryPolicy{}).Decide(tt.in); got != tt.want {
				t.Fatalf("decision = %v, want %v", got, tt.want)
			}
		})
	}
}
