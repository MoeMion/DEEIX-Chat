package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/golang-jwt/jwt/v5"
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

func TestOperationSignedContextHeaderSnapshotAndPropagation(t *testing.T) {
	const signedHeader = "X-Operation-Signed-Context"

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
	cfg.HeadersEnabled = true
	cfg.SignedContextHeader = signedHeader
	cfg.SignedContext = &originalSigned
	transport := &scriptedTransport{t: t, steps: []transportStep{
		initializeStep("session-snapshot", protocolVersion),
		initializedStep("session-snapshot"),
		interruptedStep(listStep("session-snapshot", `{"tools":[]}`), "cursor-snapshot"),
		resumeStep("session-snapshot", "cursor-snapshot", `{"tools":[]}`),
		terminateStep("session-snapshot"),
	}}
	op, err := newOperation(transport, cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CustomHeaders["X-Tenant"] = "tenant-mutated"
	cfg.CustomHeaders["X-Injected"] = "mutated"
	cfg.Context.UserPublicID = "user-mutated"
	cfg.HeadersEnabled = false
	cfg.SignedContextHeader = "X-Mutated-Signed-Context"
	originalSigned.Secret = "secret-mutated"
	originalSigned.Audience = "audience-mutated"

	if _, err = op.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = op.terminate(context.Background()); err != nil {
		t.Fatal(err)
	}
	transport.assertDone()
	if !op.config.HeadersEnabled {
		t.Fatal("operation lost HeadersEnabled snapshot")
	}
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
		if req.SignedContextHeader != signedHeader {
			t.Fatalf("request %d signed Header = %q, want %q", index, req.SignedContextHeader, signedHeader)
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

func TestMCPRunLifecycleMatrix(t *testing.T) {
	runMCPRunLifecycleMatrix(t)
}

type lifecycleMatrixFixture struct {
	t             *testing.T
	scenario      string
	recorder      requestRecorder
	mu            sync.Mutex
	sessions      int
	listPosts     int
	callPosts     int
	lastCallID    any
	streamStarted chan struct{}
	streamExited  chan struct{}
}

func newLifecycleMatrixFixture(t *testing.T, scenario string) *lifecycleMatrixFixture {
	t.Helper()
	return &lifecycleMatrixFixture{
		t:             t,
		scenario:      scenario,
		streamStarted: make(chan struct{}),
		streamExited:  make(chan struct{}),
	}
}

func (f *lifecycleMatrixFixture) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	item, err := f.recorder.append(req)
	if err != nil {
		f.t.Errorf("capture request: %v", err)
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if req.Method == http.MethodGet {
		f.writeResume(w)
		return
	}

	method, _ := item.Body["method"].(string)
	id := item.Body["id"]
	switch method {
	case "initialize":
		f.mu.Lock()
		f.sessions++
		sessionID := fmt.Sprintf("matrix-session-%d", f.sessions)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("MCP-Session-Id", sessionID)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":%q,"capabilities":{}}}`, id, protocolVersion)
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		f.mu.Lock()
		f.listPosts++
		listPost := f.listPosts
		f.mu.Unlock()
		if f.scenario == "session_404" && listPost == 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.writeList(w, item, id, listPost)
	case "tools/call":
		f.mu.Lock()
		f.callPosts++
		f.lastCallID = id
		f.mu.Unlock()
		f.writeCall(w, req, id)
	default:
		f.t.Errorf("unexpected rpc method %q", method)
		http.Error(w, "unexpected method", http.StatusBadRequest)
	}
}

func (f *lifecycleMatrixFixture) writeList(w http.ResponseWriter, item capturedRequest, id any, listPost int) {
	w.Header().Set("Content-Type", "application/json")
	if f.scenario != "pagination" {
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"tools":[]}}`, id)
		return
	}
	cursor := ""
	if params, ok := item.Body["params"].(map[string]any); ok {
		cursor, _ = params["cursor"].(string)
	}
	wantCursor := []string{"", "cursor-1", "cursor-2"}[listPost-1]
	if cursor != wantCursor {
		f.t.Errorf("page %d cursor = %q, want %q", listPost, cursor, wantCursor)
	}
	next := ""
	if listPost < 3 {
		next = fmt.Sprintf("cursor-%d", listPost)
	}
	result := map[string]any{
		"tools": []map[string]any{{"name": fmt.Sprintf("tool-%d", listPost), "inputSchema": map[string]any{}}},
	}
	if next != "" {
		result["nextCursor"] = next
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (f *lifecycleMatrixFixture) writeCall(w http.ResponseWriter, req *http.Request, id any) {
	switch f.scenario {
	case "json":
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"content":[{"type":"text","text":"ok"}]}}`, id)
	case "sse_multiple_events":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "id: progress-1\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\nid: complete-1\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n", id)
	case "sse_resume":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: resume-1\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
	case "session_404":
		w.WriteHeader(http.StatusNotFound)
	case "cancel", "timeout":
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Test-Track-Body", "true")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(f.streamStarted)
		<-req.Context().Done()
		close(f.streamExited)
	default:
		f.t.Errorf("unexpected call scenario %q", f.scenario)
		http.Error(w, "unexpected call", http.StatusBadRequest)
	}
}

