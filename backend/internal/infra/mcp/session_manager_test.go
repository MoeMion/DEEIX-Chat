package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type managerTransport struct {
	sequence           atomic.Int64
	terminations       atomic.Int32
	cleanupSawCanceled atomic.Bool
	cleanupStatus      int
	cleanupErr         error
	cleanupErrors      map[string]error
	cleanupEntered     chan string
	cleanupRelease     chan struct{}
	entered            chan string
	release            chan struct{}
	blockCalls         bool

	mu       sync.Mutex
	requests []TransportRequest
}

func (t *managerTransport) Do(ctx context.Context, req TransportRequest) (TransportResponse, error) {
	t.mu.Lock()
	t.requests = append(t.requests, cloneManagerTransportRequest(req))
	t.mu.Unlock()

	switch req.Operation {
	case OperationInitialize:
		sessionID := fmt.Sprintf("session-%d", t.sequence.Add(1))
		result, _ := json.Marshal(map[string]interface{}{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]interface{}{},
			"serverInfo":      map[string]string{"name": "test", "version": "1"},
		})
		return TransportResponse{
			SessionID: sessionID,
			Message:   rpcMessage{JSONRPC: "2.0", ID: req.RequestID, Result: result},
		}, nil
	case OperationInitialized:
		return TransportResponse{}, nil
	case OperationListTools:
		return TransportResponse{
			Message: rpcMessage{JSONRPC: "2.0", ID: req.RequestID, Result: json.RawMessage(`{"tools":[]}`)},
		}, nil
	case OperationCallTool:
		if t.blockCalls {
			select {
			case t.entered <- req.Session.ID:
			case <-ctx.Done():
				return TransportResponse{}, ctx.Err()
			}
			select {
			case <-t.release:
			case <-ctx.Done():
				return TransportResponse{}, ctx.Err()
			}
		}
		return TransportResponse{
			Message: rpcMessage{
				JSONRPC: "2.0",
				ID:      req.RequestID,
				Result:  json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`),
			},
		}, nil
	case OperationTerminate:
		t.terminations.Add(1)
		if ctx.Err() != nil {
			t.cleanupSawCanceled.Store(true)
		}
		if t.cleanupEntered != nil {
			select {
			case t.cleanupEntered <- req.Session.ID:
			case <-ctx.Done():
				return TransportResponse{}, ctx.Err()
			}
			select {
			case <-t.cleanupRelease:
			case <-ctx.Done():
				return TransportResponse{}, ctx.Err()
			}
		}
		if t.cleanupErr != nil {
			return TransportResponse{}, t.cleanupErr
		}
		if err := t.cleanupErrors[req.Session.ID]; err != nil {
			return TransportResponse{}, err
		}
		if t.cleanupStatus != 0 {
			return TransportResponse{}, newRequestError(
				OperationTerminate,
				DeliverySent,
				t.cleanupStatus,
				ClientErrorHTTP,
				errors.New("cleanup status"),
			)
		}
		return TransportResponse{}, nil
	default:
		return TransportResponse{}, fmt.Errorf("unexpected operation %d", req.Operation)
	}
}

func cloneManagerTransportRequest(req TransportRequest) TransportRequest {
	clone := req
	clone.Body = append([]byte(nil), req.Body...)
	clone.RequestID = append(json.RawMessage(nil), req.RequestID...)
	clone.CustomHeaders = cloneCustomHeaders(req.CustomHeaders)
	if req.SignedContext != nil {
		signed := *req.SignedContext
		clone.SignedContext = &signed
	}
	return clone
}

func (t *managerTransport) requestSnapshot() []TransportRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	requests := make([]TransportRequest, len(t.requests))
	for index, req := range t.requests {
		requests[index] = cloneManagerTransportRequest(req)
	}
	return requests
}

func managerAcquireInput(serverID uint, userPublicID string, runID string) AcquireInput {
	return AcquireInput{
		ServerID:        serverID,
		ServerUpdatedAt: time.Unix(1700000000, 0).UTC(),
		RetryCount:      1,
		CallConfig: CallConfig{
			BaseURL:       fmt.Sprintf("https://mcp-%d.example.test/rpc", serverID),
			AuthToken:     "token-1",
			TimeoutMS:     1000,
			CustomHeaders: map[string]string{"X-Tenant": "tenant-1"},
			Context: TemplateContext{
				Mode:            ContextModeChat,
				UserPublicID:    userPublicID,
				UserDisplayName: "User One",
				RunID:           runID,
			},
		},
	}
}

func cloneAcquireInput(input AcquireInput) AcquireInput {
	clone := input
	clone.CallConfig.CustomHeaders = cloneCustomHeaders(input.CallConfig.CustomHeaders)
	if input.CallConfig.SignedContext != nil {
		signed := *input.CallConfig.SignedContext
		clone.CallConfig.SignedContext = &signed
	}
	return clone
}

func TestSessionManagerBuildsStrictOpaqueKeys(t *testing.T) {
	valid := managerAcquireInput(1, "user-1", "run-1")
	const wantAuthIdentity = "deb0ce9b468a418c9984e809dba30636a314940bd58739d1891d855da93d7e48"
	if got := AuthenticationIdentity(valid.CallConfig.AuthToken); got != wantAuthIdentity {
		t.Fatal("AuthenticationIdentity changed from the frozen digest contract")
	}
	key, err := BuildSessionKey(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(key.AuthIdentity) != 64 || len(key.ConfigVersion) != 64 {
		t.Fatalf("digest lengths = auth:%d config:%d, want 64 each", len(key.AuthIdentity), len(key.ConfigVersion))
	}
	for _, secret := range []string{valid.CallConfig.AuthToken, valid.CallConfig.BaseURL} {
		if key.AuthIdentity == secret || key.ConfigVersion == secret {
			t.Fatal("session key exposed raw sensitive configuration")
		}
	}

	baseConfig := ConfigurationVersionInput{
		ServerUpdatedAt: valid.ServerUpdatedAt,
		Endpoint:        valid.CallConfig.BaseURL,
		TimeoutMS:       valid.CallConfig.TimeoutMS,
		RetryCount:      valid.RetryCount,
		SignedContext: &SignedContextConfig{
			Secret:   "secret",
			Issuer:   "ab",
			Audience: "c",
		},
	}
	collisionCandidate := baseConfig
	collisionCandidate.SignedContext = &SignedContextConfig{
		Secret:   "secret",
		Issuer:   "a",
		Audience: "bc",
	}
	if ConfigurationVersion(baseConfig) == ConfigurationVersion(collisionCandidate) {
		t.Fatal("configuration digest did not length-prefix adjacent fields")
	}

	for _, tt := range []struct {
		name   string
		mutate func(*AcquireInput)
	}{
		{name: "missing server", mutate: func(in *AcquireInput) { in.ServerID = 0 }},
		{name: "missing revision", mutate: func(in *AcquireInput) { in.ServerUpdatedAt = time.Time{} }},
		{name: "probe mode", mutate: func(in *AcquireInput) { in.CallConfig.Context.Mode = ContextModeProbe }},
		{name: "missing user", mutate: func(in *AcquireInput) { in.CallConfig.Context.UserPublicID = " \t" }},
		{name: "missing run", mutate: func(in *AcquireInput) { in.CallConfig.Context.RunID = " \t" }},
		{name: "missing endpoint", mutate: func(in *AcquireInput) { in.CallConfig.BaseURL = "" }},
		{name: "unsupported endpoint scheme", mutate: func(in *AcquireInput) { in.CallConfig.BaseURL = "ftp://mcp.example.test/rpc" }},
		{name: "endpoint without hostname", mutate: func(in *AcquireInput) { in.CallConfig.BaseURL = "http://:8080/rpc" }},
		{name: "endpoint user info", mutate: func(in *AcquireInput) { in.CallConfig.BaseURL = "https://user@mcp.example.test/rpc" }},
		{name: "endpoint query", mutate: func(in *AcquireInput) { in.CallConfig.BaseURL = "https://mcp.example.test/rpc?secret=value" }},
		{name: "endpoint fragment", mutate: func(in *AcquireInput) { in.CallConfig.BaseURL = "https://mcp.example.test/rpc#fragment" }},
		{name: "nonpositive timeout", mutate: func(in *AcquireInput) { in.CallConfig.TimeoutMS = 0 }},
		{name: "negative retry", mutate: func(in *AcquireInput) { in.RetryCount = -1 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := cloneAcquireInput(valid)
			tt.mutate(&input)
			if _, buildErr := BuildSessionKey(input); !errors.Is(buildErr, ErrInvalidSessionKey) {
				t.Fatalf("BuildSessionKey error = %v, want ErrInvalidSessionKey", buildErr)
			}
		})
	}
}

func TestSessionManagerOpenEphemeralRejectsEndpointWithoutHostname(t *testing.T) {
	cfg := testCallConfig()
	cfg.BaseURL = "http://:8080/rpc"
	manager := newRunSessionManager(&managerTransport{})
	if _, _, err := manager.OpenEphemeral(context.Background(), cfg, 0); err == nil {
		t.Fatal("OpenEphemeral accepted an endpoint without a hostname")
	}
}

func TestSessionManagerUsesClientTransportIdentityAndHandlesNilDependencies(t *testing.T) {
	transport := &managerTransport{}
	client := &Client{transport: transport}
	manager, ok := NewSessionManager(client).(*runSessionManager)
	if !ok {
		t.Fatalf("NewSessionManager type = %T", NewSessionManager(client))
	}
	if manager.transport != transport {
		t.Fatal("NewSessionManager did not reuse the Client transport identity")
	}
	if _, err := NewSessionManager(nil).Acquire(context.Background(), managerAcquireInput(1, "user-1", "run-1")); err == nil {
		t.Fatal("nil Client unexpectedly produced a usable operation")
	}
	if _, err := newRunSessionManager(nil).Acquire(context.Background(), managerAcquireInput(1, "user-1", "run-1")); err == nil {
		t.Fatal("nil transport unexpectedly produced a usable operation")
	}
}

func TestSessionManagerKeyReuseSeparationAndMismatch(t *testing.T) {
	manager := newRunSessionManager(&managerTransport{})
	base := managerAcquireInput(1, "user-1", "run-1")
	first, err := manager.Acquire(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Acquire(context.Background(), cloneAcquireInput(base))
	if err != nil || first != second {
		t.Fatalf("exact key was not reused: first=%p second=%p err=%v", first, second, err)
	}

	for _, tt := range []struct {
		name   string
		mutate func(*AcquireInput)
	}{
		{name: "server", mutate: func(in *AcquireInput) { in.ServerID++ }},
		{name: "user", mutate: func(in *AcquireInput) { in.CallConfig.Context.UserPublicID = "user-2" }},
		{name: "run", mutate: func(in *AcquireInput) { in.CallConfig.Context.RunID = "run-2" }},
		{name: "auth", mutate: func(in *AcquireInput) { in.CallConfig.AuthToken = "token-2" }},
		{name: "revision", mutate: func(in *AcquireInput) { in.ServerUpdatedAt = in.ServerUpdatedAt.Add(time.Second) }},
		{name: "endpoint", mutate: func(in *AcquireInput) { in.CallConfig.BaseURL = "https://other.example.test/rpc" }},
		{name: "timeout", mutate: func(in *AcquireInput) { in.CallConfig.TimeoutMS++ }},
		{name: "retry", mutate: func(in *AcquireInput) { in.RetryCount++ }},
		{name: "signed config", mutate: func(in *AcquireInput) {
			in.CallConfig.SignedContext = &SignedContextConfig{
				Secret:         "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY",
				Issuer:         "https://deeix.example.test",
				Audience:       "urn:deeix:mcp:test",
				KeyID:          "ctx_test",
				ExpiresSeconds: 300,
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := cloneAcquireInput(base)
			tt.mutate(&input)
			got, acquireErr := manager.Acquire(context.Background(), input)
			if acquireErr != nil {
				t.Fatal(acquireErr)
			}
			if got == first {
				t.Fatal("distinct key reused the base operation")
			}
		})
	}

	contextMutation := cloneAcquireInput(base)
	contextMutation.CallConfig.Context.UserDisplayName = "changed"
	if _, err = manager.Acquire(context.Background(), contextMutation); !errors.Is(err, ErrSessionContextMismatch) {
		t.Fatalf("context mutation error = %v", err)
	}
	headerMutation := cloneAcquireInput(base)
	headerMutation.CallConfig.CustomHeaders["X-Tenant"] = "changed"
	if _, err = manager.Acquire(context.Background(), headerMutation); !errors.Is(err, ErrSessionContextMismatch) {
		t.Fatalf("Header mutation error = %v", err)
	}
}

func TestSessionManagerDefensivelySnapshotsConfiguration(t *testing.T) {
	transport := &managerTransport{}
	manager := newRunSessionManager(transport)
	input := managerAcquireInput(1, "user-1", "run-1")
	original := cloneAcquireInput(input)
	input.CallConfig.SignedContext = &SignedContextConfig{
		Secret:         "secret-1",
		Issuer:         "issuer-1",
		Audience:       "audience-1",
		KeyID:          "key-1",
		ExpiresSeconds: 300,
		IncludeName:    true,
	}
	original = cloneAcquireInput(input)
	op, err := manager.Acquire(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}

	input.CallConfig.CustomHeaders["X-Tenant"] = "mutated"
	input.CallConfig.SignedContext.Secret = "mutated"
	input.CallConfig.Context.UserDisplayName = "mutated"
	if _, err = op.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}

	requests := transport.requestSnapshot()
	if len(requests) != 3 {
		t.Fatalf("request count = %d, want 3", len(requests))
	}
	for _, req := range requests {
		if !reflect.DeepEqual(req.CustomHeaders, original.CallConfig.CustomHeaders) ||
			!reflect.DeepEqual(req.TemplateContext, original.CallConfig.Context) ||
			!reflect.DeepEqual(req.SignedContext, original.CallConfig.SignedContext) {
			t.Fatal("transport observed mutated immutable session configuration")
		}
	}
	if reused, acquireErr := manager.Acquire(context.Background(), original); acquireErr != nil || reused != op {
		t.Fatalf("original snapshot was not reusable: reused=%p op=%p err=%v", reused, op, acquireErr)
	}
}

func TestSessionManagerClosedOperationsCannotReopenSessions(t *testing.T) {
	t.Run("acquired operation after CloseRun", func(t *testing.T) {
		transport := &managerTransport{}
		manager := newRunSessionManager(transport)
		op, err := manager.Acquire(context.Background(), managerAcquireInput(1, "user-1", "run-1"))
		if err != nil {
			t.Fatal(err)
		}
		assertManagerOperationCannotReopen(t, transport, op, func() error {
			return manager.CloseRun(context.Background(), "user-1", "run-1")
		})
	})

	t.Run("acquired operation after CloseAll", func(t *testing.T) {
		transport := &managerTransport{}
		manager := newRunSessionManager(transport)
		op, err := manager.Acquire(context.Background(), managerAcquireInput(1, "user-1", "run-1"))
		if err != nil {
			t.Fatal(err)
		}
		assertManagerOperationCannotReopen(t, transport, op, func() error {
			return manager.CloseAll(context.Background())
		})
	})

	t.Run("ephemeral operation after callback", func(t *testing.T) {
		transport := &managerTransport{}
		manager := newRunSessionManager(transport)
		op, closeOperation, err := manager.OpenEphemeral(context.Background(), testCallConfig(), 0)
		if err != nil {
			t.Fatal(err)
		}
		assertManagerOperationCannotReopen(t, transport, op, func() error {
			return closeOperation(context.Background())
		})
	})

	t.Run("ephemeral operation after CloseAll", func(t *testing.T) {
		transport := &managerTransport{}
		manager := newRunSessionManager(transport)
		op, _, err := manager.OpenEphemeral(context.Background(), testCallConfig(), 0)
		if err != nil {
			t.Fatal(err)
		}
		assertManagerOperationCannotReopen(t, transport, op, func() error {
			return manager.CloseAll(context.Background())
		})
	})
}

func assertManagerOperationCannotReopen(
	t *testing.T,
	transport *managerTransport,
	op Operation,
	closeOperation func() error,
) {
	t.Helper()
	if _, err := op.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := closeOperation(); err != nil {
		t.Fatal(err)
	}
	requestsBefore := len(transport.requestSnapshot())
	if _, err := op.ListTools(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("ListTools after close error = %v, want ErrSessionClosed", err)
	}
	if _, err := op.CallTool(
		context.Background(),
		CallInput{ToolName: "echo", ArgumentsJSON: `{}`},
	); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("CallTool after close error = %v, want ErrSessionClosed", err)
	}
	if requestsAfter := len(transport.requestSnapshot()); requestsAfter != requestsBefore {
		t.Fatalf("requests after close = %d, want unchanged %d", requestsAfter, requestsBefore)
	}
	if transport.sequence.Load() != 1 || transport.terminations.Load() != 1 {
		t.Fatalf(
			"initializations=%d terminations=%d, want exactly one each",
			transport.sequence.Load(),
			transport.terminations.Load(),
		)
	}
}

func TestSessionManagerSerializesSameSessionAndAllowsDifferentSessions(t *testing.T) {
	t.Run("canceled waiter cannot enter same session", func(t *testing.T) {
		transport := &managerTransport{
			blockCalls: true,
			entered:    make(chan string, 2),
			release:    make(chan struct{}),
		}
		manager := newRunSessionManager(transport)
		op, err := manager.Acquire(context.Background(), managerAcquireInput(1, "user-1", "run-1"))
		if err != nil {
			t.Fatal(err)
		}
		firstDone := make(chan error, 1)
		go func() {
			_, callErr := op.CallTool(context.Background(), CallInput{ToolName: "echo", ArgumentsJSON: `{}`})
			firstDone <- callErr
		}()
		<-transport.entered
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err = op.CallTool(ctx, CallInput{ToolName: "echo", ArgumentsJSON: `{}`}); !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting call error = %v", err)
		}
		if len(transport.entered) != 0 {
			t.Fatal("canceled waiter entered transport")
		}
		close(transport.release)
		if err = <-firstDone; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("different sessions cross barrier", func(t *testing.T) {
		transport := &managerTransport{
			blockCalls: true,
			entered:    make(chan string, 2),
			release:    make(chan struct{}),
		}
		manager := newRunSessionManager(transport)
		first, err := manager.Acquire(context.Background(), managerAcquireInput(1, "user-1", "run-1"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := manager.Acquire(context.Background(), managerAcquireInput(2, "user-1", "run-1"))
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 2)
		for _, op := range []Operation{first, second} {
			go func(op Operation) {
				_, callErr := op.CallTool(context.Background(), CallInput{ToolName: "echo", ArgumentsJSON: `{}`})
				done <- callErr
			}(op)
		}
		seen := map[string]struct{}{}
		for len(seen) < 2 {
			select {
			case sessionID := <-transport.entered:
				seen[sessionID] = struct{}{}
			case <-time.After(time.Second):
				t.Fatal("different sessions did not reach the barrier")
			}
		}
		close(transport.release)
		for range 2 {
			if callErr := <-done; callErr != nil {
				t.Fatal(callErr)
			}
		}
	})
}

func TestSessionManagerCleanupOutlivesTimedOutWaiterAndDeletesOnce(t *testing.T) {
	transport := &managerTransport{
		blockCalls: true,
		entered:    make(chan string, 1),
		release:    make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseCall := func() {
		releaseOnce.Do(func() { close(transport.release) })
	}
	t.Cleanup(releaseCall)

	manager := newRunSessionManager(transport)
	op, closeOperation, err := manager.OpenEphemeral(context.Background(), testCallConfig(), 0)
	if err != nil {
		t.Fatal(err)
	}
	callDone := make(chan error, 1)
	go func() {
		_, callErr := op.CallTool(context.Background(), CallInput{ToolName: "echo", ArgumentsJSON: `{}`})
		callDone <- callErr
	}()
	<-transport.entered

	originalWaitTimeout := manager.cleanupWaitTimeout
	manager.cleanupWaitTimeout = 0
	firstCloseErr := closeOperation(context.Background())
	manager.cleanupWaitTimeout = originalWaitTimeout
	if !errors.Is(firstCloseErr, context.DeadlineExceeded) {
		t.Fatalf("first close error = %v, want deadline exceeded", firstCloseErr)
	}
	if transport.terminations.Load() != 0 {
		t.Fatalf("terminations before active call drains = %d, want 0", transport.terminations.Load())
	}
	requestsBeforeRejectedUse := len(transport.requestSnapshot())
	if _, err = op.ListTools(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("operation accepted new use after cleanup began: %v", err)
	}
	if requestsAfterRejectedUse := len(transport.requestSnapshot()); requestsAfterRejectedUse != requestsBeforeRejectedUse {
		t.Fatalf("rejected use dispatched requests: before=%d after=%d", requestsBeforeRejectedUse, requestsAfterRejectedUse)
	}

	releaseCall()
	if err = <-callDone; err != nil {
		t.Fatal(err)
	}
	if err = closeOperation(context.Background()); err != nil {
		t.Fatalf("later close did not observe eventual cleanup result: %v", err)
	}
	if err = closeOperation(context.Background()); err != nil {
		t.Fatalf("idempotent close error = %v", err)
	}
	if transport.terminations.Load() != 1 {
		t.Fatalf("terminations = %d, want exactly 1", transport.terminations.Load())
	}
}

func TestSessionManagerCleanupAndTerminalState(t *testing.T) {
	for _, tt := range []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{name: "success"},
		{name: "404 is closed", statusCode: http.StatusNotFound},
		{name: "405 is closed", statusCode: http.StatusMethodNotAllowed},
		{name: "failure still removes", statusCode: http.StatusBadGateway, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := &managerTransport{cleanupStatus: tt.statusCode}
			manager := newRunSessionManager(transport)
			for serverID := uint(1); serverID <= 2; serverID++ {
				op, err := manager.Acquire(context.Background(), managerAcquireInput(serverID, "user-1", "run-1"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = op.ListTools(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := manager.CloseRun(ctx, "user-1", "run-1")
			if (err != nil) != tt.wantErr {
				t.Fatalf("CloseRun error = %v, wantErr=%v", err, tt.wantErr)
			}
			if len(manager.entries) != 0 || transport.terminations.Load() != 2 || transport.cleanupSawCanceled.Load() {
				t.Fatalf(
					"entries=%d terminations=%d canceled=%v",
					len(manager.entries),
					transport.terminations.Load(),
					transport.cleanupSawCanceled.Load(),
				)
			}
		})
	}

	transport := &managerTransport{}
	manager := newRunSessionManager(transport)
	runOp, err := manager.Acquire(context.Background(), managerAcquireInput(1, "user-1", "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runOp.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	ephemeral, closeEphemeral, err := manager.OpenEphemeral(context.Background(), testCallConfig(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ephemeral.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = manager.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = closeEphemeral(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = closeEphemeral(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(manager.entries) != 0 || len(manager.ephemeral) != 0 || transport.terminations.Load() != 2 {
		t.Fatalf(
			"entries=%d ephemeral=%d terminations=%d",
			len(manager.entries),
			len(manager.ephemeral),
			transport.terminations.Load(),
		)
	}
	if _, err = manager.Acquire(context.Background(), managerAcquireInput(2, "user-1", "run-2")); !errors.Is(err, ErrSessionManagerClosed) {
		t.Fatalf("Acquire after CloseAll = %v", err)
	}
	if _, _, err = manager.OpenEphemeral(context.Background(), testCallConfig(), 0); !errors.Is(err, ErrSessionManagerClosed) {
		t.Fatalf("OpenEphemeral after CloseAll = %v", err)
	}
}

func TestSessionManagerSelectedButNeverOpenedDoesNotDelete(t *testing.T) {
	transport := &managerTransport{}
	manager := newRunSessionManager(transport)
	if _, err := manager.Acquire(context.Background(), managerAcquireInput(1, "user-1", "run-1")); err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseRun(context.Background(), "user-1", "run-1"); err != nil {
		t.Fatal(err)
	}
	if transport.sequence.Load() != 0 || transport.terminations.Load() != 0 {
		t.Fatalf("initializations=%d terminations=%d, want zero", transport.sequence.Load(), transport.terminations.Load())
	}
}

func TestSessionManagerEphemeralCloseIsRegisteredDetachedAndIdempotent(t *testing.T) {
	transport := &managerTransport{}
	manager := newRunSessionManager(transport)
	op, closeOperation, err := manager.OpenEphemeral(context.Background(), testCallConfig(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(manager.ephemeral) != 1 {
		t.Fatalf("ephemeral entries = %d, want 1", len(manager.ephemeral))
	}
	if _, err = op.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = closeOperation(canceled); err != nil {
		t.Fatal(err)
	}
	if err = closeOperation(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(manager.ephemeral) != 0 || transport.terminations.Load() != 1 || transport.cleanupSawCanceled.Load() {
		t.Fatalf(
			"ephemeral=%d terminations=%d canceled=%v",
			len(manager.ephemeral),
			transport.terminations.Load(),
			transport.cleanupSawCanceled.Load(),
		)
	}
	if err = manager.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if transport.terminations.Load() != 1 {
		t.Fatalf("terminations after CloseAll = %d, want 1", transport.terminations.Load())
	}
}

func TestSessionManagerCleanupRunsSessionsConcurrently(t *testing.T) {
	const sessionCount = 16
	transport := &managerTransport{
		cleanupEntered: make(chan string, sessionCount),
		cleanupRelease: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseCleanup := func() {
		releaseOnce.Do(func() { close(transport.cleanupRelease) })
	}
	t.Cleanup(releaseCleanup)
	manager := newRunSessionManager(transport)
	for serverID := uint(1); serverID <= sessionCount; serverID++ {
		op, err := manager.Acquire(context.Background(), managerAcquireInput(serverID, "user-1", "run-1"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = op.ListTools(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		done <- manager.CloseRun(context.Background(), "user-1", "run-1")
	}()
	seen := make(map[string]struct{}, sessionCount)
	for len(seen) < sessionCount {
		select {
		case sessionID := <-transport.cleanupEntered:
			seen[sessionID] = struct{}{}
		case <-time.After(time.Second):
			t.Fatal("cleanup did not run sessions concurrently")
		}
	}
	releaseCleanup()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSessionManagerCloseRunClosesEveryMatchingRevision(t *testing.T) {
	transport := &managerTransport{}
	manager := newRunSessionManager(transport)
	for revision := range 3 {
		input := managerAcquireInput(1, "user-1", "run-1")
		input.ServerUpdatedAt = input.ServerUpdatedAt.Add(time.Duration(revision) * time.Second)
		op, err := manager.Acquire(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = op.ListTools(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	other, err := manager.Acquire(context.Background(), managerAcquireInput(2, "user-1", "run-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = manager.CloseRun(context.Background(), "user-1", "run-1"); err != nil {
		t.Fatal(err)
	}
	if len(manager.entries) != 1 || transport.terminations.Load() != 3 {
		t.Fatalf("entries=%d terminations=%d, want 1 and 3", len(manager.entries), transport.terminations.Load())
	}
	if err = manager.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionManagerCleanupJoinsIndependentFailures(t *testing.T) {
	firstErr := errors.New("first cleanup failed")
	secondErr := errors.New("second cleanup failed")
	transport := &managerTransport{cleanupErrors: map[string]error{
		"session-1": firstErr,
		"session-2": secondErr,
	}}
	manager := newRunSessionManager(transport)
	for serverID := uint(1); serverID <= 2; serverID++ {
		op, err := manager.Acquire(context.Background(), managerAcquireInput(serverID, "user-1", "run-1"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = op.ListTools(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	err := manager.CloseRun(context.Background(), "user-1", "run-1")
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("CloseRun error = %v, want both cleanup failures", err)
	}
	if len(manager.entries) != 0 || transport.terminations.Load() != 2 {
		t.Fatalf("entries=%d terminations=%d, want 0 and 2", len(manager.entries), transport.terminations.Load())
	}
}

func TestSessionManagerCloseAllRacesRegistration(t *testing.T) {
	manager := newRunSessionManager(&managerTransport{})
	start := make(chan struct{})
	var wg sync.WaitGroup
	var unexpected atomic.Int32
	for i := range 64 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				_, err := manager.Acquire(
					context.Background(),
					managerAcquireInput(uint(i+1), "user-1", fmt.Sprintf("run-%d", i)),
				)
				if err != nil && !errors.Is(err, ErrSessionManagerClosed) {
					unexpected.Add(1)
				}
				return
			}
			_, closeOperation, err := manager.OpenEphemeral(context.Background(), testCallConfig(), 0)
			if err == nil {
				if closeErr := closeOperation(context.Background()); closeErr != nil {
					unexpected.Add(1)
				}
			} else if !errors.Is(err, ErrSessionManagerClosed) {
				unexpected.Add(1)
			}
		}(i)
	}
	close(start)
	if err := manager.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if unexpected.Load() != 0 || len(manager.entries) != 0 || len(manager.ephemeral) != 0 {
		t.Fatalf(
			"unexpected=%d entries=%d ephemeral=%d",
			unexpected.Load(),
			len(manager.entries),
			len(manager.ephemeral),
		)
	}
}

func TestSessionManagerConcurrentCloseAllSharesCompletionAndResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cleanupErr := errors.New("shared cleanup failure")
		transport := &managerTransport{
			cleanupErr:     cleanupErr,
			cleanupEntered: make(chan string, 1),
			cleanupRelease: make(chan struct{}),
		}
		var releaseOnce sync.Once
		releaseCleanup := func() {
			releaseOnce.Do(func() { close(transport.cleanupRelease) })
		}
		t.Cleanup(releaseCleanup)

		manager := newRunSessionManager(transport)
		op, err := manager.Acquire(context.Background(), managerAcquireInput(1, "user-1", "run-1"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = op.ListTools(context.Background()); err != nil {
			t.Fatal(err)
		}

		firstDone := make(chan error, 1)
		go func() { firstDone <- manager.CloseAll(context.Background()) }()
		<-transport.cleanupEntered

		secondStarted := make(chan struct{})
		secondDone := make(chan error, 1)
		go func() {
			close(secondStarted)
			secondDone <- manager.CloseAll(context.Background())
		}()
		<-secondStarted
		synctest.Wait()
		select {
		case secondErr := <-secondDone:
			t.Fatalf("second CloseAll returned before shared cleanup completed: %v", secondErr)
		default:
		}

		releaseCleanup()
		synctest.Wait()
		firstErr := <-firstDone
		secondErr := <-secondDone
		if !errors.Is(firstErr, cleanupErr) || !errors.Is(secondErr, cleanupErr) {
			t.Fatalf("CloseAll errors did not share cleanup result: first=%v second=%v", firstErr, secondErr)
		}
		if err = manager.CloseAll(context.Background()); !errors.Is(err, cleanupErr) {
			t.Fatalf("later CloseAll error = %v, want shared cleanup failure", err)
		}
		if transport.terminations.Load() != 1 {
			t.Fatalf("terminations = %d, want exactly 1", transport.terminations.Load())
		}
		if _, err = manager.Acquire(context.Background(), managerAcquireInput(2, "user-1", "run-2")); !errors.Is(err, ErrSessionManagerClosed) {
			t.Fatalf("Acquire after CloseAll = %v", err)
		}
		if _, _, err = manager.OpenEphemeral(context.Background(), testCallConfig(), 0); !errors.Is(err, ErrSessionManagerClosed) {
			t.Fatalf("OpenEphemeral after CloseAll = %v", err)
		}
	})
}

func TestSessionManagerCloseAllWaitsForTimedOutEphemeralCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &managerTransport{
			blockCalls: true,
			entered:    make(chan string, 1),
			release:    make(chan struct{}),
		}
		var releaseOnce sync.Once
		releaseCall := func() {
			releaseOnce.Do(func() { close(transport.release) })
		}
		t.Cleanup(releaseCall)

		manager := newRunSessionManager(transport)
		op, closeOperation, err := manager.OpenEphemeral(context.Background(), testCallConfig(), 0)
		if err != nil {
			t.Fatal(err)
		}
		callDone := make(chan error, 1)
		go func() {
			_, callErr := op.CallTool(context.Background(), CallInput{ToolName: "echo", ArgumentsJSON: `{}`})
			callDone <- callErr
		}()
		<-transport.entered

		originalWaitTimeout := manager.cleanupWaitTimeout
		manager.cleanupWaitTimeout = 0
		firstCloseErr := closeOperation(context.Background())
		manager.cleanupWaitTimeout = originalWaitTimeout
		if !errors.Is(firstCloseErr, context.DeadlineExceeded) {
			t.Fatalf("first close error = %v, want deadline exceeded", firstCloseErr)
		}

		closeAllDone := make(chan error, 1)
		go func() { closeAllDone <- manager.CloseAll(context.Background()) }()
		synctest.Wait()
		select {
		case closeAllErr := <-closeAllDone:
			t.Fatalf("CloseAll returned before the tracked ephemeral cleanup completed: %v", closeAllErr)
		default:
		}

		releaseCall()
		synctest.Wait()
		if callErr := <-callDone; callErr != nil {
			t.Fatal(callErr)
		}
		if closeAllErr := <-closeAllDone; closeAllErr != nil {
			t.Fatal(closeAllErr)
		}
		if transport.terminations.Load() != 1 {
			t.Fatalf("terminations = %d, want exactly 1", transport.terminations.Load())
		}
		if err = closeOperation(context.Background()); err != nil {
			t.Fatalf("later callback error = %v", err)
		}
	})
}

func TestSessionManagerConcurrentIsolationStress(t *testing.T) {
	runSessionManagerConcurrentIsolationStress(t)
}

func runSessionManagerConcurrentIsolationStress(t *testing.T) {
	t.Helper()
	const (
		userCount       = 50
		runsPerUser     = 2
		serverCount     = 2
		callersPerKey   = 2
		wantSessionKeys = userCount * runsPerUser * serverCount
	)

	signer := newStressIsolationSigner()
	roundTripper := newStressIsolationRoundTripper(wantSessionKeys)
	client := &http.Client{Transport: roundTripper}
	manager := newRunSessionManager(newHTTPTransport(client, signer))
	type stressOperation struct {
		key       string
		operation Operation
	}
	operations := make([]stressOperation, 0, wantSessionKeys)

	for userIndex := range userCount {
		userPublicID := fmt.Sprintf("stress-user-%02d", userIndex)
		for runIndex := range runsPerUser {
			runID := fmt.Sprintf("stress-run-%d", runIndex)
			for serverIndex := range serverCount {
				serverID := uint(serverIndex + 1)
				key := stressIsolationKey(userPublicID, runID, serverID)
				operation, err := manager.Acquire(t.Context(), AcquireInput{
					ServerID:        serverID,
					ServerUpdatedAt: time.Date(2026, time.July, 13, int(serverID), 0, 0, 0, time.UTC),
					RetryCount:      0,
					CallConfig: CallConfig{
						BaseURL:   "https://stress.example.test/mcp",
						AuthToken: "auth-" + userPublicID,
						TimeoutMS: 30000,
						CustomHeaders: map[string]string{
							"X-Test-Key":    key,
							"X-Test-User":   userPublicID,
							"X-Test-Run":    runID,
							"X-Test-Server": fmt.Sprint(serverID),
						},
						Context: TemplateContext{
							Mode:         ContextModeChat,
							UserPublicID: userPublicID,
							RunID:        runID,
						},
						SignedContext: &SignedContextConfig{
							Secret:         "stress-signing-config",
							Issuer:         "https://chat.example.test",
							Audience:       fmt.Sprintf("stress-server-%d", serverID),
							KeyID:          "ctx_stress",
							ExpiresSeconds: 60,
						},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				operations = append(operations, stressOperation{key: key, operation: operation})
			}
		}
	}

	manager.mu.Lock()
	if len(manager.entries) != wantSessionKeys {
		manager.mu.Unlock()
		t.Fatalf("manager entries = %d, want %d", len(manager.entries), wantSessionKeys)
	}
	keysPerServer := map[uint]int{}
	for key := range manager.entries {
		keysPerServer[key.ServerID]++
	}
	manager.mu.Unlock()
	for serverID := uint(1); serverID <= serverCount; serverID++ {
		if keysPerServer[serverID] != userCount*runsPerUser {
			t.Fatalf("Server %d keys = %d, want %d", serverID, keysPerServer[serverID], userCount*runsPerUser)
		}
	}

	callErrors := make(chan error, wantSessionKeys*callersPerKey)
	guardCtx, guardCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer guardCancel()
	var callers sync.WaitGroup
	for _, item := range operations {
		arguments, err := json.Marshal(map[string]string{"expectedKey": item.key})
		if err != nil {
			t.Fatal(err)
		}
		for range callersPerKey {
			callers.Go(func() {
				result, err := item.operation.CallTool(guardCtx, CallInput{ToolName: "echo", ArgumentsJSON: string(arguments)})
				if err != nil {
					callErrors <- err
					return
				}
				var echoed struct {
					ExpectedKey string `json:"expectedKey"`
				}
				if decodeErr := json.Unmarshal([]byte(result), &echoed); decodeErr != nil {
					callErrors <- errors.New("stress response is invalid")
					return
				}
				if keyErr := validateStressExpectedKey(item.key, echoed.ExpectedKey); keyErr != nil {
					callErrors <- keyErr
				}
			})
		}
	}
	var barrierErr error
	select {
	case <-roundTripper.allFirstCallsEntered:
		close(roundTripper.releaseFirstCalls)
	case <-guardCtx.Done():
		close(roundTripper.releaseFirstCalls)
		barrierErr = guardCtx.Err()
	}
	callers.Wait()
	if barrierErr != nil {
		t.Fatalf("first-call barrier did not reach %d session keys: %v", wantSessionKeys, barrierErr)
	}
	close(callErrors)
	for err := range callErrors {
		t.Errorf("concurrent CallTool failed: %v", err)
	}
	if t.Failed() {
		return
	}

	roundTripper.mu.Lock()
	if roundTripper.maxGlobalInFlight != wantSessionKeys {
		roundTripper.mu.Unlock()
		t.Fatalf("cross-session max in-flight = %d, want %d", roundTripper.maxGlobalInFlight, wantSessionKeys)
	}
	for key, maxInFlight := range roundTripper.maxInFlightByKey {
		if maxInFlight != 1 {
			roundTripper.mu.Unlock()
			t.Fatalf("session %q max in-flight = %d, want 1", key, maxInFlight)
		}
	}
	roundTripper.mu.Unlock()

	closeRunCalls := 0
	for userIndex := range userCount {
		userPublicID := fmt.Sprintf("stress-user-%02d", userIndex)
		for runIndex := range runsPerUser {
			runID := fmt.Sprintf("stress-run-%d", runIndex)
			if err := manager.CloseRun(t.Context(), userPublicID, runID); err != nil {
				t.Fatalf("CloseRun(%q, %q): %v", userPublicID, runID, err)
			}
			closeRunCalls++
		}
	}
	if closeRunCalls != userCount*runsPerUser {
		t.Fatalf("CloseRun calls = %d, want %d", closeRunCalls, userCount*runsPerUser)
	}
	manager.mu.Lock()
	entriesAfterCloseRun := len(manager.entries)
	manager.mu.Unlock()
	if entriesAfterCloseRun != 0 {
		t.Fatalf("entries after CloseRun = %d, want 0", entriesAfterCloseRun)
	}
	if err := manager.CloseAll(t.Context()); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}
	manager.mu.Lock()
	remaining := len(manager.entries) + len(manager.ephemeral) + len(manager.closingEntries)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("manager retained entries after cleanup = %d", remaining)
	}

	violations := append(roundTripper.violationSnapshot(), signer.violationSnapshot()...)
	if len(violations) != 0 {
		t.Fatalf("cross-session isolation violations = %#v", violations)
	}
	roundTripper.assertExactCounts(t, wantSessionKeys, callersPerKey)
	signer.assertExactCounts(t, wantSessionKeys, 5)
}

func stressIsolationKey(userPublicID string, runID string, serverID uint) string {
	return fmt.Sprintf("%s|%s|server-%d", userPublicID, runID, serverID)
}

func validateStressExpectedKey(expectedKey string, physicalKey string) error {
	if expectedKey == "" || expectedKey != physicalKey {
		return fmt.Errorf("stress expected key does not match physical identity")
	}
	return nil
}

type stressIsolationSigner struct {
	mu         sync.Mutex
	callsByKey map[string]int
	violations []string
}

func newStressIsolationSigner() *stressIsolationSigner {
	return &stressIsolationSigner{callsByKey: make(map[string]int)}
}

func (s *stressIsolationSigner) Sign(templateContext TemplateContext, config SignedContextConfig) (string, error) {
	serverText := strings.TrimPrefix(config.Audience, "stress-server-")
	key := fmt.Sprintf("%s|%s|server-%s", templateContext.UserPublicID, templateContext.RunID, serverText)
	s.mu.Lock()
	defer s.mu.Unlock()
	if templateContext.Mode != ContextModeChat || serverText == config.Audience || templateContext.UserPublicID == "" || templateContext.RunID == "" {
		s.violations = append(s.violations, "invalid signer input")
	}
	s.callsByKey[key]++
	return fmt.Sprintf("signed|%s|%s|%s|%d", templateContext.UserPublicID, templateContext.RunID, config.Audience, s.callsByKey[key]), nil
}

func (s *stressIsolationSigner) violationSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.violations...)
}

func (s *stressIsolationSigner) assertExactCounts(t *testing.T, wantKeys int, wantCallsPerKey int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.callsByKey) != wantKeys {
		t.Fatalf("signer keys = %d, want %d", len(s.callsByKey), wantKeys)
	}
	for key, calls := range s.callsByKey {
		if calls != wantCallsPerKey {
			t.Fatalf("signer calls for %q = %d, want %d", key, calls, wantCallsPerKey)
		}
	}
}

type stressIsolationRequestCounts struct {
	initialize  int
	initialized int
	calls       int
	deletes     int
}

type stressIsolationRoundTripper struct {
	target               int
	allFirstCallsEntered chan struct{}
	releaseFirstCalls    chan struct{}
	readyOnce            sync.Once
	mu                   sync.Mutex
	countsByKey          map[string]stressIsolationRequestCounts
	sessionsByKey        map[string]string
	firstCallKeys        map[string]struct{}
	inFlightByKey        map[string]int
	maxInFlightByKey     map[string]int
	globalInFlight       int
	maxGlobalInFlight    int
	violations           []string
}

func newStressIsolationRoundTripper(target int) *stressIsolationRoundTripper {
	return &stressIsolationRoundTripper{
		target:               target,
		allFirstCallsEntered: make(chan struct{}),
		releaseFirstCalls:    make(chan struct{}),
		countsByKey:          make(map[string]stressIsolationRequestCounts),
		sessionsByKey:        make(map[string]string),
		firstCallKeys:        make(map[string]struct{}),
		inFlightByKey:        make(map[string]int),
		maxInFlightByKey:     make(map[string]int),
	}
}

func (r *stressIsolationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		defer func() {
			if err := req.Body.Close(); err != nil {
				r.recordViolation("request body close failed")
			}
		}()
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, errors.New("stress request body read failed")
		}
	}
	method := ""
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if len(body) != 0 {
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, errors.New("stress request body decode failed")
		}
		method = envelope.Method
	} else if req.Method == http.MethodPost {
		return nil, errors.New("stress request body is empty")
	}
	key := req.Header.Get("X-Test-Key")
	userPublicID := req.Header.Get("X-Test-User")
	runID := req.Header.Get("X-Test-Run")
	serverText := req.Header.Get("X-Test-Server")
	wantKey := fmt.Sprintf("%s|%s|server-%s", userPublicID, runID, serverText)
	wantSignedPrefix := fmt.Sprintf("signed|%s|%s|stress-server-%s|", userPublicID, runID, serverText)
	expectedKey := ""
	if method == "tools/call" {
		var callEnvelope struct {
			Params struct {
				Arguments struct {
					ExpectedKey string `json:"expectedKey"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &callEnvelope); err != nil {
			return nil, errors.New("stress call body decode failed")
		}
		expectedKey = callEnvelope.Params.Arguments.ExpectedKey
	}

	r.mu.Lock()
	if key == "" || key != wantKey {
		r.violations = append(r.violations, "custom header identity mismatch")
	}
	if req.Header.Get("Authorization") != "Bearer auth-"+userPublicID {
		r.violations = append(r.violations, "authorization identity mismatch")
	}
	if !strings.HasPrefix(req.Header.Get("X-DEEIX-Context"), wantSignedPrefix) {
		r.violations = append(r.violations, "signed context identity mismatch")
	}
	if method == "tools/call" {
		if err := validateStressExpectedKey(expectedKey, key); err != nil {
			r.violations = append(r.violations, "call expected key mismatch")
		}
	}
	counts := r.countsByKey[key]
	sessionID := r.sessionsByKey[key]
	shouldBlock := false
	switch {
	case req.Method == http.MethodDelete:
		counts.deletes++
	case method == "initialize":
		counts.initialize++
		sessionID = "session-" + strings.ReplaceAll(key, "|", "-")
		r.sessionsByKey[key] = sessionID
	case method == "notifications/initialized":
		counts.initialized++
	case method == "tools/call":
		counts.calls++
		r.inFlightByKey[key]++
		if r.inFlightByKey[key] > r.maxInFlightByKey[key] {
			r.maxInFlightByKey[key] = r.inFlightByKey[key]
		}
		r.globalInFlight++
		if r.globalInFlight > r.maxGlobalInFlight {
			r.maxGlobalInFlight = r.globalInFlight
		}
		if _, exists := r.firstCallKeys[key]; !exists {
			r.firstCallKeys[key] = struct{}{}
			shouldBlock = true
			if len(r.firstCallKeys) == r.target {
				r.readyOnce.Do(func() { close(r.allFirstCallsEntered) })
			}
		}
	}
	r.countsByKey[key] = counts
	if method != "initialize" && req.Header.Get("MCP-Protocol-Version") != protocolVersion {
		r.violations = append(r.violations, "protocol header mismatch")
	}
	if method == "initialize" {
		if req.Header.Get("MCP-Protocol-Version") != "" || req.Header.Get("MCP-Session-Id") != "" {
			r.violations = append(r.violations, "initialize carried session headers")
		}
	} else if sessionID == "" || req.Header.Get("MCP-Session-Id") != sessionID {
		r.violations = append(r.violations, "session header mismatch")
	}
	r.mu.Unlock()

	if shouldBlock {
		select {
		case <-r.releaseFirstCalls:
		case <-req.Context().Done():
			r.finishCall(key)
			return nil, req.Context().Err()
		}
	}
	if method == "tools/call" {
		r.finishCall(key)
	}

	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}
	switch {
	case req.Method == http.MethodDelete:
		response.StatusCode = http.StatusNoContent
	case method == "initialize":
		response.Header.Set("Content-Type", "application/json")
		response.Header.Set("MCP-Session-Id", sessionID)
		response.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%q,"capabilities":{}}}`,
			envelope.ID,
			protocolVersion,
		)))
	case method == "notifications/initialized":
		response.StatusCode = http.StatusAccepted
	case method == "tools/call":
		response.Header.Set("Content-Type", "application/json")
		response.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%s,"result":{"expectedKey":%q}}`,
			envelope.ID,
			key,
		)))
	default:
		response.StatusCode = http.StatusBadRequest
	}
	return response, nil
}

