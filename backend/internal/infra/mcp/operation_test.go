package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
)

type transportStep struct {
	operation   OperationKind
	httpMethod  string
	rpcMethod   string
	sessionID   string
	version     string
	lastEventID string
	result      json.RawMessage
	response    TransportResponse
	err         error
}

type scriptedTransport struct {
	t     *testing.T
	mu    sync.Mutex
	steps []transportStep
	seen  []TransportRequest
}

func (s *scriptedTransport) Do(_ context.Context, req TransportRequest) (TransportResponse, error) {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steps) == 0 {
		s.t.Fatalf("unexpected request: operation=%v method=%s", req.Operation, rpcMethod(req))
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	s.seen = append(s.seen, req)
	if req.Operation != step.operation || req.HTTPMethod != step.httpMethod || rpcMethod(req) != step.rpcMethod ||
		req.Session.ID != step.sessionID || req.Session.ProtocolVersion != step.version || req.LastEventID != step.lastEventID {
		s.t.Fatalf("request = %#v, step = %#v", req, step)
	}
	if req.Operation == OperationInitialize {
		var envelope struct {
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if err := json.Unmarshal(req.Body, &envelope); err != nil || envelope.Params.ProtocolVersion != protocolVersion {
			s.t.Fatalf("initialize protocol version = %q, want %q (decode error %v)", envelope.Params.ProtocolVersion, protocolVersion, err)
		}
	}
	response := step.response
	if step.result != nil {
		response.Message = rpcMessage{JSONRPC: "2.0", ID: append(json.RawMessage(nil), req.RequestID...), Result: step.result}
	}
	return response, step.err
}

func (s *scriptedTransport) assertDone() {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steps) != 0 {
		s.t.Fatalf("%d transport steps were not consumed", len(s.steps))
	}
}

func rpcMethod(req TransportRequest) string {
	if len(req.Body) == 0 {
		return ""
	}
	var envelope struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(req.Body, &envelope)
	return envelope.Method
}

func initializeStep(sessionID string, version string) transportStep {
	result, _ := json.Marshal(map[string]interface{}{
		"protocolVersion": version,
		"capabilities":    map[string]interface{}{},
		"serverInfo":      map[string]string{"name": "test", "version": "1.0.0"},
	})
	return transportStep{
		operation:  OperationInitialize,
		httpMethod: http.MethodPost,
		rpcMethod:  "initialize",
		result:     result,
		response:   TransportResponse{SessionID: sessionID},
	}
}

func initializedStep(sessionID string) transportStep {
	return transportStep{
		operation:  OperationInitialized,
		httpMethod: http.MethodPost,
		rpcMethod:  "notifications/initialized",
		sessionID:  sessionID,
		version:    protocolVersion,
	}
}

func listStep(sessionID string, result string) transportStep {
	return transportStep{
		operation:  OperationListTools,
		httpMethod: http.MethodPost,
		rpcMethod:  "tools/list",
		sessionID:  sessionID,
		version:    protocolVersion,
		result:     json.RawMessage(result),
	}
}

func callStep(sessionID string, result string) transportStep {
	return transportStep{
		operation:  OperationCallTool,
		httpMethod: http.MethodPost,
		rpcMethod:  "tools/call",
		sessionID:  sessionID,
		version:    protocolVersion,
		result:     json.RawMessage(result),
	}
}

func resumeStep(sessionID string, cursor string, result string) transportStep {
	return transportStep{
		operation:   OperationResumeSSE,
		httpMethod:  http.MethodGet,
		sessionID:   sessionID,
		version:     protocolVersion,
		lastEventID: cursor,
		result:      json.RawMessage(result),
	}
}

func interruptedStep(step transportStep, cursor string) transportStep {
	step.response.LastEventID = cursor
	step.err = newRequestError(step.operation, DeliverySent, 0, ClientErrorNetwork, ErrSSEInterrupted)
	return step
}

func terminateStep(sessionID string) transportStep {
	return transportStep{
		operation:  OperationTerminate,
		httpMethod: http.MethodDelete,
		sessionID:  sessionID,
		version:    protocolVersion,
	}
}

func testCallConfig() CallConfig {
	return CallConfig{
		BaseURL:       "https://mcp.example.test/rpc",
		TimeoutMS:     1000,
		CustomHeaders: map[string]string{"X-Tenant": "tenant-1"},
		Context: TemplateContext{
			Mode:         ContextModeChat,
			UserPublicID: "user-1",
			RunID:        "run-1",
		},
	}
}