func (f *lifecycleMatrixFixture) writeResume(w http.ResponseWriter) {
	if f.scenario != "sse_resume" {
		f.t.Errorf("unexpected resume for %q", f.scenario)
		http.Error(w, "unexpected resume", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	id := f.lastCallID
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "id: resume-2\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"resumed\"}]}}\n\n", id)
}

func (f *lifecycleMatrixFixture) counts() (sessions int, lists int, calls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions, f.listPosts, f.callPosts
}

type lifecycleMatrixClock struct {
	mu     sync.Mutex
	base   time.Time
	nowSeq int
	idSeq  int
}

func (c *lifecycleMatrixClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.base.Add(time.Duration(c.nowSeq) * 61 * time.Second)
	c.nowSeq++
	return now
}

func (c *lifecycleMatrixClock) newID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idSeq++
	return fmt.Sprintf("ctx_matrix_%03d", c.idSeq)
}

type lifecycleObservedBody struct {
	io.ReadCloser
	readStarted chan struct{}
	readDone    chan struct{}
	closed      chan struct{}
	readOnce    sync.Once
	doneOnce    sync.Once
	closeOnce   sync.Once
}

func (b *lifecycleObservedBody) Read(buffer []byte) (int, error) {
	b.readOnce.Do(func() { close(b.readStarted) })
	count, err := b.ReadCloser.Read(buffer)
	if err != nil {
		b.doneOnce.Do(func() { close(b.readDone) })
	}
	return count, err
}

func (b *lifecycleObservedBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return b.ReadCloser.Close()
}

type lifecycleMatrixRoundTripper struct {
	next              http.RoundTripper
	bodyReadStarted   chan struct{}
	bodyReadDone      chan struct{}
	bodyClosed        chan struct{}
	deleteObservation chan lifecycleDeleteObservation
}

type lifecycleDeleteObservation struct {
	ContextError error
	HasDeadline  bool
	Remaining    time.Duration
}

type lifecycleCallOutcome struct {
	result string
	err    error
}

func awaitLifecycleCallOutcome(ctx context.Context, result <-chan lifecycleCallOutcome) (lifecycleCallOutcome, error) {
	select {
	case outcome := <-result:
		return outcome, nil
	case <-ctx.Done():
		return lifecycleCallOutcome{}, fmt.Errorf("await lifecycle call result: %w", ctx.Err())
	}
}

func drainLifecycleCallOutcomePreservingError(
	cleanupCtx context.Context,
	result <-chan lifecycleCallOutcome,
	initialErr error,
) (lifecycleCallOutcome, error) {
	outcome, drainErr := awaitLifecycleCallOutcome(cleanupCtx, result)
	return outcome, errors.Join(initialErr, drainErr)
}

func (r *lifecycleMatrixRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodDelete {
		observation := lifecycleDeleteObservation{ContextError: req.Context().Err()}
		if deadline, ok := req.Context().Deadline(); ok {
			observation.HasDeadline = true
			observation.Remaining = time.Until(deadline)
		}
		r.deleteObservation <- observation
	}
	response, err := r.next.RoundTrip(req)
	if err == nil && response != nil && response.Header.Get("X-Test-Track-Body") == "true" {
		response.Body = &lifecycleObservedBody{
			ReadCloser:  response.Body,
			readStarted: r.bodyReadStarted,
			readDone:    r.bodyReadDone,
			closed:      r.bodyClosed,
		}
	}
	return response, err
}

type lifecycleTriggeredContext struct {
	done chan struct{}
	mu   sync.Mutex
	err  error
	once sync.Once
}

func newLifecycleTriggeredContext() *lifecycleTriggeredContext {
	return &lifecycleTriggeredContext{done: make(chan struct{})}
}

func (c *lifecycleTriggeredContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *lifecycleTriggeredContext) Done() <-chan struct{}       { return c.done }
func (c *lifecycleTriggeredContext) Value(any) any               { return nil }

