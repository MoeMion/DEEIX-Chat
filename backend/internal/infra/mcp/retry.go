package mcp

import (
	"context"
	"errors"
)

type RetryDecision uint8

const (
	RetryStop RetryDecision = iota
	RetrySameSession
	RetryNewSession
)

type RetryInput struct {
	Operation OperationKind
	Attempt   int
	Budget    int
	Delivery  DeliveryState
	Err       error
}

type RetryPolicy interface {
	Decide(RetryInput) RetryDecision
}

type ClassifiedRetryPolicy struct{}

func (ClassifiedRetryPolicy) Decide(input RetryInput) RetryDecision {
	if input.Attempt >= input.Budget || errors.Is(input.Err, context.Canceled) ||
		errors.Is(input.Err, context.DeadlineExceeded) || input.Operation == OperationTerminate ||
		input.Operation == OperationResumeSSE {
		return RetryStop
	}
	if errors.Is(input.Err, errTLSPolicyFailure) {
		return RetryStop
	}
	if errors.Is(input.Err, ErrSessionInvalid) {
		switch input.Operation {
		case OperationInitialize, OperationInitialized, OperationListTools:
			return RetryNewSession
		case OperationCallTool:
			if input.Delivery == DeliveryNotSent {
				return RetryNewSession
			}
		}
		return RetryStop
	}

	var requestErr *RequestError
	if errors.As(input.Err, &requestErr) && requestErr.Class == ClientErrorNetwork &&
		input.Delivery == DeliveryNotSent {
		switch input.Operation {
		case OperationInitialize, OperationInitialized, OperationListTools, OperationCallTool:
			return RetrySameSession
		}
	}
	return RetryStop
}