func (r *stressIsolationRoundTripper) recordViolation(message string) {
	r.mu.Lock()
	r.violations = append(r.violations, message)
	r.mu.Unlock()
}

func (r *stressIsolationRoundTripper) finishCall(key string) {
	r.mu.Lock()
	r.inFlightByKey[key]--
	r.globalInFlight--
	r.mu.Unlock()
}

func (r *stressIsolationRoundTripper) violationSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.violations...)
}

func (r *stressIsolationRoundTripper) assertExactCounts(t *testing.T, wantKeys int, wantCalls int) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.countsByKey) != wantKeys {
		t.Fatalf("physical session keys = %d, want %d", len(r.countsByKey), wantKeys)
	}
	for key, counts := range r.countsByKey {
		want := stressIsolationRequestCounts{initialize: 1, initialized: 1, calls: wantCalls, deletes: 1}
		if counts != want {
			t.Fatalf("physical counts for %q = %#v, want %#v", key, counts, want)
		}
	}
}

func TestStressIsolationExpectedKeyRejectsPermutedOperation(t *testing.T) {
	if err := validateStressExpectedKey("user-a|run-a|server-1", "user-a|run-a|server-1"); err != nil {
		t.Fatalf("valid expectedKey rejected: %v", err)
	}
	if err := validateStressExpectedKey("user-a|run-a|server-1", "user-b|run-b|server-2"); err == nil {
		t.Fatal("permuted operation key was accepted")
	}
}

type stressIsolationFaultBody struct {
	reader io.Reader
	err    error
	closed atomic.Bool
}

func (b *stressIsolationFaultBody) Read(buffer []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	return b.reader.Read(buffer)
}

func (b *stressIsolationFaultBody) Close() error {
	b.closed.Store(true)
	return nil
}

func TestStressIsolationRoundTripperClosesAndRejectsBadBodies(t *testing.T) {
	for _, test := range []struct {
		name string
		body *stressIsolationFaultBody
	}{
		{name: "read failure", body: &stressIsolationFaultBody{err: errors.New("fixture read failure")}},
		{name: "malformed json", body: &stressIsolationFaultBody{reader: strings.NewReader(`{`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := newStressIsolationRoundTripper(1)
			_, err := roundTripper.RoundTrip(&http.Request{
				Method: http.MethodPost,
				Header: make(http.Header),
				Body:   test.body,
			})
			if err == nil {
				t.Fatal("bad request body was accepted")
			}
			if !test.body.closed.Load() {
				t.Fatal("request body was not closed")
			}
		})
	}
}