func (c *lifecycleTriggeredContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *lifecycleTriggeredContext) trigger(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

type lifecycleStableClaims struct {
	Subject                  string
	Issuer                   string
	Audience                 []string
	Mode                     string
	Name                     string
	Email                    string
	Role                     string
	ConversationPublicID     string
	AssistantMessagePublicID string
	UserMessagePublicID      string
	RequestID                string
	RunID                    string
	TraceID                  string
}

type lifecycleExpectedRequest struct {
	operation   OperationKind
	httpMethod  string
	rpcMethod   string
	requestID   string
	sessionID   string
	lastEventID string
}

func lifecycleExpectedSequence(scenario string) ([]lifecycleExpectedRequest, error) {
	initialize := func(id string) lifecycleExpectedRequest {
		return lifecycleExpectedRequest{operation: OperationInitialize, httpMethod: http.MethodPost, rpcMethod: "initialize", requestID: id}
	}
	initialized := func(sessionID string) lifecycleExpectedRequest {
		return lifecycleExpectedRequest{operation: OperationInitialized, httpMethod: http.MethodPost, rpcMethod: "notifications/initialized", sessionID: sessionID}
	}
	call := func(id string, sessionID string) lifecycleExpectedRequest {
		return lifecycleExpectedRequest{operation: OperationCallTool, httpMethod: http.MethodPost, rpcMethod: "tools/call", requestID: id, sessionID: sessionID}
	}
	list := func(id string, sessionID string) lifecycleExpectedRequest {
		return lifecycleExpectedRequest{operation: OperationListTools, httpMethod: http.MethodPost, rpcMethod: "tools/list", requestID: id, sessionID: sessionID}
	}
	terminate := func(sessionID string) lifecycleExpectedRequest {
		return lifecycleExpectedRequest{operation: OperationTerminate, httpMethod: http.MethodDelete, sessionID: sessionID}
	}

	sessionOne := "matrix-session-1"
	switch scenario {
	case "json", "sse_multiple_events", "cancel", "timeout":
		return []lifecycleExpectedRequest{
			initialize("1"), initialized(sessionOne), call("2", sessionOne), terminate(sessionOne),
		}, nil
	case "sse_resume":
		return []lifecycleExpectedRequest{
			initialize("1"),
			initialized(sessionOne),
			call("2", sessionOne),
			{operation: OperationResumeSSE, httpMethod: http.MethodGet, requestID: "2", sessionID: sessionOne, lastEventID: "resume-1"},
			terminate(sessionOne),
		}, nil
	case "pagination":
		return []lifecycleExpectedRequest{
			initialize("1"), initialized(sessionOne), list("2", sessionOne), list("3", sessionOne), list("4", sessionOne), terminate(sessionOne),
		}, nil
	case "session_404":
		return []lifecycleExpectedRequest{
			initialize("1"),
			initialized(sessionOne),
			list("2", sessionOne),
			initialize("3"),
			initialized("matrix-session-2"),
			list("4", "matrix-session-2"),
			call("5", "matrix-session-2"),
		}, nil
	default:
		return nil, fmt.Errorf("unknown lifecycle scenario")
	}
}

func validateLifecycleExpectedRequest(
	expected lifecycleExpectedRequest,
	logical TransportRequest,
	physical capturedRequest,
) error {
	if logical.Operation != expected.operation || logical.HTTPMethod != expected.httpMethod || rpcMethod(logical) != expected.rpcMethod {
		return fmt.Errorf("logical request does not match independent oracle")
	}
	if string(logical.RequestID) != expected.requestID || logical.Session.ID != expected.sessionID || logical.LastEventID != expected.lastEventID {
		return fmt.Errorf("logical request identity does not match independent oracle")
	}
	if expected.operation == OperationInitialize {
		if logical.Session.ProtocolVersion != "" {
			return fmt.Errorf("initialize logical protocol is not empty")
		}
	} else if logical.Session.ProtocolVersion != protocolVersion {
		return fmt.Errorf("logical protocol does not match independent oracle")
	}
	if physical.Method != expected.httpMethod {
		return fmt.Errorf("physical method does not match independent oracle")
	}
	physicalRPCMethod, _ := physical.Body["method"].(string)
	if physicalRPCMethod != expected.rpcMethod {
		return fmt.Errorf("physical rpc method does not match independent oracle")
	}
	physicalID, hasPhysicalID := physical.Body["id"]
	if expected.httpMethod == http.MethodPost && expected.requestID != "" {
		encodedID, err := json.Marshal(physicalID)
		if err != nil || !hasPhysicalID || string(encodedID) != expected.requestID {
			return fmt.Errorf("physical rpc id does not match independent oracle")
		}
	} else if hasPhysicalID {
		return fmt.Errorf("physical request unexpectedly carries rpc id")
	}
	if expected.operation == OperationInitialize {
		if physical.Header.Get("MCP-Protocol-Version") != "" || physical.Header.Get("MCP-Session-Id") != "" {
			return fmt.Errorf("initialize physical headers are not empty")
		}
	} else if physical.Header.Get("MCP-Protocol-Version") != protocolVersion || physical.Header.Get("MCP-Session-Id") != expected.sessionID {
		return fmt.Errorf("physical session headers do not match independent oracle")
	}
	if physical.Header.Get("Last-Event-ID") != expected.lastEventID {
		return fmt.Errorf("physical last-event-id does not match independent oracle")
	}
	return nil
}

func validateLifecycleCallResult(raw string, wantText string) error {
	var result toolCallResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return fmt.Errorf("decode lifecycle call result")
	}
	if result.textContent() != wantText {
		return fmt.Errorf("lifecycle call result text mismatch")
	}
	return nil
}