func TestOperationLifecycleUses20251125AndDeletes(t *testing.T) {
	transport := &scriptedTransport{t: t, steps: []transportStep{
		initializeStep("session-1", "2025-11-25"),
		initializedStep("session-1"),
		listStep("session-1", `{"tools":[]}`),
		terminateStep("session-1"),
	}}
	op, err := newOperation(transport, testCallConfig(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = op.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = op.terminate(context.Background()); err != nil {
		t.Fatal(err)
	}
	transport.assertDone()
}

func TestOperationSnapshotsImmutableRequestContext(t *testing.T) {
	originalContext := TemplateContext{
		Mode:                     ContextModeChat,
		UserPublicID:             "user-original",
		UserDisplayName:          "Original User",
		UserEmail:                "original@example.test",
		UserRole:                 "member",
		ConversationPublicID:     "conversation-original",
		AssistantMessagePublicID: "assistant-original",
		UserMessagePublicID:      "message-original",
		RequestID:                "request-original",
		RunID:                    "run-original",
		TraceID:                  "trace-original",
	}
	originalSigned := SignedContextConfig{
		Secret:         "secret-original",
		Issuer:         "issuer-original",
		Audience:       "audience-original",
		KeyID:          "key-original",
		ExpiresSeconds: 300,
		IncludeName:    true,
		IncludeEmail:   true,
		IncludeRole:    true,
	}
	cfg := testCallConfig()
	cfg.CustomHeaders = map[string]string{"X-Tenant": "tenant-original"}
	cfg.Context = originalContext
	cfg.SignedContext = &originalSigned
	transport := &scriptedTransport{t: t, steps: []transportStep{
		initializeStep("session-snapshot", protocolVersion),
		initializedStep("session-snapshot"),
		listStep("session-snapshot", `{"tools":[]}`),
		terminateStep("session-snapshot"),
	}}
	op, err := newOperation(transport, cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CustomHeaders["X-Tenant"] = "tenant-mutated"
	cfg.CustomHeaders["X-Injected"] = "mutated"
	cfg.Context.UserPublicID = "user-mutated"
	originalSigned.Secret = "secret-mutated"
	originalSigned.Audience = "audience-mutated"

	if _, err = op.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = op.terminate(context.Background()); err != nil {
		t.Fatal(err)
	}
	transport.assertDone()
	wantSigned := SignedContextConfig{
		Secret:         "secret-original",
		Issuer:         "issuer-original",
		Audience:       "audience-original",
		KeyID:          "key-original",
		ExpiresSeconds: 300,
		IncludeName:    true,
		IncludeEmail:   true,
		IncludeRole:    true,
	}
	for index, req := range transport.seen {
		if !reflect.DeepEqual(req.CustomHeaders, map[string]string{"X-Tenant": "tenant-original"}) {
			t.Fatalf("request %d headers = %#v", index, req.CustomHeaders)
		}
		if !reflect.DeepEqual(req.TemplateContext, originalContext) {
			t.Fatalf("request %d context = %#v", index, req.TemplateContext)
		}
		if req.SignedContext == nil || !reflect.DeepEqual(*req.SignedContext, wantSigned) {
			t.Fatalf("request %d signed config = %#v", index, req.SignedContext)
		}
	}
}

func TestOperationRejectsNegotiationAndUnsafeSessionIDs(t *testing.T) {
	for _, tt := range []struct {
		name      string
		sessionID string
		version   string
		wantErr   error
		cleanup   bool
	}{
		{name: "different version", sessionID: "session-1", version: "2025-06-18", wantErr: ErrUnsupportedProtocolVersion, cleanup: true},
		{name: "control byte", sessionID: "bad\n-session", version: "2025-11-25", wantErr: ErrInvalidSessionID},
		{name: "oversized", sessionID: strings.Repeat("s", maxSessionIDBytes+1), version: "2025-11-25", wantErr: ErrInvalidSessionID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			steps := []transportStep{initializeStep(tt.sessionID, tt.version)}
			if tt.cleanup {
				steps = append(steps, terminateStep(tt.sessionID))
			}
			transport := &scriptedTransport{t: t, steps: steps}
			op, err := newOperation(transport, testCallConfig(), 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = op.ListTools(context.Background())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			transport.assertDone()
		})
	}
}

func TestOperationMalformedInitializeDeletesCapturedSession(t *testing.T) {
	decodeErr := newRequestError(OperationInitialize, DeliverySent, 0, ClientErrorProtocol, errors.New("malformed response"))
	transport := &scriptedTransport{t: t, steps: []transportStep{
		{
			operation:  OperationInitialize,
			httpMethod: http.MethodPost,
			rpcMethod:  "initialize",
			response:   TransportResponse{SessionID: "session-captured"},
			err:        decodeErr,
		},
		terminateStep("session-captured"),
	}}
	op, err := newOperation(transport, testCallConfig(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = op.ListTools(context.Background()); err == nil {
		t.Fatal("expected initialize failure")
	}
	transport.assertDone()
}

func TestOperationInitialized404ReinitializesBeforeAnyToolCall(t *testing.T) {
	invalid := newRequestError(OperationInitialized, DeliverySent, http.StatusNotFound, ClientErrorProtocol, ErrSessionInvalid)
	failedInitialized := initializedStep("session-old")
	failedInitialized.err = invalid
	transport := &scriptedTransport{t: t, steps: []transportStep{
		initializeStep("session-old", "2025-11-25"),
		failedInitialized,
		initializeStep("session-new", "2025-11-25"),
		initializedStep("session-new"),
		listStep("session-new", `{"tools":[]}`),
	}}
	op, err := newOperation(transport, testCallConfig(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = op.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	transport.assertDone()
	for _, req := range transport.seen {
		if (req.Operation == OperationListTools || req.Operation == OperationCallTool) && req.Session.ID == "session-old" {
			t.Fatalf("tool request used invalid session: %#v", req)
		}
	}
}

func TestOperationRetryBudgetSpansInitializationAndToolBoundary(t *testing.T) {
	initializedInvalid := initializedStep("session-old")
	initializedInvalid.err = newRequestError(
		OperationInitialized,
		DeliverySent,
		http.StatusNotFound,
		ClientErrorProtocol,
		ErrSessionInvalid,
	)
	transientCause := errors.New("transient list connect failure")
	listTransient := listStep("session-new", "")
	listTransient.err = newRequestError(
		OperationListTools,
		DeliveryNotSent,
		0,
		ClientErrorNetwork,
		transientCause,
	)
	transport := &scriptedTransport{t: t, steps: []transportStep{
		initializeStep("session-old", protocolVersion),
		initializedInvalid,
		initializeStep("session-new", protocolVersion),
		initializedStep("session-new"),
		listTransient,
	}}
	op, err := newOperation(transport, testCallConfig(), 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = op.ListTools(context.Background())
	if !errors.Is(err, transientCause) {
		t.Fatalf("error = %v, want %v", err, transientCause)
	}
	transport.assertDone()
}

func TestOperationSessionReplacementRetainsOldHandleWhenCleanupFails(t *testing.T) {
	cleanupFailure := newRequestError(
		OperationTerminate,
		DeliverySent,
		http.StatusInternalServerError,
		ClientErrorHTTP,
		errors.New("cleanup failed"),
	)
	replacement := listStep("session-old", `{"tools":[]}`)
	replacement.response.SessionID = "session-new"
	failedOldDelete := terminateStep("session-old")
	failedOldDelete.err = cleanupFailure
	transport := &scriptedTransport{t: t, steps: []transportStep{
		initializeStep("session-old", protocolVersion),
		initializedStep("session-old"),
		replacement,
		failedOldDelete,
		terminateStep("session-new"),
		terminateStep("session-old"),
	}}
	op, err := newOperation(transport, testCallConfig(), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = op.ListTools(context.Background())
	if !errors.Is(err, cleanupFailure) {
		t.Fatalf("error = %v, want %v", err, cleanupFailure)
	}
	if op.session.ID != "session-old" {
		t.Fatalf("retained session = %q, want session-old", op.session.ID)
	}
	if err = op.terminate(context.Background()); err != nil {
		t.Fatal(err)
	}
	transport.assertDone()
}

func TestOperationTerminateClearsStateAndTreats404And405AsClosed(t *testing.T) {
	cleanupFailure := newRequestError(
		OperationTerminate,
		DeliverySent,
		http.StatusInternalServerError,
		ClientErrorHTTP,
		errors.New("cleanup failed"),
	)
	for _, tt := range []struct {
		name    string
		err     error
		wantErr error
	}{
		{name: "success"},
		{name: "not found", err: newRequestError(OperationTerminate, DeliverySent, http.StatusNotFound, ClientErrorHTTP, nil)},
		{name: "method not allowed", err: newRequestError(OperationTerminate, DeliverySent, http.StatusMethodNotAllowed, ClientErrorHTTP, nil)},
		{name: "failure still clears", err: cleanupFailure, wantErr: cleanupFailure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deleteStep := terminateStep("session-terminate")
			deleteStep.err = tt.err
			transport := &scriptedTransport{t: t, steps: []transportStep{
				initializeStep("session-terminate", protocolVersion),
				initializedStep("session-terminate"),
				listStep("session-terminate", `{"tools":[]}`),
				deleteStep,
			}}
			op, err := newOperation(transport, testCallConfig(), 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = op.ListTools(context.Background()); err != nil {
				t.Fatal(err)
			}
			err = op.terminate(context.Background())
			if tt.wantErr == nil && err != nil {
				t.Fatalf("terminate error = %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("terminate error = %v, want %v", err, tt.wantErr)
			}
			if op.session.ID != "" || op.isInitialized {
				t.Fatalf("operation retained state: session=%#v initialized=%v", op.session, op.isInitialized)
			}
			if err = op.terminate(context.Background()); err != nil {
				t.Fatalf("second terminate error = %v", err)
			}
			transport.assertDone()
		})
	}
}

type gateHarness struct {
	callDispatched chan struct{}
}

func newGateHarness() *gateHarness {
	return &gateHarness{
		callDispatched: make(chan struct{}, 2),
	}
}

func (h *gateHarness) Do(_ context.Context, req TransportRequest) (TransportResponse, error) {
	switch req.Operation {
	case OperationInitialize:
		step := initializeStep("session-gate", protocolVersion)
		return TransportResponse{
			Message:   rpcMessage{JSONRPC: "2.0", ID: req.RequestID, Result: step.result},
			SessionID: "session-gate",
		}, nil
	case OperationInitialized, OperationTerminate:
		return TransportResponse{}, nil
	case OperationCallTool:
		h.callDispatched <- struct{}{}
		return TransportResponse{Message: rpcMessage{
			JSONRPC: "2.0",
			ID:      req.RequestID,
			Result:  json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`),
		}}, nil
	default:
		return TransportResponse{}, fmt.Errorf("unexpected operation %d", req.Operation)
	}
}

func TestOperationGateSerializesSameSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		harness := newGateHarness()
		op, err := newOperation(harness, testCallConfig(), 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = op.CallTool(context.Background(), CallInput{ToolName: "first", ArgumentsJSON: `{}`}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-harness.callDispatched:
		default:
			t.Fatal("first call did not reach the transport")
		}

		op.gate <- struct{}{} // Simulate an in-flight operation holding this established session's gate.
		callDone := make(chan error, 1)
		go func() {
			_, callErr := op.CallTool(context.Background(), CallInput{ToolName: "second", ArgumentsJSON: `{}`})
			callDone <- callErr
		}()
		synctest.Wait()
		select {
		case <-harness.callDispatched:
			t.Fatal("second call dispatched while the operation gate was held")
		default:
		}

		<-op.gate
		synctest.Wait()
		select {
		case err = <-callDone:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("second call did not finish after gate release")
		}
		select {
		case <-harness.callDispatched:
		default:
			t.Fatal("second call did not dispatch after gate release")
		}
		if err = op.terminate(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOperationGateCanceledWaiterReturnsWithoutDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		harness := newGateHarness()
		op, err := newOperation(harness, testCallConfig(), 0)
		if err != nil {
			t.Fatal(err)
		}
		op.gate <- struct{}{}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		waiterDone := make(chan error, 1)
		go func() {
			_, callErr := op.CallTool(canceled, CallInput{ToolName: "echo", ArgumentsJSON: `{}`})
			waiterDone <- callErr
		}()
		synctest.Wait()
		select {
		case err = <-waiterDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("waiter error = %v, want %v", err, context.Canceled)
			}
		default:
			t.Fatal("canceled waiter did not exit")
		}
		select {
		case <-harness.callDispatched:
			t.Fatal("canceled waiter dispatched a call")
		default:
		}
		<-op.gate
	})
}

func TestOperationSSEResumePolicy(t *testing.T) {
	callResult := `{"content":[{"type":"text","text":"ok"}]}`
	resume404 := resumeStep("session-1", "evt-1", "")
	resume404.err = newRequestError(OperationResumeSSE, DeliverySent, http.StatusNotFound, ClientErrorProtocol, ErrSessionInvalid)
	for _, tt := range []struct {
		name        string
		sessionID   string
		steps       []transportStep
		wantErr     error
		wantCalls   int
		wantResumes int
	}{
		{
			name:      "resume succeeds",
			sessionID: "session-1",
			steps: []transportStep{
				interruptedStep(callStep("session-1", ""), "evt-1"),
				resumeStep("session-1", "evt-1", callResult),
			},
			wantCalls: 1, wantResumes: 1,
		},
		{
			name:      "missing event id stops",
			sessionID: "session-1",
			steps:     []transportStep{interruptedStep(callStep("session-1", ""), "")},
			wantErr:   ErrSSEInterrupted, wantCalls: 1,
		},
		{
			name:      "missing session stops",
			sessionID: "",
			steps:     []transportStep{interruptedStep(callStep("", ""), "evt-1")},
			wantErr:   ErrSSEInterrupted, wantCalls: 1,
		},
		{
			name:      "invalid event id stops",
			sessionID: "session-1",
			steps:     []transportStep{interruptedStep(callStep("session-1", ""), "bad\nevent")},
			wantErr:   ErrSSEInterrupted, wantCalls: 1,
		},
		{
			name:      "two resumes only",
			sessionID: "session-1",
			steps: []transportStep{
				interruptedStep(callStep("session-1", ""), "evt-1"),
				interruptedStep(resumeStep("session-1", "evt-1", ""), "evt-2"),
				interruptedStep(resumeStep("session-1", "evt-2", ""), "evt-3"),
			},
			wantErr: ErrSSEInterrupted, wantCalls: 1, wantResumes: 2,
		},
		{
			name:      "resume 404 never replays call",
			sessionID: "session-1",
			steps: []transportStep{
				interruptedStep(callStep("session-1", ""), "evt-1"),
				resume404,
			},
			wantErr: ErrSessionInvalid, wantCalls: 1, wantResumes: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			steps := []transportStep{initializeStep(tt.sessionID, "2025-11-25"), initializedStep(tt.sessionID)}
			steps = append(steps, tt.steps...)
			transport := &scriptedTransport{t: t, steps: steps}
			op, err := newOperation(transport, testCallConfig(), 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = op.CallTool(context.Background(), CallInput{ToolName: "echo", ArgumentsJSON: `{}`})
			if tt.wantErr == nil && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			transport.assertDone()
			calls, resumes := 0, 0
			var callRequestID json.RawMessage
			for _, req := range transport.seen {
				switch req.Operation {
				case OperationCallTool:
					calls++
					if callRequestID == nil {
						callRequestID = append(json.RawMessage(nil), req.RequestID...)
					}
				case OperationResumeSSE:
					resumes++
					if !equalRPCID(callRequestID, req.RequestID) {
						t.Fatalf("resume request id = %s, want original %s", req.RequestID, callRequestID)
					}
				}
			}
			if calls != tt.wantCalls || resumes != tt.wantResumes {
				t.Fatalf("calls/resumes = %d/%d, want %d/%d", calls, resumes, tt.wantCalls, tt.wantResumes)
			}
		})
	}
}

func TestOperationListToolsResume404RestartsFromPageOne(t *testing.T) {
	resumeInvalid := resumeStep("session-old", "evt-list", "")
	resumeInvalid.err = newRequestError(
		OperationResumeSSE,
		DeliverySent,
		http.StatusNotFound,
		ClientErrorProtocol,
		ErrSessionInvalid,
	)
	transport := &scriptedTransport{t: t, steps: []transportStep{
		initializeStep("session-old", protocolVersion),
		initializedStep("session-old"),
		interruptedStep(listStep("session-old", ""), "evt-list"),
		resumeInvalid,
		initializeStep("session-new", protocolVersion),
		initializedStep("session-new"),
		listStep("session-new", `{"tools":[{"name":"after-restart"}]}`),
	}}
	op, err := newOperation(transport, testCallConfig(), 1)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := op.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(tools); !reflect.DeepEqual(got, []string{"after-restart"}) {
		t.Fatalf("tools = %v, want [after-restart]", got)
	}
	transport.assertDone()
	lists, resumes := 0, 0
	for _, req := range transport.seen {
		switch req.Operation {
		case OperationListTools:
			lists++
		case OperationResumeSSE:
			resumes++
		}
	}
	if lists != 2 || resumes != 1 {
		t.Fatalf("lists/resumes = %d/%d, want 2/1", lists, resumes)
	}
}

func TestOperationCallToolResumeFailureNeverReplaysOriginalPost(t *testing.T) {
	resumeCause := errors.New("resume connect failure")
	resumeFailure := resumeStep("session-1", "evt-1", "")
	resumeFailure.err = newRequestError(
		OperationResumeSSE,
		DeliveryNotSent,
		0,
		ClientErrorNetwork,
		resumeCause,
	)
	transport := &scriptedTransport{t: t, steps: []transportStep{
		initializeStep("session-1", protocolVersion),
		initializedStep("session-1"),
		interruptedStep(callStep("session-1", ""), "evt-1"),
		resumeFailure,
	}}
	op, err := newOperation(transport, testCallConfig(), 3)
	if err != nil {
		t.Fatal(err)
	}
	_, err = op.CallTool(context.Background(), CallInput{ToolName: "echo", ArgumentsJSON: `{}`})
	if !errors.Is(err, resumeCause) {
		t.Fatalf("error = %v, want %v", err, resumeCause)
	}
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Operation != OperationCallTool || requestErr.Delivery != DeliverySent {
		t.Fatalf("logical call delivery = %#v, want sent call", requestErr)
	}
	transport.assertDone()
	calls, resumes := 0, 0
	for _, req := range transport.seen {
		switch req.Operation {
		case OperationCallTool:
			calls++
		case OperationResumeSSE:
			resumes++
		}
	}
	if calls != 1 || resumes != 1 {
		t.Fatalf("calls/resumes = %d/%d, want 1/1", calls, resumes)
	}
}

func TestOperationCallToolClassifiedRetryNeverReplaysAmbiguousDelivery(t *testing.T) {
	callResult := `{"content":[{"type":"text","text":"ok"}]}`
	errTransient := errors.New("transient connect failure")
	errAmbiguous := errors.New("ambiguous write failure")
	errSessionNotSent := newRequestError(OperationCallTool, DeliveryNotSent, 0, ClientErrorProtocol, ErrSessionInvalid)
	errSessionSent := newRequestError(OperationCallTool, DeliverySent, http.StatusNotFound, ClientErrorProtocol, ErrSessionInvalid)
	failedCall := func(sessionID string, delivery DeliveryState, cause error) transportStep {
		step := callStep(sessionID, "")
		step.err = newRequestError(OperationCallTool, delivery, 0, ClientErrorNetwork, cause)
		return step
	}
	for _, tt := range []struct {
		name      string
		retry     int
		steps     []transportStep
		wantErr   error
		wantCalls int
		wantInits int
	}{
		{
			name:  "not sent retries same session",
			retry: 1,
			steps: []transportStep{
				initializeStep("session-1", protocolVersion),
				initializedStep("session-1"),
				failedCall("session-1", DeliveryNotSent, errTransient),
				callStep("session-1", callResult),
			},
			wantCalls: 2, wantInits: 1,
		},
		{
			name:  "unknown delivery stops",
			retry: 3,
			steps: []transportStep{
				initializeStep("session-1", protocolVersion),
				initializedStep("session-1"),
				failedCall("session-1", DeliveryUnknown, errAmbiguous),
			},
			wantErr: errAmbiguous, wantCalls: 1, wantInits: 1,
		},
		{
			name:  "sent delivery stops",
			retry: 3,
			steps: []transportStep{
				initializeStep("session-1", protocolVersion),
				initializedStep("session-1"),
				failedCall("session-1", DeliverySent, errAmbiguous),
			},
			wantErr: errAmbiguous, wantCalls: 1, wantInits: 1,
		},
		{
			name:  "not sent invalid session rebuilds",
			retry: 1,
			steps: []transportStep{
				initializeStep("session-old", protocolVersion),
				initializedStep("session-old"),
				{
					operation:  OperationCallTool,
					httpMethod: http.MethodPost,
					rpcMethod:  "tools/call",
					sessionID:  "session-old",
					version:    protocolVersion,
					err:        errSessionNotSent,
				},
				initializeStep("session-new", protocolVersion),
				initializedStep("session-new"),
				callStep("session-new", callResult),
			},
			wantCalls: 2, wantInits: 2,
		},
		{
			name:  "sent invalid session stops",
			retry: 3,
			steps: []transportStep{
				initializeStep("session-1", protocolVersion),
				initializedStep("session-1"),
				{
					operation:  OperationCallTool,
					httpMethod: http.MethodPost,
					rpcMethod:  "tools/call",
					sessionID:  "session-1",
					version:    protocolVersion,
					err:        errSessionSent,
				},
			},
			wantErr: ErrSessionInvalid, wantCalls: 1, wantInits: 1,
		},
		{
			name: "zero budget has no hidden retry",
			steps: []transportStep{
				initializeStep("session-1", protocolVersion),
				initializedStep("session-1"),
				failedCall("session-1", DeliveryNotSent, errTransient),
			},
			wantErr: errTransient, wantCalls: 1, wantInits: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := &scriptedTransport{t: t, steps: tt.steps}
			op, err := newOperation(transport, testCallConfig(), tt.retry)
			if err != nil {
				t.Fatal(err)
			}
			_, err = op.CallTool(context.Background(), CallInput{ToolName: "echo", ArgumentsJSON: `{}`})
			if tt.wantErr == nil && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			transport.assertDone()
			calls, inits := 0, 0
			for _, req := range transport.seen {
				switch req.Operation {
				case OperationCallTool:
					calls++
				case OperationInitialize:
					inits++
				}
			}
			if calls != tt.wantCalls || inits != tt.wantInits {
				t.Fatalf("calls/inits = %d/%d, want %d/%d", calls, inits, tt.wantCalls, tt.wantInits)
			}
		})
	}
}

type transportFunc func(context.Context, TransportRequest) (TransportResponse, error)

func (f transportFunc) Do(ctx context.Context, req TransportRequest) (TransportResponse, error) {
	return f(ctx, req)
}

type paginationHarness struct {
	list             func(call int, cursor string) (json.RawMessage, error)
	initializeCount  int
	listCount        int
	cursors          []string
	currentSessionID string
}

func (h *paginationHarness) Do(_ context.Context, req TransportRequest) (TransportResponse, error) {
	switch req.Operation {
	case OperationInitialize:
		h.initializeCount++
		h.currentSessionID = "session-" + strconv.Itoa(h.initializeCount)
		step := initializeStep(h.currentSessionID, "2025-11-25")
		response := step.response
		response.Message = rpcMessage{JSONRPC: "2.0", ID: req.RequestID, Result: step.result}
		return response, nil
	case OperationInitialized:
		return TransportResponse{}, nil
	case OperationListTools:
		h.listCount++
		cursor := listCursor(req.Body)
		h.cursors = append(h.cursors, cursor)
		result, err := h.list(h.listCount, cursor)
		if err != nil {
			return TransportResponse{}, err
		}
		return TransportResponse{Message: rpcMessage{JSONRPC: "2.0", ID: req.RequestID, Result: result}}, nil
	default:
		return TransportResponse{}, fmt.Errorf("unexpected operation %d", req.Operation)
	}
}

func listCursor(body []byte) string {
	var envelope struct {
		Params map[string]string `json:"params"`
	}
	_ = json.Unmarshal(body, &envelope)
	return envelope.Params["cursor"]
}

func listPage(names []string, next string) json.RawMessage {
	tools := make([]Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, Tool{Name: name})
	}
	raw, _ := json.Marshal(map[string]interface{}{"tools": tools, "nextCursor": next})
	return raw
}

func toolNames(tools []Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestOperationListToolsPaginationContracts(t *testing.T) {
	invalidSession := newRequestError(OperationListTools, DeliverySent, http.StatusNotFound, ClientErrorProtocol, ErrSessionInvalid)
	tooMany := make([]string, maxAccumulatedTools+1)
	for i := range tooMany {
		tooMany[i] = "tool-" + strconv.Itoa(i)
	}
	for _, tt := range []struct {
		name       string
		retry      int
		list       func(int, string) (json.RawMessage, error)
		wantNames  []string
		wantErr    error
		wantCalls  int
		wantInits  int
		wantCursor []string
	}{
		{
			name: "three pages",
			list: func(call int, _ string) (json.RawMessage, error) {
				pages := []json.RawMessage{listPage([]string{"a"}, "c1"), listPage([]string{"b"}, "c2"), listPage([]string{"c"}, "")}
				return pages[call-1], nil
			},
			wantNames: []string{"a", "b", "c"}, wantCalls: 3, wantInits: 1, wantCursor: []string{"", "c1", "c2"},
		},
		{
			name: "cursor loop",
			list: func(call int, _ string) (json.RawMessage, error) {
				return listPage([]string{strconv.Itoa(call)}, "same"), nil
			},
			wantErr: ErrPaginationCursorLoop, wantCalls: 2, wantInits: 1, wantCursor: []string{"", "same"},
		},
		{
			name: "page limit",
			list: func(call int, _ string) (json.RawMessage, error) {
				return listPage(nil, "cursor-"+strconv.Itoa(call)), nil
			},
			wantErr: ErrTooManyToolPages, wantCalls: maxToolListPages, wantInits: 1,
		},
		{
			name: "page exact limit",
			list: func(call int, _ string) (json.RawMessage, error) {
				next := ""
				if call < maxToolListPages {
					next = "cursor-" + strconv.Itoa(call)
				}
				return listPage(nil, next), nil
			},
			wantNames: []string{}, wantCalls: maxToolListPages, wantInits: 1,
		},
		{
			name:    "tool limit",
			list:    func(int, string) (json.RawMessage, error) { return listPage(tooMany, ""), nil },
			wantErr: ErrTooManyTools, wantCalls: 1, wantInits: 1,
		},
		{
			name:  "404 restarts from page one",
			retry: 1,
			list: func(call int, _ string) (json.RawMessage, error) {
				switch call {
				case 1, 3:
					return listPage([]string{"a"}, "c1"), nil
				case 2:
					return nil, invalidSession
				default:
					return listPage([]string{"b"}, ""), nil
				}
			},
			wantNames: []string{"a", "b"}, wantCalls: 4, wantInits: 2, wantCursor: []string{"", "c1", "", "c1"},
		},
		{
			name:    "404 with zero budget",
			list:    func(int, string) (json.RawMessage, error) { return nil, invalidSession },
			wantErr: ErrSessionInvalid, wantCalls: 1, wantInits: 1, wantCursor: []string{""},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			harness := &paginationHarness{list: tt.list}
			op, err := newOperation(harness, testCallConfig(), tt.retry)
			if err != nil {
				t.Fatal(err)
			}
			tools, err := op.ListTools(context.Background())
			if tt.wantErr == nil && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantNames != nil && !reflect.DeepEqual(toolNames(tools), tt.wantNames) {
				t.Fatalf("tools = %v, want %v", toolNames(tools), tt.wantNames)
			}
			if harness.listCount != tt.wantCalls || harness.initializeCount != tt.wantInits {
				t.Fatalf("calls/inits = %d/%d, want %d/%d", harness.listCount, harness.initializeCount, tt.wantCalls, tt.wantInits)
			}
			if tt.wantCursor != nil && !reflect.DeepEqual(harness.cursors, tt.wantCursor) {
				t.Fatalf("cursors = %v, want %v", harness.cursors, tt.wantCursor)
			}
		})
	}
}

func TestOperationListToolsAcceptsExactToolLimit(t *testing.T) {
	names := make([]string, maxAccumulatedTools)
	for index := range names {
		names[index] = "tool-" + strconv.Itoa(index)
	}
	harness := &paginationHarness{list: func(int, string) (json.RawMessage, error) {
		return listPage(names, ""), nil
	}}
	op, err := newOperation(harness, testCallConfig(), 0)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := op.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != maxAccumulatedTools || tools[0].Name != "tool-0" ||
		tools[len(tools)-1].Name != "tool-"+strconv.Itoa(maxAccumulatedTools-1) {
		t.Fatalf("tool bounds = len %d first %q last %q", len(tools), tools[0].Name, tools[len(tools)-1].Name)
	}
	if harness.listCount != 1 {
		t.Fatalf("list calls = %d, want 1", harness.listCount)
	}
}

func TestListAccumulatorRetainedByteBudgetAndReset(t *testing.T) {
	page := []Tool{{
		Name:        "n",
		Title:       "t",
		Description: "d",
		InputSchema: json.RawMessage(`{}`),
	}}
	const pageBytes int64 = 5
	const cursor = "cursor"
	const cursorBytes int64 = int64(len(cursor))

	t.Run("exact combined boundary", func(t *testing.T) {
		accumulator := listAccumulator{retainedBytes: maxRetainedListBytes - pageBytes - cursorBytes}
		if err := accumulator.appendPage(page, cursor, false); err != nil {
			t.Fatal(err)
		}
		if accumulator.retainedBytes != maxRetainedListBytes || len(accumulator.tools) != 1 {
			t.Fatalf("accumulator = bytes %d tools %d", accumulator.retainedBytes, len(accumulator.tools))
		}
		if _, exists := accumulator.seenCursors[cursor]; !exists {
			t.Fatalf("cursor %q was not retained", cursor)
		}
	})

	t.Run("combined one byte over is transactional", func(t *testing.T) {
		accumulator := listAccumulator{
			tools:         []Tool{{Name: "existing"}},
			seenCursors:   map[string]struct{}{"kept": {}},
			retainedBytes: maxRetainedListBytes - pageBytes - cursorBytes + 1,
		}
		beforeBytes := accumulator.retainedBytes
		beforeTools := append([]Tool(nil), accumulator.tools...)
		beforeCursors := map[string]struct{}{"kept": {}}
		if err := accumulator.appendPage(page, cursor, false); !errors.Is(err, ErrRetainedListTooLarge) {
			t.Fatalf("error = %v, want %v", err, ErrRetainedListTooLarge)
		}
		if accumulator.retainedBytes != beforeBytes || !reflect.DeepEqual(accumulator.tools, beforeTools) ||
			!reflect.DeepEqual(accumulator.seenCursors, beforeCursors) {
			t.Fatalf("overflow mutated accumulator = bytes %d tools %#v cursors %#v", accumulator.retainedBytes, accumulator.tools, accumulator.seenCursors)
		}
	})

	t.Run("exact cursor boundary", func(t *testing.T) {
		accumulator := listAccumulator{retainedBytes: maxRetainedListBytes - cursorBytes}
		if err := accumulator.appendPage(nil, cursor, false); err != nil {
			t.Fatal(err)
		}
		if accumulator.retainedBytes != maxRetainedListBytes {
			t.Fatalf("retained bytes = %d, want %d", accumulator.retainedBytes, maxRetainedListBytes)
		}
		if _, exists := accumulator.seenCursors[cursor]; !exists {
			t.Fatalf("cursor %q was not retained", cursor)
		}
	})

	t.Run("cursor one byte over does not mutate", func(t *testing.T) {
		accumulator := listAccumulator{
			seenCursors:   map[string]struct{}{"kept": {}},
			retainedBytes: maxRetainedListBytes - cursorBytes + 1,
		}
		beforeBytes := accumulator.retainedBytes
		if err := accumulator.appendPage(nil, cursor, false); !errors.Is(err, ErrRetainedListTooLarge) {
			t.Fatalf("error = %v, want %v", err, ErrRetainedListTooLarge)
		}
		if accumulator.retainedBytes != beforeBytes || !reflect.DeepEqual(accumulator.seenCursors, map[string]struct{}{"kept": {}}) {
			t.Fatalf("cursor overflow mutated accumulator = bytes %d cursors %#v", accumulator.retainedBytes, accumulator.seenCursors)
		}
	})

	t.Run("cursor loop precedes page and byte limits", func(t *testing.T) {
		accumulator := listAccumulator{
			seenCursors:   map[string]struct{}{cursor: {}},
			retainedBytes: maxRetainedListBytes,
		}
		if err := accumulator.appendPage(nil, cursor, true); !errors.Is(err, ErrPaginationCursorLoop) {
			t.Fatalf("error = %v, want %v", err, ErrPaginationCursorLoop)
		}
		if accumulator.retainedBytes != maxRetainedListBytes || len(accumulator.seenCursors) != 1 {
			t.Fatalf("loop classification mutated accumulator = bytes %d cursors %#v", accumulator.retainedBytes, accumulator.seenCursors)
		}
	})

	t.Run("page limit precedes new cursor byte limit", func(t *testing.T) {
		accumulator := listAccumulator{retainedBytes: maxRetainedListBytes}
		if err := accumulator.appendPage(nil, cursor, true); !errors.Is(err, ErrTooManyToolPages) {
			t.Fatalf("error = %v, want %v", err, ErrTooManyToolPages)
		}
		if accumulator.retainedBytes != maxRetainedListBytes || len(accumulator.seenCursors) != 0 {
			t.Fatalf("page classification mutated accumulator = bytes %d cursors %#v", accumulator.retainedBytes, accumulator.seenCursors)
		}
	})

	t.Run("restart reset", func(t *testing.T) {
		accumulator := listAccumulator{
			tools:         []Tool{{Name: "discarded-before-restart"}},
			seenCursors:   map[string]struct{}{"discarded-cursor": {}},
			retainedBytes: maxRetainedListBytes,
		}
		accumulator.reset()
		if accumulator.retainedBytes != 0 || len(accumulator.tools) != 0 || len(accumulator.seenCursors) != 0 {
			t.Fatalf("reset accumulator = bytes %d tools %d cursors %d", accumulator.retainedBytes, len(accumulator.tools), len(accumulator.seenCursors))
		}
		if err := accumulator.appendPage(page, "", false); err != nil {
			t.Fatal(err)
		}
		if accumulator.retainedBytes != pageBytes || len(accumulator.tools) != 1 {
			t.Fatalf("post-reset accumulator = bytes %d tools %d", accumulator.retainedBytes, len(accumulator.tools))
		}
	})
}