func runMCPRunLifecycleMatrix(t *testing.T) {
	t.Helper()
	for _, scenario := range []string{
		"json",
		"sse_multiple_events",
		"sse_resume",
		"pagination",
		"session_404",
		"cancel",
		"timeout",
	} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newLifecycleMatrixFixture(t, scenario)
			server := httptest.NewServer(fixture)
			defer server.Close()

			bodyClosed := make(chan struct{})
			bodyReadStarted := make(chan struct{})
			bodyReadDone := make(chan struct{})
			deleteObservation := make(chan lifecycleDeleteObservation, 2)
			httpClient := server.Client()
			httpClient.Transport = &lifecycleMatrixRoundTripper{
				next:              httpClient.Transport,
				bodyReadStarted:   bodyReadStarted,
				bodyReadDone:      bodyReadDone,
				bodyClosed:        bodyClosed,
				deleteObservation: deleteObservation,
			}
			clock := &lifecycleMatrixClock{base: time.Date(2026, time.July, 13, 8, 0, 0, 0, time.UTC)}
			signer := newJWTContextSigner(clock.now, clock.newID)
			boundary := &transportBoundaryRecorder{next: newHTTPTransport(httpClient, signer)}
			secret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
			originalHeaders := map[string]string{"X-Tenant": "tenant-user-public", "X-Run": "run-public"}
			originalContext := TemplateContext{
				Mode:                     ContextModeChat,
				UserPublicID:             "user-public",
				UserDisplayName:          "Matrix User",
				UserEmail:                "matrix@example.test",
				UserRole:                 "member",
				ConversationPublicID:     "conversation-public",
				AssistantMessagePublicID: "assistant-public",
				UserMessagePublicID:      "message-public",
				RequestID:                "request-public",
				RunID:                    "run-public",
				TraceID:                  "trace-public",
			}
			wantContext := originalContext
			signedConfig := SignedContextConfig{
				Secret:         secret,
				Issuer:         "https://chat.example.test",
				Audience:       "urn:deeix:mcp:matrix",
				KeyID:          "ctx_matrix",
				ExpiresSeconds: 60,
				IncludeName:    true,
				IncludeEmail:   true,
				IncludeRole:    true,
			}
			cfg := CallConfig{
				BaseURL:             server.URL,
				TimeoutMS:           1000,
				CustomHeaders:       originalHeaders,
				Context:             originalContext,
				SignedContextHeader: "X-Matrix-Signed-Context",
				SignedContext:       &signedConfig,
			}
			op, err := newOperation(boundary, cfg, 1)
			if err != nil {
				t.Fatal(err)
			}
			originalHeaders["X-Tenant"] = "mutated"
			originalContext.UserPublicID = "mutated"
			signedConfig.Audience = "mutated"

			var fixtureWaitErr error
			switch scenario {
			case "pagination":
				var tools []Tool
				tools, err = op.ListTools(t.Context())
				if err == nil && !reflect.DeepEqual([]string{tools[0].Name, tools[1].Name, tools[2].Name}, []string{"tool-1", "tool-2", "tool-3"}) {
					t.Fatalf("paginated tools = %#v", tools)
				}
			case "session_404":
				if _, err = op.ListTools(t.Context()); err != nil {
					t.Fatalf("list after one session rebuild: %v", err)
				}
				_, err = op.CallTool(t.Context(), CallInput{ToolName: "memory.get", ArgumentsJSON: `{}`})
				if !errors.Is(err, ErrSessionInvalid) {
					t.Fatalf("call error = %v, want ErrSessionInvalid", err)
				}
			case "cancel", "timeout":
				guardCtx, guardCancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer guardCancel()
				triggered := newLifecycleTriggeredContext()
				wantErr := context.Canceled
				if scenario == "timeout" {
					wantErr = context.DeadlineExceeded
				}
				callResult := make(chan lifecycleCallOutcome, 1)
				go func() {
					result, callErr := op.CallTool(triggered, CallInput{ToolName: "memory.get", ArgumentsJSON: `{}`})
					callResult <- lifecycleCallOutcome{result: result, err: callErr}
				}()
				startErr := awaitTestSignal(guardCtx, bodyReadStarted, "matrix client body read start")
				triggered.trigger(wantErr)
				readErr := awaitTestSignal(guardCtx, bodyReadDone, "matrix client body read completion")
				outcome, outcomeErr := awaitLifecycleCallOutcome(guardCtx, callResult)
				if outcomeErr != nil {
					cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
					outcome, outcomeErr = drainLifecycleCallOutcomePreservingError(cleanupCtx, callResult, outcomeErr)
					cleanupCancel()
				}
				bodyErr := awaitTestSignal(guardCtx, bodyClosed, "matrix response body close")
				serverErr := awaitTestSignal(guardCtx, fixture.streamExited, "matrix server reader exit")
				fixtureWaitErr = errors.Join(startErr, readErr, outcomeErr, bodyErr, serverErr)
				err = outcome.err
				if !errors.Is(err, wantErr) {
					fixtureWaitErr = errors.Join(fixtureWaitErr, fmt.Errorf("call error does not match terminal context"))
				}
			default:
				var result string
				result, err = op.CallTool(t.Context(), CallInput{ToolName: "memory.get", ArgumentsJSON: `{}`})
				if err == nil {
					wantText := "ok"
					if scenario == "sse_resume" {
						wantText = "resumed"
					}
					if resultErr := validateLifecycleCallResult(result, wantText); resultErr != nil {
						t.Fatal(resultErr)
					}
				}
			}
			if err != nil && scenario != "session_404" && scenario != "cancel" && scenario != "timeout" {
				t.Fatalf("operation failed: %v", err)
			}
			terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 5*time.Second)
			terminateErr := op.terminate(terminateCtx)
			terminateCancel()
			if terminateErr != nil {
				t.Fatalf("terminate: %v", terminateErr)
			}
			if fixtureWaitErr != nil {
				t.Fatal(fixtureWaitErr)
			}

			logical := boundary.snapshot()
			physical := fixture.recorder.snapshot()
			assertLifecycleMatrixRequestContract(t, scenario, logical, physical, secret, wantContext)
			sessions, listPosts, callPosts := fixture.counts()
			wantCallPosts := 1
			if scenario == "pagination" {
				wantCallPosts = 0
			}
			if callPosts != wantCallPosts {
				t.Fatalf("tools/call POSTs = %d, want %d", callPosts, wantCallPosts)
			}
			switch scenario {
			case "pagination":
				if sessions != 1 || listPosts != 3 {
					t.Fatalf("sessions=%d list POSTs=%d, want 1 and 3", sessions, listPosts)
				}
			case "session_404":
				if sessions != 2 || listPosts != 2 || callPosts != 1 {
					t.Fatalf("sessions=%d lists=%d calls=%d, want 2/2/1", sessions, listPosts, callPosts)
				}
			}
			if scenario == "cancel" || scenario == "timeout" {
				observationGuard, observationCancel := context.WithTimeout(t.Context(), 5*time.Second)
				var observation lifecycleDeleteObservation
				select {
				case observation = <-deleteObservation:
				case <-observationGuard.Done():
					observationCancel()
					t.Fatalf("await bounded DELETE observation: %v", observationGuard.Err())
				}
				observationCancel()
				if observation.ContextError != nil || !observation.HasDeadline || observation.Remaining <= 0 || observation.Remaining > cleanupTimeout {
					t.Fatalf("DELETE context = %#v, want active bounded cleanup", observation)
				}
			}
		})
	}
}

func assertLifecycleMatrixRequestContract(
	t *testing.T,
	scenario string,
	logical []TransportRequest,
	physical []capturedRequest,
	secret string,
	wantContext TemplateContext,
) {
	t.Helper()
	if len(logical) == 0 || len(logical) != len(physical) {
		t.Fatalf("logical requests=%d physical requests=%d", len(logical), len(physical))
	}
	expected, err := lifecycleExpectedSequence(scenario)
	if err != nil {
		t.Fatal(err)
	}
	if len(expected) != len(logical) {
		t.Fatalf("scenario %q requests=%d, independent oracle wants %d", scenario, len(logical), len(expected))
	}
	for index := range expected {
		if requestErr := validateLifecycleExpectedRequest(expected[index], logical[index], physical[index]); requestErr != nil {
			t.Fatalf("scenario %q request %d: %v", scenario, index, requestErr)
		}
	}
	var firstStable lifecycleStableClaims
	var priorClaims ContextJWTClaims
	var priorToken string
	var callRequestID json.RawMessage
	var nextRequestID int64
	resumeGETs := 0
	callPOSTs := 0
	listCursors := make([]string, 0, 3)
	for index := range logical {
		logicalRequest := logical[index]
		physicalRequest := physical[index]
		if physicalRequest.Method != logicalRequest.HTTPMethod {
			t.Fatalf("request %d method=%q, want %q", index, physicalRequest.Method, logicalRequest.HTTPMethod)
		}
		physicalRPCMethod, _ := physicalRequest.Body["method"].(string)
		if physicalRPCMethod != rpcMethod(logicalRequest) {
			t.Fatalf("request %d rpc method=%q, want %q", index, physicalRPCMethod, rpcMethod(logicalRequest))
		}
		physicalID, hasPhysicalID := physicalRequest.Body["id"]
		if len(logicalRequest.RequestID) == 0 {
			if hasPhysicalID {
				t.Fatalf("request %d had unexpected rpc id %#v", index, physicalID)
			}
		} else if logicalRequest.HTTPMethod == http.MethodPost {
			encodedID, marshalErr := json.Marshal(physicalID)
			if marshalErr != nil || !equalRPCID(encodedID, logicalRequest.RequestID) {
				t.Fatalf("request %d rpc id=%s, want %s", index, encodedID, logicalRequest.RequestID)
			}
		}
		if logicalRequest.Operation == OperationInitialize || logicalRequest.Operation == OperationListTools || logicalRequest.Operation == OperationCallTool {
			nextRequestID++
			var requestID int64
			if unmarshalErr := json.Unmarshal(logicalRequest.RequestID, &requestID); unmarshalErr != nil || requestID != nextRequestID {
				t.Fatalf("request %d rpc id=%s, want %d", index, logicalRequest.RequestID, nextRequestID)
			}
		}
		if logicalRequest.Operation == OperationInitialize {
			if got := physicalRequest.Header.Get("MCP-Protocol-Version"); got != "" {
				t.Fatalf("initialize %d protocol header=%q", index, got)
			}
			if got := physicalRequest.Header.Get("MCP-Session-Id"); got != "" {
				t.Fatalf("initialize %d session header=%q", index, got)
			}
		} else {
			if got := physicalRequest.Header.Get("MCP-Protocol-Version"); got != "2025-11-25" {
				t.Fatalf("request %d protocol=%q, want 2025-11-25", index, got)
			}
			if got := physicalRequest.Header.Get("MCP-Session-Id"); got != logicalRequest.Session.ID || got == "" {
				t.Fatalf("request %d physical session=%q logical=%q", index, got, logicalRequest.Session.ID)
			}
		}
		if got := physicalRequest.Header.Get("Last-Event-ID"); got != logicalRequest.LastEventID {
			t.Fatalf("request %d Last-Event-ID=%q, want %q", index, got, logicalRequest.LastEventID)
		}
		if got := physicalRequest.Header.Get("X-Tenant"); got != "tenant-user-public" {
			t.Fatalf("request %d X-Tenant=%q", index, got)
		}
		if got := physicalRequest.Header.Get("X-Run"); got != "run-public" {
			t.Fatalf("request %d X-Run=%q", index, got)
		}
		if !reflect.DeepEqual(logicalRequest.TemplateContext, wantContext) {
			t.Fatalf("request %d context mutated: %#v", index, logicalRequest.TemplateContext)
		}
		if logicalRequest.SignedContextHeader != "X-Matrix-Signed-Context" {
			t.Fatalf("request %d signed Header binding=%q", index, logicalRequest.SignedContextHeader)
		}
		token := physicalRequest.Header.Get("X-Matrix-Signed-Context")
		if values := physicalRequest.Header.Values("X-DEEIX-Context"); len(values) != 0 {
			t.Fatalf("request %d emitted legacy fixed signed Header: %#v", index, values)
		}
		claims, keyID := parseLifecycleMatrixClaims(t, token, secret)
		if keyID != "ctx_matrix" {
			t.Fatalf("request %d signed context kid=%q, want ctx_matrix", index, keyID)
		}
		stable := lifecycleStableClaims{
			Subject: claims.Subject, Issuer: claims.Issuer, Audience: append([]string(nil), claims.Audience...),
			Mode: claims.Mode, Name: claims.Name, Email: claims.Email, Role: claims.Role,
			ConversationPublicID: claims.ConversationPublicID, AssistantMessagePublicID: claims.AssistantMessagePublicID,
			UserMessagePublicID: claims.UserMessagePublicID, RequestID: claims.RequestID, RunID: claims.RunID, TraceID: claims.TraceID,
		}
		if index == 0 {
			firstStable = stable
			wantStable := lifecycleStableClaims{
				Subject: "user-public", Issuer: "https://chat.example.test", Audience: []string{"urn:deeix:mcp:matrix"},
				Mode: string(ContextModeChat), Name: "Matrix User", Email: "matrix@example.test", Role: "member",
				ConversationPublicID: "conversation-public", AssistantMessagePublicID: "assistant-public",
				UserMessagePublicID: "message-public", RequestID: "request-public", RunID: "run-public", TraceID: "trace-public",
			}
			if !reflect.DeepEqual(firstStable, wantStable) {
				t.Fatalf("stable JWT claims=%#v, want %#v", firstStable, wantStable)
			}
		} else {
			if token == priorToken {
				t.Fatalf("request %d reused the prior signed JWT", index)
			}
			if !reflect.DeepEqual(stable, firstStable) {
				t.Fatalf("request %d stable JWT claims changed: %#v vs %#v", index, stable, firstStable)
			}
			if claims.ID == priorClaims.ID || claims.IssuedAt.Equal(priorClaims.IssuedAt.Time) || claims.NotBefore.Equal(priorClaims.NotBefore.Time) || claims.ExpiresAt.Equal(priorClaims.ExpiresAt.Time) {
				t.Fatalf("request %d dynamic JWT claims were reused", index)
			}
			if claims.IssuedAt.Sub(priorClaims.IssuedAt.Time) <= 60*time.Second {
				t.Fatalf("request %d fake-clock advance=%v, want beyond TTL", index, claims.IssuedAt.Sub(priorClaims.IssuedAt.Time))
			}
		}
		if claims.ExpiresAt.Sub(claims.IssuedAt.Time) != 60*time.Second || !claims.NotBefore.Equal(claims.IssuedAt.Time) {
			t.Fatalf("request %d JWT times iat=%v nbf=%v exp=%v", index, claims.IssuedAt, claims.NotBefore, claims.ExpiresAt)
		}
		priorClaims = claims
		priorToken = token

		if logicalRequest.Operation == OperationCallTool {
			callPOSTs++
			callRequestID = append(json.RawMessage(nil), logicalRequest.RequestID...)
		}
		if logicalRequest.Operation == OperationResumeSSE {
			resumeGETs++
			if logicalRequest.HTTPMethod != http.MethodGet || logicalRequest.LastEventID != "resume-1" || !equalRPCID(logicalRequest.RequestID, callRequestID) {
				t.Fatalf("resume request = %#v, call id=%s", logicalRequest, callRequestID)
			}
		}
		if logicalRequest.Operation == OperationListTools {
			cursor := ""
			var envelope struct {
				Params map[string]string `json:"params"`
			}
			if unmarshalErr := json.Unmarshal(logicalRequest.Body, &envelope); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			cursor = envelope.Params["cursor"]
			listCursors = append(listCursors, cursor)
		}
	}
	if scenario == "sse_resume" && (resumeGETs != 1 || callPOSTs != 1) {
		t.Fatalf("resume GETs=%d tools/call POSTs=%d, want 1/1", resumeGETs, callPOSTs)
	}
	if scenario == "pagination" && !reflect.DeepEqual(listCursors, []string{"", "cursor-1", "cursor-2"}) {
		t.Fatalf("pagination cursors=%#v", listCursors)
	}
}

func parseLifecycleMatrixClaims(t *testing.T, signed string, secret string) (ContextJWTClaims, string) {
	t.Helper()
	claims := ContextJWTClaims{}
	parsed, err := jwt.ParseWithClaims(
		signed,
		&claims,
		func(token *jwt.Token) (any, error) { return []byte(secret), nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithoutClaimsValidation(),
	)
	if err != nil || parsed == nil || !parsed.Valid {
		t.Fatalf("parse signed context: token valid=%v error=%v", parsed != nil && parsed.Valid, err)
	}
	keyID, _ := parsed.Header["kid"].(string)
	return claims, keyID
}

func TestLifecycleMatrixIndependentOracleRejectsMutations(t *testing.T) {
	baseExpected := lifecycleExpectedRequest{
		operation:  OperationListTools,
		httpMethod: http.MethodPost,
		rpcMethod:  "tools/list",
		requestID:  "4",
		sessionID:  "matrix-session-2",
	}
	baseLogical := TransportRequest{
		Operation:  OperationListTools,
		HTTPMethod: http.MethodPost,
		RequestID:  json.RawMessage(`4`),
		Session:    sessionState{ID: "matrix-session-2", ProtocolVersion: protocolVersion},
		Body:       []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`),
	}
	basePhysical := capturedRequest{
		Method: http.MethodPost,
		Header: http.Header{
			"Mcp-Protocol-Version": []string{protocolVersion},
			"Mcp-Session-Id":       []string{"matrix-session-2"},
		},
		Body: map[string]any{"jsonrpc": "2.0", "id": float64(4), "method": "tools/list", "params": map[string]any{}},
	}
	if err := validateLifecycleExpectedRequest(baseExpected, baseLogical, basePhysical); err != nil {
		t.Fatalf("baseline oracle rejected valid request: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*TransportRequest, *capturedRequest)
	}{
		{
			name: "stale session after reinitialize",
			mutate: func(logical *TransportRequest, physical *capturedRequest) {
				logical.Session.ID = "matrix-session-1"
				physical.Header.Set("MCP-Session-Id", "matrix-session-1")
			},
		},
		{
			name: "permuted operation",
			mutate: func(logical *TransportRequest, physical *capturedRequest) {
				logical.Operation = OperationCallTool
				logical.Body = []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{}}`)
				physical.Body["method"] = "tools/call"
			},
		},
		{
			name: "non resume cursor",
			mutate: func(logical *TransportRequest, physical *capturedRequest) {
				logical.LastEventID = "resume-1"
				physical.Header.Set("Last-Event-ID", "resume-1")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			logical := baseLogical
			logical.Body = append([]byte(nil), baseLogical.Body...)
			physical := basePhysical
			physical.Header = basePhysical.Header.Clone()
			physical.Body = maps.Clone(basePhysical.Body)
			test.mutate(&logical, &physical)
			if err := validateLifecycleExpectedRequest(baseExpected, logical, physical); err == nil {
				t.Fatal("independent oracle accepted mutated request")
			}
		})
	}
}

func TestLifecycleMatrixSemanticResultRejectsWrongPayload(t *testing.T) {
	if err := validateLifecycleCallResult(`{"content":[{"type":"text","text":"ok"}]}`, "ok"); err != nil {
		t.Fatalf("semantic result rejected valid payload: %v", err)
	}
	if err := validateLifecycleCallResult(`{"content":[{"type":"text","text":"wrong"}]}`, "ok"); err == nil {
		t.Fatal("semantic result accepted wrong SSE payload")
	}
}

func TestLifecycleMatrixCancellationFixtureFlushesNoBodyBeforeTrigger(t *testing.T) {
	fixture := newLifecycleMatrixFixture(t, "cancel")
	recorder := httptest.NewRecorder()
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	request := httptest.NewRequest(http.MethodPost, "https://matrix.example.test/mcp", nil).WithContext(requestCtx)
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		fixture.writeCall(recorder, request, float64(2))
	}()

	guardCtx, guardCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer guardCancel()
	startErr := awaitTestSignal(guardCtx, fixture.streamStarted, "cancellation fixture header flush")
	bodyBytesBeforeTrigger := recorder.Body.Len()
	cancelRequest()
	doneErr := awaitTestSignal(guardCtx, handlerDone, "cancellation fixture handler exit")
	if waitErr := errors.Join(startErr, doneErr); waitErr != nil {
		t.Fatal(waitErr)
	}
	if bodyBytesBeforeTrigger != 0 {
		t.Fatalf("cancellation fixture emitted %d body bytes before trigger, want 0", bodyBytesBeforeTrigger)
	}
}

func TestLifecycleOutcomeCleanupDrainPreservesInitialGuardError(t *testing.T) {
	result := make(chan lifecycleCallOutcome, 1)
	result <- lifecycleCallOutcome{result: "drained", err: context.Canceled}
	outcome, err := drainLifecycleCallOutcomePreservingError(
		t.Context(),
		result,
		context.DeadlineExceeded,
	)
	if outcome.result != "drained" || !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("drained outcome = %#v", outcome)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup drain suppressed initial guard error: %v", err)
	}
}
