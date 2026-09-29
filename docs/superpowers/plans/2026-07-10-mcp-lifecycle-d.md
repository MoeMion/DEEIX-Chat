# Run-Scoped MCP Lifecycle and Protocol Upgrade Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade the MCP client to protocol `2025-11-25` and provide bounded, cancellation-safe, run/server-scoped sessions without replaying an ambiguously delivered tool call.

**Architecture:** Keep application-owned `TemplateContext` and rendered custom Headers immutable for one conversation run/server, while an in-memory MCP `SessionManager` keys sessions by server, user public ID, run ID, authentication identity, and configuration revision. A protocol-only `Transport` owns every MCP/HTTP Header, parses bounded JSON or multi-event SSE responses, resumes only an interrupted SSE stream with `Last-Event-ID`, and exposes delivery state to a classified retry policy; an `Operation` serializes calls within one server session while sessions for different servers remain independent.

**Tech Stack:** Go 1.26, `context`, `net/http`, `net/http/httptrace`, `encoding/json`, `bufio`, `sync`, `httptest`, existing OpenTelemetry HTTP transport, existing phase-C `ContextSigner`.

## Global Constraints

- Run every command from the active isolated feature worktree root; phase-D scope checks rely on the local base ref recorded in Task 1.
- Start only after the A+B and C release gates in `docs/superpowers/plans/2026-07-10-mcp-custom-headers-roadmap.md` pass; this plan must not compensate for an incomplete earlier phase.
- Add and pass the complete transport contract tests while `protocolVersion` is still `2025-06-18`; change the advertised version to `2025-11-25` only in Task 3.
- The transport exclusively owns `Accept`, `Content-Type`, `MCP-Protocol-Version`, `MCP-Session-Id`, and `Last-Event-ID`; set protocol Headers only after custom Header validation.
- Retain this complete case-insensitive exact-name blacklist without additions or omissions: `Accept`, `Accept-Encoding`, `Authorization`, `Baggage`, `Connection`, `Content-Length`, `Content-Type`, `Cookie`, `Host`, `Keep-Alive`, `Last-Event-ID`, `MCP-Protocol-Version`, `MCP-Session-Id`, `Origin`, `Proxy-Authenticate`, `Proxy-Authorization`, `Proxy-Connection`, `Set-Cookie`, `TE`, `Traceparent`, `Tracestate`, `Trailer`, `Transfer-Encoding`, `Upgrade`, `User-Agent`, and `X-DEEIX-Context`.
- Retain this complete case-insensitive prefix blacklist: `MCP-`, `Proxy-`, `Sec-`, and `X-DEEIX-`.
- Outbound and inbound `MCP-Session-Id` values are at most 4096 bytes, contain only visible ASCII bytes `0x21..0x7e`, and pass `httpguts.ValidHeaderFieldValue`; an invalid value is never copied into another request.
- Retained `Last-Event-ID` values are at most 4096 bytes, valid UTF-8, contain no NUL/CR/LF, and pass `httpguts.ValidHeaderFieldValue`; an explicit empty SSE `id:` clears the cursor.
- Reuse one MCP session per conversation run and MCP Server, never globally by URL.
- Every run-session key contains `ServerID`, user public ID, run ID, an opaque authentication fingerprint, and an opaque configuration version derived from immutable non-context Server configuration plus runtime retry/timeout settings. The per-call `TemplateContext` and rendered custom Header map are excluded from that fingerprint and are compared separately for exact cache-hit equality.
- A run-scoped session retains one immutable `TemplateContext`, one immutable rendered custom Header map, and one immutable optional `SignedContextConfig`; a different value for the same key returns `ErrSessionContextMismatch` instead of mutating the live session.
- Call the phase-C signer immediately before every HTTP POST, GET-resume, or DELETE. A short-lived JWT may change between requests, but the `TemplateContext` and signing configuration captured by the session may not.
- Consume a complete JSON-RPC response or all SSE events needed to find the matching request ID. Enforce 8 MiB per HTTP response, 1 MiB per SSE event, 1024 SSE events per response, two GET resume attempts, 128 `tools/list` pages, and 10000 accumulated tools.
- Resume SSE only with GET plus a non-empty `Last-Event-ID` from the interrupted stream and an existing MCP session. Never resume by replaying the original POST.
- Rebuild a missing/invalid session only at an idempotent boundary: initialize, `notifications/initialized`, `tools/list`, or an explicitly not-sent `tools/call`. A `tools/call` that may have reached the server is never replayed.
- Reinterpret existing `MCPToolRetryCount` only as the maximum retry budget consulted by the classified retry policy; it no longer means retry every tool error.
- Serialize concurrent operations sharing one server session. Operations using different server sessions do not share a mutex and may proceed in parallel subject to the existing global tool limiter.
- End every opened run session that received a non-empty server session ID with a bounded DELETE attempt, including success, error, cancellation, and timeout. A selected-but-never-opened Server sends no DELETE. DELETE 404/405 is terminal cleanup success; cleanup must not keep the run alive indefinitely.
- This phase does not change frontend code, administrator MCP HTTP APIs, Swagger, response DTOs, database tables, or database columns.
- Never log, trace, audit, store in `LastError`, or expose through control-plane/error responses a rendered Header value, bearer token, signed JWT, signing secret, authentication fingerprint, raw SSE data payload, or tool arguments. A validated matching `tools/call.result` may flow only through the intended LLM tool-result path.
- Commit subjects use the repository-approved `type: subject` form and contain only English letters, digits, spaces, periods, hyphens, and underscores.

---

## File Structure and Frozen Interfaces

Create or split the MCP implementation by responsibility:

- `backend/internal/infra/mcp/protocol.go`: JSON-RPC envelopes, protocol constants, operation kind, delivery state, and typed protocol errors.
- `backend/internal/infra/mcp/transport.go`: request construction, phase-C per-request signing, protocol Header ownership, size limits, HTTP delivery classification, and JSON/SSE dispatch.
- `backend/internal/infra/mcp/sse.go`: bounded SSE event decoding and `Last-Event-ID` cursor tracking.
- `backend/internal/infra/mcp/operation.go`: initialize/initialized lifecycle, matching JSON-RPC responses, SSE resume, paginated `tools/list`, `tools/call`, and DELETE.
- `backend/internal/infra/mcp/session_manager.go`: run-scoped session cache, immutable context checks, same-session serialization, per-run cleanup, and process shutdown cleanup.
- `backend/internal/infra/mcp/retry.go`: retry decisions based on operation idempotency, delivery state, HTTP/session status, cancellation, and retry budget.
- `backend/internal/infra/mcp/client.go`: retain public compatibility methods for probe/sync while delegating protocol work to `Operation` and `Transport`; remove the old first-SSE-block parser and blanket per-call initialization internals.

Phase A+B already owns this input and its semantics:

```go
type TemplateContext struct {
	Mode                     ContextMode
	UserPublicID             string
	UserDisplayName          string
	UserEmail                string
	UserRole                 string
	ConversationPublicID     string
	AssistantMessagePublicID string
	UserMessagePublicID      string
	RequestID                string
	RunID                    string
	TraceID                  string
}
```

Phase C already owns the signer. Do not rename, wrap, cache, or broaden it:

```go
type SignedContextConfig struct {
	Secret          string
	Issuer          string
	Audience        string
	KeyID           string
	ExpiresSeconds  int
	IncludeName     bool
	IncludeEmail    bool
	IncludeRole     bool
}

type ContextSigner interface {
	Sign(context TemplateContext, config SignedContextConfig) (string, error)
}
```

Phase C also appends `SignedContext *SignedContextConfig` to A+B `CallConfig`; nil is the only disabled state.

Phase D adds these exact interfaces and value types in `backend/internal/infra/mcp/protocol.go` and `backend/internal/infra/mcp/session_manager.go`:

```go
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

type SessionKey struct {
	ServerID        uint
	UserPublicID    string
	RunID           string
	AuthIdentity    string
	ConfigVersion   string
}

type AcquireInput struct {
	ServerID        uint
	ServerUpdatedAt time.Time
	CallConfig      CallConfig
	RetryCount      int
}

type Operation interface {
	ListTools(context.Context) ([]Tool, error)
	CallTool(context.Context, CallInput) (string, error)
}

type SessionManager interface {
	Acquire(context.Context, AcquireInput) (Operation, error)
	OpenEphemeral(context.Context, CallConfig, int) (Operation, func(context.Context) error, error)
	CloseRun(context.Context, string, string) error
	CloseAll(context.Context) error
}

type Transport interface {
	Do(context.Context, TransportRequest) (TransportResponse, error)
}
```

`Client.contextSigner` remains the sole signer dependency. `httpTransport.Do` calls it for every physical HTTP request with a copied `TemplateContext` and `SignedContextConfig`; neither `SessionManager` nor `Operation` stores a JWT string. `ConfigVersion` fingerprints Server revision, endpoint, timeout/retry settings, and immutable `SignedContextConfig` fields including the encrypted-secret identity; it deliberately excludes `TemplateContext` and the rendered custom Header map because `Acquire` compares those values separately. Persisted or runtime changes isolate future acquisitions, while an already acquired run-session remains immutable until `CloseRun`.

---

### Task 1: Freeze the pre-upgrade transport contract

**Files:**
- Create: `backend/internal/infra/mcp/transport_test.go`
- Modify: `backend/internal/infra/mcp/client_test.go`
- Test: `backend/internal/infra/mcp/transport_test.go`
- Test: `backend/internal/infra/mcp/client_test.go`

**Interfaces:**
- Consumes: A+B `CallConfig`, `TemplateContext`, Header rendering/blacklist, phase-C `ContextSigner`, and the current `protocolVersion = "2025-06-18"`.
- Produces: request-capture fixtures and transport contract tests which Task 2 must satisfy without changing the protocol version.

- [ ] **Step 0: Record the exact phase-D base in the isolated worktree**

Run from the feature worktree root before the first D edit:

```powershell
git status --short
git update-ref refs/codex/phase-d-base HEAD
git rev-parse refs/codex/phase-d-base
```

Expected: status is clean and the printed SHA equals the pre-D `HEAD`. Keep this local ref until Task 6 scope verification; it prevents unrelated `dev` history from contaminating the diff.

- [ ] **Step 1: Add a request recorder and deterministic signer to the transport tests**

Add these complete test-only types to `transport_test.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type capturedRequest struct {
	Method string
	Header http.Header
	Body   map[string]interface{}
}

type requestRecorder struct {
	mu       sync.Mutex
	requests []capturedRequest
}

func (r *requestRecorder) append(req *http.Request) (capturedRequest, error) {
	item := capturedRequest{Method: req.Method, Header: req.Header.Clone()}
	if req.Body != nil && req.Method == http.MethodPost {
		if err := json.NewDecoder(req.Body).Decode(&item.Body); err != nil {
			return capturedRequest{}, err
		}
	}
	r.mu.Lock()
	r.requests = append(r.requests, item)
	r.mu.Unlock()
	return item, nil
}

func (r *requestRecorder) snapshot() []capturedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]capturedRequest, len(r.requests))
	copy(result, r.requests)
	return result
}

type incrementingSigner struct {
	mu     sync.Mutex
	inputs []capturedSignerInput
}

type capturedSignerInput struct {
	TemplateContext TemplateContext
	Config          SignedContextConfig
}

func (s *incrementingSigner) Sign(templateContext TemplateContext, config SignedContextConfig) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, capturedSignerInput{
		TemplateContext: templateContext,
		Config:          config,
	})
	return "signed-" + time.Unix(int64(len(s.inputs)), 0).UTC().Format("150405"), nil
}

type staticSigner struct {
	token string
	err   error
}

func (s staticSigner) Sign(TemplateContext, SignedContextConfig) (string, error) {
	return s.token, s.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type waitForContextBody struct{ ctx context.Context }

func (b *waitForContextBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (*waitForContextBody) Close() error { return nil }

func contractRequest(endpoint string) TransportRequest {
	return TransportRequest{
		Operation:  OperationListTools,
		HTTPMethod: http.MethodPost,
		Endpoint:   endpoint,
		Body:       []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`),
		RequestID:  json.RawMessage(`7`),
		TemplateContext: TemplateContext{
			Mode:         ContextModeChat,
			UserPublicID: "user_public",
			RunID:        "run_public",
		},
	}
}

func notifyWroteRequest(req *http.Request, writeErr error) {
	trace := httptrace.ContextClientTrace(req.Context())
	if trace != nil && trace.WroteRequest != nil {
		trace.WroteRequest(httptrace.WroteRequestInfo{Err: writeErr})
	}
}
```

- [ ] **Step 2: Write the full lifecycle/Header ownership test while the advertised version is still 2025-06-18**

Add this complete lifecycle test. It captures `initialize`, `notifications/initialized`, `tools/list`, `tools/call`, and DELETE, and it stays red until the new transport owns protocol Headers and cleanup:

```go
func assertProtocolHeaders(t *testing.T, req capturedRequest, wantSession bool) {
	t.Helper()
	if got := req.Header.Get("Accept"); got != "application/json, text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}
	if req.Method == http.MethodPost && req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", req.Header.Get("Content-Type"))
	}
	if wantSession {
		if req.Header.Get("MCP-Session-Id") != "session-1" {
			t.Fatalf("MCP-Session-Id = %q", req.Header.Get("MCP-Session-Id"))
		}
		if req.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
			t.Fatalf("MCP-Protocol-Version = %q", req.Header.Get("MCP-Protocol-Version"))
		}
	} else if req.Header.Get("MCP-Session-Id") != "" || req.Header.Get("MCP-Protocol-Version") != "" {
		t.Fatalf("initialize unexpectedly had session protocol headers: %#v", req.Header)
	}
	if req.Header.Get("X-Custom-Tenant") != "tenant-user_public" {
		t.Fatalf("custom Header = %q", req.Header.Get("X-Custom-Tenant"))
	}
}

func TestClientTransportContractBeforeProtocolUpgrade(t *testing.T) {
	recorder := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		item, err := recorder.append(req)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		method, _ := item.Body["method"].(string)
		id := item.Body["id"]
		switch method {
		case "initialize":
			w.Header().Set("MCP-Session-Id", "session-1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"test","version":"1"}}}`, id)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"tools":[]}}`, id)
		case "tools/call":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"content":[{"type":"text","text":"ok"}]}}`, id)
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	templateContext := TemplateContext{
		Mode:         ContextModeChat,
		UserPublicID: "user_public",
		RunID:        "run_public",
	}
	signer := &incrementingSigner{}
	client := NewClient()
	client.httpClient = server.Client()
	client.contextSigner = signer
	cfg := CallConfig{
		BaseURL:       server.URL,
		TimeoutMS:     1000,
		CustomHeaders: map[string]string{"X-Custom-Tenant": "tenant-user_public"},
		Context:       templateContext,
		SignedContext: &SignedContextConfig{KeyID: "ctx_test"},
	}
	if _, err := client.ListTools(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(context.Background(), cfg, CallInput{ToolName: "echo", ArgumentsJSON: `{}`}); err != nil {
		t.Fatal(err)
	}

	requests := recorder.snapshot()
	if len(requests) != 8 {
		t.Fatalf("request count = %d, want 8", len(requests))
	}
	seenTokens := map[string]struct{}{}
	for _, req := range requests {
		method, _ := req.Body["method"].(string)
		assertProtocolHeaders(t, req, method != "initialize")
		token := req.Header.Get("X-DEEIX-Context")
		if token == "" {
			t.Fatal("missing signed context")
		}
		seenTokens[token] = struct{}{}
	}
	if len(seenTokens) != len(requests) {
		t.Fatalf("signed tokens were reused: %v", seenTokens)
	}
	signer.mu.Lock()
	defer signer.mu.Unlock()
	for _, input := range signer.inputs {
		if !reflect.DeepEqual(input.TemplateContext, templateContext) {
			t.Fatalf("signer context changed: %#v", input.TemplateContext)
		}
	}

	for _, name := range []string{"mcp-session-id", "MCP-Protocol-Version", "Last-Event-ID", "X-DEEIX-Context"} {
		if err := ValidateRenderedCustomHeaders(map[string]string{name: "malicious"}); err == nil {
			t.Fatalf("reserved Header %q was accepted", name)
		}
	}
}
```

This test deliberately invokes both public compatibility methods so Task 2 must keep the old per-operation facade green before Task 3 introduces run reuse.

- [ ] **Step 3: Add JSON, multi-event SSE, delivery, timeout, cancellation, and body-limit transport cases**

Add these concrete table-driven tests. They use `newHTTPTransport` directly so the red state is a missing production boundary, not a vacuous assertion:

```go
func TestTransportContractResponsesAndBounds(t *testing.T) {
	tests := []struct {
		name          string
		contentType   string
		body          string
		request       func(string) TransportRequest
		sessionID     string
		wantErr       error
		wantAnyErr    bool
		wantSessionID string
	}{
		{name: "json", contentType: "application/json", body: `{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`},
		{name: "sse notification then match", contentType: "text/event-stream", body: "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"ok\":true}}\n\n"},
		{name: "sse multiline", contentType: "text/event-stream", body: "data: {\"jsonrpc\":\"2.0\",\n" + "data: \"id\":7,\"result\":{\"ok\":true}}\n\n"},
		{name: "mismatched id", contentType: "application/json", body: `{"jsonrpc":"2.0","id":8,"result":{}}`, wantErr: ErrMismatchedResponseID},
		{name: "malformed initialize keeps session", contentType: "application/json", body: `{`, sessionID: "session-captured", wantAnyErr: true, wantSessionID: "session-captured"},
		{name: "response limit", contentType: "application/json", body: strings.Repeat("x", int(maxResponseBytes)+1), wantErr: ErrResponseTooLarge},
		{
			name: "initialized accepts empty",
			request: func(endpoint string) TransportRequest {
				req := contractRequest(endpoint)
				req.Operation = OperationInitialized
				req.RequestID = nil
				req.Body = []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
				return req
			},
		},
		{
			name: "delete accepts empty",
			request: func(endpoint string) TransportRequest {
				req := contractRequest(endpoint)
				req.Operation = OperationTerminate
				req.HTTPMethod = http.MethodDelete
				req.RequestID = nil
				req.Body = nil
				return req
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.contentType != "" {
					w.Header().Set("Content-Type", tt.contentType)
				}
				if tt.sessionID != "" {
					w.Header().Set("MCP-Session-Id", tt.sessionID)
				}
				if tt.body != "" {
					_, _ = io.WriteString(w, tt.body)
				}
			}))
			defer server.Close()
			req := contractRequest(server.URL)
			if tt.request != nil {
				req = tt.request(server.URL)
			}
			response, err := newHTTPTransport(server.Client(), nil).Do(context.Background(), req)
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantAnyErr && err == nil {
				t.Fatal("expected error")
			}
			if tt.wantErr == nil && !tt.wantAnyErr && err != nil {
				t.Fatal(err)
			}
			if response.SessionID != tt.wantSessionID {
				t.Fatalf("session = %q, want %q", response.SessionID, tt.wantSessionID)
			}
		})
	}
}

func TestTransportContractCancellationTimeoutAndDelivery(t *testing.T) {
	for _, tt := range []struct {
		name         string
		wrote        bool
		writeErr     error
		wantDelivery DeliveryState
	}{
		{name: "connect failure", wantDelivery: DeliveryNotSent},
		{name: "successful write then failure", wrote: true, wantDelivery: DeliverySent},
		{name: "partial write", wrote: true, writeErr: errors.New("partial"), wantDelivery: DeliveryUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if tt.wrote {
					notifyWroteRequest(req, tt.writeErr)
				}
				return nil, errors.New("network failed")
			})}
			_, err := newHTTPTransport(client, nil).Do(context.Background(), contractRequest("https://mcp.invalid"))
			var requestErr *RequestError
			if !errors.As(err, &requestErr) || requestErr.Delivery != tt.wantDelivery {
				t.Fatalf("error = %#v, want delivery %v", err, tt.wantDelivery)
			}
		})
	}

	for _, tt := range []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{name: "cancel", ctx: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, wantErr: context.Canceled},
		{name: "timeout", ctx: func() (context.Context, context.CancelFunc) { return context.WithTimeout(context.Background(), 10*time.Millisecond) }, wantErr: context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := tt.ctx()
			if tt.name == "cancel" {
				time.AfterFunc(10*time.Millisecond, cancel)
			} else {
				defer cancel()
			}
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				notifyWroteRequest(req, nil)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: &waitForContextBody{ctx: req.Context()}}, nil
			})}
			_, err := newHTTPTransport(client, nil).Do(ctx, contractRequest("https://mcp.invalid"))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestTransportContractRejectsProtocolIDsAndSignerOutputBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		notifyWroteRequest(req, nil)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":7,"result":{}}`)),
		}, nil
	})}

	for _, mutate := range []func(*TransportRequest){
		func(req *TransportRequest) { req.Session.ID = "bad\n-session" },
		func(req *TransportRequest) { req.LastEventID = strings.Repeat("x", maxLastEventIDBytes+1) },
	} {
		req := contractRequest("https://mcp.invalid")
		mutate(&req)
		if _, err := newHTTPTransport(client, nil).Do(context.Background(), req); err == nil {
			t.Fatal("expected protocol ID validation error")
		}
	}

	req := contractRequest("https://mcp.invalid")
	req.SignedContext = &SignedContextConfig{KeyID: "ctx_test"}
	oversized := staticSigner{token: strings.Repeat("x", maxSignedContextHeaderBytes+1)}
	if _, err := newHTTPTransport(client, oversized).Do(context.Background(), req); !errors.Is(err, ErrInvalidSignedContext) {
		t.Fatalf("oversized signer error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("dispatched %d invalid requests", calls.Load())
	}

	inbound := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		notifyWroteRequest(req, nil)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":   []string{"application/json"},
				"MCP-Session-Id": []string{"bad\n-session"},
			},
			Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":7,"result":{}}`)),
		}, nil
	})}
	if _, err := newHTTPTransport(inbound, nil).Do(context.Background(), contractRequest("https://mcp.invalid")); !errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("inbound session error = %v", err)
	}
}
```

Each body-limit assertion must use `errors.Is(err, ErrResponseTooLarge)`. Each cancellation/timeout assertion must use `errors.Is(err, context.Canceled)` or `errors.Is(err, context.DeadlineExceeded)`. Delivery assertions must use `errors.As(err, &requestErr)` and compare `requestErr.Delivery` to `DeliveryNotSent`, `DeliverySent`, or `DeliveryUnknown`; they must not compare error strings.

- [ ] **Step 4: Run the tests and record the expected red state**

Run: `cd backend && go test ./internal/infra/mcp -run 'Test(ClientTransportContractBeforeProtocolUpgrade|TransportContract)' -count=1`

Expected: FAIL because `Transport`, `DeliveryState`, bounded multi-event SSE consumption, and typed transport errors do not exist yet; the failure must occur while `protocolVersion` is still `2025-06-18`.

- [ ] **Step 5: Keep the red contract uncommitted for Task 2**

Run `git diff --check` and inspect the tests, but do not create an intentionally uncompilable commit. Task 2 commits these tests together with the minimum green transport implementation while the protocol version is still `2025-06-18`.

---

### Task 2: Introduce the bounded HTTP Transport and complete SSE consumer

**Files:**
- Create: `backend/internal/infra/mcp/protocol.go`
- Create: `backend/internal/infra/mcp/transport.go`
- Create: `backend/internal/infra/mcp/sse.go`
- Create: `backend/internal/infra/mcp/sse_test.go`
- Modify: `backend/internal/infra/mcp/client.go`
- Test: `backend/internal/infra/mcp/transport_test.go`
- Test: `backend/internal/infra/mcp/sse_test.go`

**Interfaces:**
- Consumes: phase-C `Client.contextSigner.Sign(TemplateContext, SignedContextConfig)` and A+B validated/rendered custom Headers.
- Produces: `Transport`, `TransportRequest`, `TransportResponse`, `RequestError`, `SSECursor`, `OperationKind`, and `DeliveryState`; Task 3 builds protocol operations only on these types.

- [ ] **Step 1: Define protocol values, bounded transport input/output, and typed errors**

Create `protocol.go` with these production declarations, retaining `protocolVersion = "2025-06-18"` until Task 3:

```go
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	protocolVersion         = "2025-06-18"
	maxResponseBytes  int64 = 8 << 20
	maxSSEEventBytes        = 1 << 20
	maxSSEEvents            = 1024
	maxSSEResumes           = 2
	maxToolListPages        = 128
	maxAccumulatedTools     = 10000
	maxSessionIDBytes       = 4096
	maxLastEventIDBytes     = 4096
)

var (
	ErrResponseTooLarge          = errors.New("mcp response exceeds limit")
	ErrSSEEventTooLarge          = errors.New("mcp sse event exceeds limit")
	ErrTooManySSEEvents          = errors.New("mcp sse event count exceeds limit")
	ErrSSEInterrupted            = errors.New("mcp sse stream interrupted")
	ErrUnsupportedContentType    = errors.New("mcp response content type is unsupported")
	ErrMismatchedResponseID      = errors.New("mcp response id does not match request")
	ErrUnsupportedServerRequest  = errors.New("mcp server request is unsupported")
	ErrSessionInvalid            = errors.New("mcp session is invalid")
	ErrInvalidSessionID          = errors.New("mcp session id is invalid")
	ErrInvalidLastEventID        = errors.New("mcp last event id is invalid")
	ErrPaginationCursorLoop      = errors.New("mcp tools list cursor repeated")
	ErrTooManyToolPages          = errors.New("mcp tools list page limit exceeded")
	ErrTooManyTools              = errors.New("mcp tools list result limit exceeded")
	ErrUnsupportedProtocolVersion = errors.New("mcp protocol version is unsupported")
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
	return fmt.Sprintf(
		"mcp request failed: operation=%d delivery=%d status=%d class=%s",
		e.Operation,
		e.Delivery,
		e.StatusCode,
		e.Class,
	)
}

func (e *RequestError) Unwrap() error { return e.cause }

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
```

- [ ] **Step 2: Implement a bounded SSE decoder that returns every complete event**

Create `sse.go`. `decodeSSE` must read line-by-line with `bufio.Reader`, support `\n`, `\r\n`, comments, repeated `data:` fields joined with `\n`, `event:`, and `id:`; dispatch on a blank line and dispatch the final event at clean EOF. Reject an `id` above 4096 bytes, invalid UTF-8, NUL/CR/LF, or `httpguts.ValidHeaderFieldValue == false` before retaining it for a future Header. Count bytes across every field of one event, return `ErrSSEEventTooLarge` above 1 MiB, and return `ErrTooManySSEEvents` after 1024 dispatched events.

```go
type sseEvent struct {
	Event string
	Data  []byte
	ID    string
	HasID bool
}

type SSECursor struct {
	LastEventID string
	Events      int
}

func decodeSSE(ctx context.Context, body io.Reader, consume func(sseEvent) (bool, error)) (SSECursor, error)
```

`HasID` distinguishes a missing `id:` field from an explicit empty `id:`. A present non-empty ID replaces the cursor; an explicit empty ID clears it, so no subsequent resume sends `Last-Event-ID`. `consume` returning `true` stops only after a matching JSON-RPC response has been decoded. A clean EOF or non-context I/O interruption before `true` returns `ErrSSEInterrupted` together with the current cursor. Context cancellation/deadline, size/count, invalid-ID, and protocol/decode errors return their own error and are never resumable.

- [ ] **Step 3: Implement HTTP dispatch with exclusive protocol Header ownership and delivery tracking**

Create `transport.go` with concrete `httpTransport`:

```go
type httpTransport struct {
	client        *http.Client
	contextSigner ContextSigner
}

func newHTTPTransport(client *http.Client, signer ContextSigner) Transport {
	return &httpTransport{client: client, contextSigner: signer}
}
```

In `Do`, clone `CustomHeaders`; call the A+B `ValidateRenderedCustomHeaders` function again; validate any outbound session ID and Last-Event-ID against their protocol-specific 4096-byte/syntax rules before `Header.Set`; then set transport-owned Headers in this order: `Authorization: Bearer <AuthToken>` when non-empty, POST-only `Content-Type`, `Accept`, negotiated `MCP-Protocol-Version`, `MCP-Session-Id`, `Last-Event-ID`, and finally the newly signed `X-DEEIX-Context`. Never accept these values from `CustomHeaders`, and never copy the authentication fingerprint into a request.

Attach `httptrace.ClientTrace.WroteRequest` to the request context and classify conservatively. Validation, signing, marshaling, or request-construction failures before `client.Do` are `DeliveryNotSent`. If `client.Do` fails before the callback ever runs, classify `DeliveryNotSent`; a callback with `WroteRequestInfo.Err == nil` is `DeliverySent`; a callback reporting a write error is `DeliveryUnknown` because a prefix may have left the process. Any received HTTP response is `DeliverySent`. Map 404 with a non-empty session ID with the complete call `newRequestError(req.Operation, DeliverySent, resp.StatusCode, ClientErrorProtocol, ErrSessionInvalid)`. Limit the body with `io.LimitedReader{N: maxResponseBytes + 1}` and distinguish a clean bounded EOF from a truncated body.

Reuse A+B `ClientErrorKind`/`SafeErrorSummary`. `RequestError.Error()` never formats its cause, URL, response/SSE payload, or remote message. Add a fixture with a secret in endpoint/query, remote JSON-RPC message, and SSE data; it must have zero matches in returned errors, traces, system events, and `LastError`.

Capture and validate `MCP-Session-Id` in transport before decoding the response body or returning it to either the pre-upgrade compatibility client or Task 3 operation. When later JSON/content/protocol decoding fails, return `TransportResponse{SessionID: validID}` together with the error so operation cleanup can DELETE it. Never discard validated response metadata merely because body parsing failed, and never return an invalid ID for later Header use.

Call the signer exactly as follows immediately before `client.Do` for POST, GET, and DELETE:

```go
if request.SignedContext != nil {
	if t.contextSigner == nil {
		return TransportResponse{}, newRequestError(
			request.Operation,
			DeliveryNotSent,
			0,
			ClientErrorProtocol,
			ErrContextSignerUnavailable,
		)
	}
	token, err := t.contextSigner.Sign(
		request.TemplateContext,
		*request.SignedContext,
	)
	if err != nil {
		return TransportResponse{}, newRequestError(
			request.Operation,
			DeliveryNotSent,
			0,
			ClientErrorProtocol,
			err,
		)
	}
	if len(token) > maxSignedContextHeaderBytes || !httpguts.ValidHeaderFieldValue(token) {
		return TransportResponse{}, newRequestError(
			request.Operation,
			DeliveryNotSent,
			0,
			ClientErrorProtocol,
			ErrInvalidSignedContext,
		)
	}
	req.Header.Set("X-DEEIX-Context", token)
}
```

Nil `SignedContext` means disabled signing. A non-nil config with a nil signer returns `ErrContextSignerUnavailable`; a non-nil but incomplete config is a signer error with `DeliveryNotSent`. Transport also rechecks the phase-C 8192-byte/Header-value bound before `Header.Set`. Every failure occurs before HTTP dispatch; transport must not infer a second disabled state from an empty secret or key ID.

- [ ] **Step 4: Consume JSON and SSE by matching the JSON-RPC request ID**

For a successful notification POST or DELETE with an empty `RequestID`, accept an empty 2xx body without requiring JSON. For `application/json`, decode exactly one `rpcMessage` plus EOF, require `jsonrpc == "2.0"`, reject an unexpected server request (`Method != "" && ID != nil`) with `ErrUnsupportedServerRequest`, and require the response ID to equal `TransportRequest.RequestID` using normalized `json.RawMessage` bytes.

For `text/event-stream`, treat an omitted event type as `message`; feed events where `Event == "" || Event == "message"` to the same message validator. Ignore notifications (`Method != "" && len(ID) == 0`) because the client advertises no notification-dependent capability; stop only on the matching response. Do not expose payload bytes in returned errors.

Refactor the existing `Client.initialize`/`rpcWithSession` path to build `TransportRequest` and delegate every physical request to this transport. Keep the current per-operation initialize/list-or-call/DELETE lifecycle and `protocolVersion = "2025-06-18"` in this task; do not retain a second HTTP/Header/SSE implementation in `client.go`.

- [ ] **Step 5: Run focused transport and SSE tests**

Run: `cd backend && go test ./internal/infra/mcp -run 'Test(ClientTransportContractBeforeProtocolUpgrade|TransportContract|DecodeSSE)' -count=1`

Expected: PASS with `protocolVersion` still equal to `2025-06-18`; oversized, cancellation, timeout, delivery-state, multiline, notification-before-response, and mismatched-ID tests all pass.

- [ ] **Step 6: Commit the transport implementation**

```bash
git add backend/internal/infra/mcp/protocol.go backend/internal/infra/mcp/transport.go backend/internal/infra/mcp/sse.go backend/internal/infra/mcp/sse_test.go backend/internal/infra/mcp/client.go backend/internal/infra/mcp/transport_test.go backend/internal/infra/mcp/client_test.go
git commit -m "refactor: isolate bounded mcp transport"
```

Expected: commit succeeds, includes the Task 1 contract tests plus Task 2 production, remains green, and `git show --check --oneline HEAD` reports no whitespace errors.

---

### Task 3: Upgrade to MCP 2025-11-25 with Operation, SSE resume, and cursor pagination

**Files:**
- Create: `backend/internal/infra/mcp/operation.go`
- Create: `backend/internal/infra/mcp/operation_test.go`
- Create: `backend/internal/infra/mcp/retry.go`
- Create: `backend/internal/infra/mcp/retry_test.go`
- Modify: `backend/internal/infra/mcp/protocol.go`
- Modify: `backend/internal/infra/mcp/client.go`
- Modify: `backend/internal/infra/mcp/client_test.go`
- Test: `backend/internal/infra/mcp/operation_test.go`
- Test: `backend/internal/infra/mcp/transport_test.go`

**Interfaces:**
- Consumes: Task 2 `Transport.Do`, `TransportRequest`, `TransportResponse`, typed limits/errors, and phase-C per-request signer boundary.
- Produces: `Operation`, `operation`, `RetryPolicy`, 2025-11-25 initialize/initialized state, resumable response consumption, paginated `ListTools`, non-replayed `CallTool`, and bounded DELETE.

- [ ] **Step 1: Write failing 2025-11-25 lifecycle and version-negotiation tests**

Create `operation_test.go` with this deterministic transport. Every step asserts the operation kind, HTTP method, JSON-RPC method, session, protocol version, and resume cursor before returning a response. A nil response ID is replaced with the actual request ID so tests never hard-code the operation's atomic counter:

```go
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
```

Run: `cd backend && go test ./internal/infra/mcp -run 'TestOperation(Lifecycle|RejectsNegotiation|MalformedInitialize|Initialized404)' -count=1`

Expected: FAIL because `operation`, `newOperation`, and `terminate` do not exist and `protocolVersion` remains `2025-06-18`. Do not edit production until this exact red state is observed.

- [ ] **Step 2: Define the Operation interface and implement the 2025-11-25 state machine**

Create `operation.go` with:

```go
type Operation interface {
	ListTools(context.Context) ([]Tool, error)
	CallTool(context.Context, CallInput) (string, error)
}

type operation struct {
	transport     Transport
	config        CallConfig
	signedContext *SignedContextConfig
	session       sessionState
	nextID        atomic.Int64
	retryPolicy   RetryPolicy
	gate          chan struct{}
}
```

Create `retry.go` in the same step so `operation.go` has no forward dependency on Task 5:

```go
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
```

Add `newOperation(transport Transport, cfg CallConfig, retryCount int) (*operation, error)`. It validates and deep-copies `cfg.CustomHeaders`, `cfg.Context`, and optional `cfg.SignedContext`, installs `ClassifiedRetryPolicy`, creates `gate := make(chan struct{}, 1)`, and performs no network I/O. The first `ListTools` or `CallTool` lazily initializes the session.

Every public operation and termination acquires the gate with `select { case gate <- struct{}{}: case <-ctx.Done(): }` and releases it with `<-gate`; a canceled waiter exits immediately without waiting behind an in-flight call. `initialize` sends protocol `2025-11-25` and requires the same version in `InitializeResult`. Before storing a non-empty server-issued session ID, require at most 4096 bytes, visible ASCII bytes `0x21..0x7e`, and `httpguts.ValidHeaderFieldValue`; otherwise fail and never copy it to a request Header. Then send `notifications/initialized`. All later POST/GET/DELETE requests use the stored negotiated version. The gate covers initialize/rebuild plus the complete list/call/terminate operation, intentionally serializing one session.

Only now change `protocolVersion` in `protocol.go`:

```go
const protocolVersion = "2025-11-25"
```

- [ ] **Step 3: Add multi-event SSE consumption and bounded Last-Event-ID recovery**

Task 2 already proves multi-event decoding, invalid/oversized IDs, and explicit empty `id:` cursor clearing at the transport boundary. Add this operation-level table to prove only a valid retained cursor and assigned session produce GET resume, at most two GETs occur, and an ambiguous `tools/call` POST is never replayed:

```go
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
			for _, req := range transport.seen {
				switch req.Operation {
				case OperationCallTool:
					calls++
				case OperationResumeSSE:
					resumes++
				}
			}
			if calls != tt.wantCalls || resumes != tt.wantResumes {
				t.Fatalf("calls/resumes = %d/%d, want %d/%d", calls, resumes, tt.wantCalls, tt.wantResumes)
			}
		})
	}
}
```

Run: `cd backend && go test ./internal/infra/mcp -run TestOperationSSEResumePolicy -count=1`

Expected: FAIL because Task 2 returns `ErrSSEInterrupted` metadata but `operation` does not yet issue bounded GET resumes. Do not implement resume before observing this failure.

- [ ] **Step 4: Write failing `tools/list` cursor, loop, limit, and restart tests**

Add this functional transport and table to `operation_test.go`:

```go
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
			name: "tool limit",
			list: func(int, string) (json.RawMessage, error) { return listPage(tooMany, ""), nil },
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
			name: "404 with zero budget",
			list: func(int, string) (json.RawMessage, error) { return nil, invalidSession },
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

func TestClassifiedRetryPolicyMatrix(t *testing.T) {
	transient := newRequestError(OperationListTools, DeliveryNotSent, 0, ClientErrorNetwork, errors.New("connect failed"))
	listInvalid := newRequestError(OperationListTools, DeliverySent, http.StatusNotFound, ClientErrorProtocol, ErrSessionInvalid)
	initializedInvalid := newRequestError(OperationInitialized, DeliverySent, http.StatusNotFound, ClientErrorProtocol, ErrSessionInvalid)
	callNotSentInvalid := newRequestError(OperationCallTool, DeliveryNotSent, 0, ClientErrorProtocol, ErrSessionInvalid)
	callSent := newRequestError(OperationCallTool, DeliverySent, http.StatusBadGateway, ClientErrorHTTP, errors.New("remote failed"))
	for _, tt := range []struct {
		name string
		in   RetryInput
		want RetryDecision
	}{
		{name: "transient not sent", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: transient}, want: RetrySameSession},
		{name: "list invalid", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 1, Delivery: DeliverySent, Err: listInvalid}, want: RetryNewSession},
		{name: "initialized invalid", in: RetryInput{Operation: OperationInitialized, Attempt: 0, Budget: 1, Delivery: DeliverySent, Err: initializedInvalid}, want: RetryNewSession},
		{name: "call not sent invalid", in: RetryInput{Operation: OperationCallTool, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: callNotSentInvalid}, want: RetryNewSession},
		{name: "call sent", in: RetryInput{Operation: OperationCallTool, Attempt: 0, Budget: 1, Delivery: DeliverySent, Err: callSent}, want: RetryStop},
		{name: "context canceled", in: RetryInput{Operation: OperationListTools, Attempt: 0, Budget: 1, Delivery: DeliveryNotSent, Err: context.Canceled}, want: RetryStop},
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
```

Run: `cd backend && go test ./internal/infra/mcp -run 'Test(OperationListToolsPaginationContracts|ClassifiedRetryPolicyMatrix)' -count=1`

Expected: FAIL on the first missing cursor/page/restart behavior. The zero-budget row proves no hidden mandatory retry exists.

- [ ] **Step 5: Implement SSE resume, pagination, classified retry, and bounded cleanup minimally**

Add this payload type and implement the loops exercised above:

```go
type listToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}
```

On `ErrSSEInterrupted`, resume only when both `session.ID` and `LastEventID` are non-empty and fewer than two resumes were attempted. Build a GET `TransportRequest{Operation: OperationResumeSSE, HTTPMethod: http.MethodGet, LastEventID: cursor}`. Preserve the original JSON-RPC request ID and never marshal or resend the original POST. A resume 404 invalidates the in-memory session: an original `tools/call` returns without another call POST, while an original idempotent `tools/list` may consume retry budget and restart from page one.

For `ListTools`, send an empty params object first and `map[string]string{"cursor": cursor}` thereafter. Track non-empty cursors, reject a repeat with `ErrPaginationCursorLoop`, reject before page 129 with `ErrTooManyToolPages`, and reject before accumulated tool 10001 with `ErrTooManyTools`. On a budgeted session restart, clear accumulator/cursor state and begin at page one; otherwise return the original error.

`CallTool` may create a new session before its first send. After a `tools/call` POST is `DeliverySent` or `DeliveryUnknown`, return the error unchanged and invalidate a 404 session; never issue a second POST. A transient `DeliveryNotSent` transport error may retry on the same session within budget. A new session is allowed for a call only when the operation already knows the old session is invalid and the call itself is still `DeliveryNotSent`.

DELETE uses a cleanup context capped at 5 seconds. Treat 2xx, 404, and 405 as closed; always clear local state even if DELETE fails. Do not retry DELETE.

Implement `ClassifiedRetryPolicy.Decide` now and add a table row in `retry_test.go` for every `(OperationKind, DeliveryState, error class)` used by the operation tests:

- `RetryStop`: canceled/deadlined context, exhausted budget, deterministic validation/signing/config/TLS-policy failure, JSON-RPC/tool-result error, every DELETE, and every `tools/call` with `DeliverySent` or `DeliveryUnknown`;
- `RetrySameSession`: genuinely transient connect/write failure with `DeliveryNotSent`;
- `RetryNewSession`: `ErrSessionInvalid` at initialize, `notifications/initialized`, or list boundaries, or `ErrSessionInvalid` for a `tools/call` whose call POST is provably `DeliveryNotSent`.

No generic `DeliveryNotSent` branch selects both same and new session. Before assigning a different non-empty session ID, DELETE the prior ID unless the server has already returned session-invalid/404; never overwrite a possibly valid ID and lose its cleanup handle. GET SSE resume uses the separate fixed `maxSSEResumes` because it continues the original response and never replays the POST; it does not consume `MCPToolRetryCount`.

- [ ] **Step 6: Run the complete infra MCP package green and commit**

Run: `cd backend && go test ./internal/infra/mcp -count=1`

Expected: PASS; request capture shows `2025-11-25`, paginated lists, GET-only SSE resume, and no ambiguous `tools/call` replay.

```bash
git add backend/internal/infra/mcp/protocol.go backend/internal/infra/mcp/operation.go backend/internal/infra/mcp/operation_test.go backend/internal/infra/mcp/retry.go backend/internal/infra/mcp/retry_test.go backend/internal/infra/mcp/client.go backend/internal/infra/mcp/client_test.go backend/internal/infra/mcp/transport_test.go
git commit -m "feat: upgrade mcp protocol operations"
```

Expected: commit succeeds with the version upgrade and operation tests in the same reviewable change.

---

### Task 4: Add immutable run/server SessionManager and deterministic cleanup

**Files:**
- Create: `backend/internal/infra/mcp/session_manager.go`
- Create: `backend/internal/infra/mcp/session_manager_test.go`
- Modify: `backend/internal/infra/mcp/client.go`
- Test: `backend/internal/infra/mcp/session_manager_test.go`

**Interfaces:**
- Consumes: Task 3 `operation`, immutable A+B `TemplateContext`/rendered Headers, phase-C `SignedContextConfig`, and classified `RetryPolicy` input.
- Produces: exported `SessionKey`, `AcquireInput`, `SessionManager`; concrete `runSessionManager`; per-key serialization; `CloseRun` and `CloseAll` cleanup.

- [ ] **Step 1: Write failing key, reuse, isolation, serialization, and cleanup tests**

Create `session_manager_test.go` with this protocol-aware fake. It lazily assigns a distinct session during initialize, blocks only `tools/call` on an explicit barrier, and records DELETE state without sleeping:

```go
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type managerTransport struct {
	sequence           atomic.Int64
	terminations       atomic.Int32
	cleanupSawCanceled atomic.Bool
	cleanupStatus      int
	cleanupErr         error
	entered            chan string
	release            chan struct{}
	blockCalls         bool
}

func (t *managerTransport) Do(ctx context.Context, req TransportRequest) (TransportResponse, error) {
	switch req.Operation {
	case OperationInitialize:
		sessionID := fmt.Sprintf("session-%d", t.sequence.Add(1))
		result, _ := json.Marshal(map[string]interface{}{
			"protocolVersion": "2025-11-25",
			"capabilities": map[string]interface{}{},
			"serverInfo": map[string]string{"name": "test", "version": "1"},
		})
		return TransportResponse{SessionID: sessionID, Message: rpcMessage{JSONRPC: "2.0", ID: req.RequestID, Result: result}}, nil
	case OperationInitialized:
		return TransportResponse{}, nil
	case OperationListTools:
		return TransportResponse{Message: rpcMessage{JSONRPC: "2.0", ID: req.RequestID, Result: json.RawMessage(`{"tools":[]}`)}}, nil
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
		return TransportResponse{Message: rpcMessage{JSONRPC: "2.0", ID: req.RequestID, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}}, nil
	case OperationTerminate:
		t.terminations.Add(1)
		if ctx.Err() != nil {
			t.cleanupSawCanceled.Store(true)
		}
		if t.cleanupErr != nil {
			return TransportResponse{}, t.cleanupErr
		}
		if t.cleanupStatus != 0 {
			return TransportResponse{}, newRequestError(OperationTerminate, DeliverySent, t.cleanupStatus, ClientErrorHTTP, errors.New("cleanup status"))
		}
		return TransportResponse{}, nil
	default:
		return TransportResponse{}, fmt.Errorf("unexpected operation %d", req.Operation)
	}
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
	clone.CallConfig.CustomHeaders = make(map[string]string, len(input.CallConfig.CustomHeaders))
	for key, value := range input.CallConfig.CustomHeaders {
		clone.CallConfig.CustomHeaders[key] = value
	}
	if input.CallConfig.SignedContext != nil {
		signed := *input.CallConfig.SignedContext
		clone.CallConfig.SignedContext = &signed
	}
	return clone
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
		{name: "signed config", mutate: func(in *AcquireInput) { in.CallConfig.SignedContext = &SignedContextConfig{Secret: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY", Issuer: "https://deeix.example.test", Audience: "urn:deeix:mcp:test", KeyID: "ctx_test", ExpiresSeconds: 300} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := cloneAcquireInput(base)
			tt.mutate(&input)
			got, err := manager.Acquire(context.Background(), input)
			if err != nil {
				t.Fatal(err)
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

func TestSessionManagerSerializesSameSessionAndAllowsDifferentSessions(t *testing.T) {
	t.Run("canceled waiter cannot enter same session", func(t *testing.T) {
		transport := &managerTransport{blockCalls: true, entered: make(chan string, 2), release: make(chan struct{})}
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
		transport := &managerTransport{blockCalls: true, entered: make(chan string, 2), release: make(chan struct{})}
		manager := newRunSessionManager(transport)
		first, _ := manager.Acquire(context.Background(), managerAcquireInput(1, "user-1", "run-1"))
		second, _ := manager.Acquire(context.Background(), managerAcquireInput(2, "user-1", "run-1"))
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
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	})
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
				t.Fatalf("entries=%d terminations=%d canceled=%v", len(manager.entries), transport.terminations.Load(), transport.cleanupSawCanceled.Load())
			}
		})
	}

	transport := &managerTransport{}
	manager := newRunSessionManager(transport)
	runOp, _ := manager.Acquire(context.Background(), managerAcquireInput(1, "user-1", "run-1"))
	_, _ = runOp.ListTools(context.Background())
	ephemeral, closeEphemeral, err := manager.OpenEphemeral(context.Background(), testCallConfig(), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ephemeral.ListTools(context.Background())
	if err = manager.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = closeEphemeral(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(manager.entries) != 0 || len(manager.ephemeral) != 0 || transport.terminations.Load() != 2 {
		t.Fatalf("entries=%d ephemeral=%d terminations=%d", len(manager.entries), len(manager.ephemeral), transport.terminations.Load())
	}
	if _, err = manager.Acquire(context.Background(), managerAcquireInput(2, "user-1", "run-2")); !errors.Is(err, ErrSessionManagerClosed) {
		t.Fatalf("Acquire after CloseAll = %v", err)
	}
	if _, _, err = manager.OpenEphemeral(context.Background(), testCallConfig(), 0); !errors.Is(err, ErrSessionManagerClosed) {
		t.Fatalf("OpenEphemeral after CloseAll = %v", err)
	}
}

func TestSessionManagerCloseAllRacesRegistration(t *testing.T) {
	manager := newRunSessionManager(&managerTransport{})
	start := make(chan struct{})
	var wg sync.WaitGroup
	var unexpected atomic.Int32
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				_, err := manager.Acquire(context.Background(), managerAcquireInput(uint(i+1), "user-1", fmt.Sprintf("run-%d", i)))
				if err != nil && !errors.Is(err, ErrSessionManagerClosed) {
					unexpected.Add(1)
				}
				return
			}
			_, closeOperation, err := manager.OpenEphemeral(context.Background(), testCallConfig(), 0)
			if err == nil {
				_ = closeOperation(context.Background())
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
		t.Fatalf("unexpected=%d entries=%d ephemeral=%d", unexpected.Load(), len(manager.entries), len(manager.ephemeral))
	}
}
```

Run: `cd backend && go test ./internal/infra/mcp -run TestSessionManager -count=1`

Expected: FAIL to compile because `AcquireInput`, `SessionManager`, `BuildSessionKey`, and `newRunSessionManager` do not exist. Do not add manager production code before this red result.

- [ ] **Step 2: Implement the deterministic key and immutable manager minimally**

Create `session_manager.go` with imports `context`, `crypto/sha256`, `encoding/binary`, `encoding/hex`, `errors`, `net/url`, `reflect`, `strconv`, `strings`, `sync`, and `time`, then add these declarations and snapshots:

```go
var (
	ErrInvalidSessionKey      = errors.New("mcp session key is invalid")
	ErrSessionContextMismatch = errors.New("mcp session context does not match cached session")
	ErrSessionManagerClosed   = errors.New("mcp session manager is closed")
)

type SessionKey struct {
	ServerID      uint
	UserPublicID  string
	RunID         string
	AuthIdentity  string
	ConfigVersion string
}

type AcquireInput struct {
	ServerID        uint
	ServerUpdatedAt time.Time
	CallConfig      CallConfig
	RetryCount      int
}

type ConfigurationVersionInput struct {
	ServerUpdatedAt time.Time
	Endpoint        string
	TimeoutMS       int
	RetryCount      int
	SignedContext   *SignedContextConfig
}

type SessionManager interface {
	Acquire(context.Context, AcquireInput) (Operation, error)
	OpenEphemeral(context.Context, CallConfig, int) (Operation, func(context.Context) error, error)
	CloseRun(context.Context, string, string) error
	CloseAll(context.Context) error
}

type runSessionManager struct {
	transport     Transport
	mu            sync.Mutex
	entries       map[SessionKey]*managedSession
	ephemeral     map[uint64]*managedSession
	nextEphemeral uint64
	closed        bool
}

type managedSession struct {
	operation  *operation
	config     CallConfig
	retryCount int
	closeOnce  sync.Once
	closeErr   error
}
```

Use a length-prefixed digest so field concatenation cannot collide:

```go
func AuthenticationIdentity(authToken string) string {
	sum := sha256.Sum256([]byte("bearer\x00" + authToken))
	return hex.EncodeToString(sum[:])
}

func ConfigurationVersion(input ConfigurationVersionInput) string {
	hash := sha256.New()
	write := func(value string) {
		_ = binary.Write(hash, binary.BigEndian, uint64(len(value)))
		_, _ = hash.Write([]byte(value))
	}
	write(input.ServerUpdatedAt.UTC().Format(time.RFC3339Nano))
	write(strings.TrimSpace(input.Endpoint))
	write(strconv.Itoa(input.TimeoutMS))
	write(strconv.Itoa(input.RetryCount))
	if input.SignedContext == nil {
		write("none")
	} else {
		write("signed")
		write(input.SignedContext.Secret)
		write(input.SignedContext.Issuer)
		write(input.SignedContext.Audience)
		write(input.SignedContext.KeyID)
		write(strconv.Itoa(input.SignedContext.ExpiresSeconds))
		write(strconv.FormatBool(input.SignedContext.IncludeName))
		write(strconv.FormatBool(input.SignedContext.IncludeEmail))
		write(strconv.FormatBool(input.SignedContext.IncludeRole))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func normalizeSessionEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidSessionKey
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String(), nil
}

func BuildSessionKey(input AcquireInput) (SessionKey, error) {
	ctx := input.CallConfig.Context
	endpoint, err := normalizeSessionEndpoint(input.CallConfig.BaseURL)
	if input.ServerID == 0 || input.ServerUpdatedAt.IsZero() || ctx.Mode != ContextModeChat ||
		strings.TrimSpace(ctx.UserPublicID) == "" || strings.TrimSpace(ctx.RunID) == "" || err != nil ||
		input.CallConfig.TimeoutMS <= 0 || input.RetryCount < 0 {
		return SessionKey{}, ErrInvalidSessionKey
	}
	return SessionKey{
		ServerID:     input.ServerID,
		UserPublicID: strings.TrimSpace(ctx.UserPublicID),
		RunID:        strings.TrimSpace(ctx.RunID),
		AuthIdentity: AuthenticationIdentity(input.CallConfig.AuthToken),
		ConfigVersion: ConfigurationVersion(ConfigurationVersionInput{
			ServerUpdatedAt: input.ServerUpdatedAt,
			Endpoint:        endpoint,
			TimeoutMS:       input.CallConfig.TimeoutMS,
			RetryCount:      input.RetryCount,
			SignedContext:   input.CallConfig.SignedContext,
		}),
	}, nil
}
```

Do not add `TemplateContext` or rendered `CustomHeaders` to `ConfigurationVersion`; exact cache-hit equality below owns those values. Add the manager methods exactly as follows:

```go
func NewSessionManager(client *Client) SessionManager {
	return newRunSessionManager(client.transport)
}

func newRunSessionManager(transport Transport) *runSessionManager {
	return &runSessionManager{
		transport: transport,
		entries:   make(map[SessionKey]*managedSession),
		ephemeral: make(map[uint64]*managedSession),
	}
}

func cloneManagerCallConfig(input CallConfig) (CallConfig, error) {
	if err := ValidateRenderedCustomHeaders(input.CustomHeaders); err != nil {
		return CallConfig{}, err
	}
	clone := input
	clone.CustomHeaders = make(map[string]string, len(input.CustomHeaders))
	for key, value := range input.CustomHeaders {
		clone.CustomHeaders[key] = value
	}
	if input.SignedContext != nil {
		signed := *input.SignedContext
		clone.SignedContext = &signed
	}
	return clone, nil
}

func sameManagerConfig(left CallConfig, right CallConfig, leftRetry int, rightRetry int) bool {
	return left.BaseURL == right.BaseURL && left.AuthToken == right.AuthToken && left.TimeoutMS == right.TimeoutMS &&
		leftRetry == rightRetry && reflect.DeepEqual(left.CustomHeaders, right.CustomHeaders) &&
		reflect.DeepEqual(left.Context, right.Context) && reflect.DeepEqual(left.SignedContext, right.SignedContext)
}

func (m *runSessionManager) Acquire(_ context.Context, input AcquireInput) (Operation, error) {
	snapshot, err := cloneManagerCallConfig(input.CallConfig)
	if err != nil {
		return nil, err
	}
	input.CallConfig = snapshot
	key, err := BuildSessionKey(input)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrSessionManagerClosed
	}
	if existing := m.entries[key]; existing != nil {
		if !sameManagerConfig(existing.config, snapshot, existing.retryCount, input.RetryCount) {
			return nil, ErrSessionContextMismatch
		}
		return existing.operation, nil
	}
	op, err := newOperation(m.transport, snapshot, input.RetryCount)
	if err != nil {
		return nil, err
	}
	m.entries[key] = &managedSession{operation: op, config: snapshot, retryCount: input.RetryCount}
	return op, nil
}

func (m *runSessionManager) OpenEphemeral(_ context.Context, cfg CallConfig, retryCount int) (Operation, func(context.Context) error, error) {
	snapshot, err := cloneManagerCallConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	op, err := newOperation(m.transport, snapshot, retryCount)
	if err != nil {
		return nil, nil, err
	}
	entry := &managedSession{operation: op, config: snapshot, retryCount: retryCount}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, ErrSessionManagerClosed
	}
	m.nextEphemeral++
	id := m.nextEphemeral
	m.ephemeral[id] = entry
	m.mu.Unlock()
	closeOperation := func(ctx context.Context) error {
		m.mu.Lock()
		delete(m.ephemeral, id)
		m.mu.Unlock()
		return closeManagedSession(ctx, entry)
	}
	return op, closeOperation, nil
}

func closeManagedSession(ctx context.Context, entry *managedSession) error {
	entry.closeOnce.Do(func() { entry.closeErr = entry.operation.terminate(ctx) })
	return entry.closeErr
}

func closeManagedSessions(ctx context.Context, entries []*managedSession) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	errs := make(chan error, len(entries))
	var wg sync.WaitGroup
	for _, entry := range entries {
		wg.Add(1)
		go func(entry *managedSession) {
			defer wg.Done()
			if err := closeManagedSession(cleanupCtx, entry); err != nil {
				errs <- err
			}
		}(entry)
	}
	wg.Wait()
	close(errs)
	joined := make([]error, 0, len(errs))
	for err := range errs {
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}

func (m *runSessionManager) CloseRun(ctx context.Context, userPublicID string, runID string) error {
	m.mu.Lock()
	entries := make([]*managedSession, 0)
	for key, entry := range m.entries {
		if key.UserPublicID == userPublicID && key.RunID == runID {
			entries = append(entries, entry)
			delete(m.entries, key)
		}
	}
	m.mu.Unlock()
	return closeManagedSessions(ctx, entries)
}

func (m *runSessionManager) CloseAll(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	entries := make([]*managedSession, 0, len(m.entries)+len(m.ephemeral))
	for _, entry := range m.entries {
		entries = append(entries, entry)
	}
	for _, entry := range m.ephemeral {
		entries = append(entries, entry)
	}
	m.entries = make(map[SessionKey]*managedSession)
	m.ephemeral = make(map[uint64]*managedSession)
	m.mu.Unlock()
	return closeManagedSessions(ctx, entries)
}
```

Task 2 must leave the exact constructed `Transport` on `Client.transport`; the public compatibility methods continue to create/close their own non-cached `operation` and never enter `entries`.

- [ ] **Step 3: Run the manager tests green under the race detector and commit**

Run: `cd backend && go test -race ./internal/infra/mcp -run 'TestSessionManager' -count=1`

Expected: PASS with no race report; exact keys reuse, context/Header mutations fail closed, a canceled same-session waiter never reaches transport, different sessions cross the barrier, cleanup ignores caller cancellation, and `CloseAll` is terminal.

```bash
git add backend/internal/infra/mcp/session_manager.go backend/internal/infra/mcp/session_manager_test.go backend/internal/infra/mcp/client.go
git commit -m "feat: add run scoped mcp sessions"
```

Expected: commit succeeds and contains no database, transport HTTP API, or frontend file.

---

### Task 5: Replace blanket tool retries and wire run lifecycle into application services

**Files:**
- Modify: `backend/internal/infra/mcp/retry.go`
- Modify: `backend/internal/infra/mcp/retry_test.go`
- Modify: `backend/internal/application/conversation/service.go`
- Modify: `backend/internal/application/conversation/service_mcp_tools.go`
- Modify: `backend/internal/application/conversation/service_tool.go`
- Modify: `backend/internal/application/conversation/service_tool_execution.go`
- Modify: `backend/internal/application/conversation/service_message_send.go`
- Modify: `backend/internal/application/conversation/service_run.go`
- Modify: `backend/internal/application/conversation/service_mcp_tools_test.go`
- Modify: `backend/internal/application/conversation/service_tool_test.go`
- Create: `backend/internal/application/conversation/service_mcp_lifecycle_test.go`
- Create: `backend/internal/application/conversation/service_run_mcp_test.go`
- Modify: `backend/internal/application/mcp/service.go`
- Create: `backend/internal/application/mcp/service_lifecycle_test.go`
- Modify: `backend/internal/app/app.go`
- Test: `backend/internal/infra/mcp/retry_test.go`
- Test: `backend/internal/application/conversation/service_mcp_lifecycle_test.go`
- Test: `backend/internal/application/conversation/service_run_mcp_test.go`
- Test: `backend/internal/application/mcp/service_lifecycle_test.go`

**Interfaces:**
- Consumes: Task 4 `SessionManager.Acquire/OpenEphemeral/CloseRun/CloseAll`, A+B authoritative public-ID `TemplateContext`, the complete built `CallConfig`, `domainmcp.Server.UpdatedAt`, and existing `MCPToolRetryCount`.
- Produces: application wiring for the existing `RetryPolicy.Decide`, run-scoped operation lookup by selected tool, finalization-path cleanup, and ephemeral sync cleanup without an API or schema change.

- [ ] **Step 1: Freeze the runtime retry budget mapping**

Extend `retry_test.go` with boundary cases for runtime budgets `0`, `1`, and `5`, matching the existing settings range. Assert that `MCPToolRetryCount` is only a maximum additional-attempt budget consumed inside `operation`; it never changes idempotency classification, and no application layer retries a returned `Operation.CallTool` error. No rule retries DELETE.

- [ ] **Step 2: Remove `callMCPWithRetry` and make one Operation responsible for classified attempts**

Delete the time-based blanket loop from `service_tool.go`. `executeToolCall` must resolve an `Operation` from the selected-tool runtime and call it once:

```go
type ExecuteToolInput struct {
	ToolName      string
	ArgumentsJSON string
	Operation     mcp.Operation
}

func (s *Service) executeToolCall(ctx context.Context, input ExecuteToolInput) (string, error) {
	toolName := strings.TrimSpace(input.ToolName)
	if toolName == "" {
		return "", fmt.Errorf("tool name is required")
	}
	if input.Operation == nil {
		return "", fmt.Errorf("tool %s is not enabled for this run", toolName)
	}
	cfg := s.cfg.Snapshot()
	limit := cfg.MCPMaxConcurrentCalls
	if limit <= 0 {
		limit = 8
	}
	return s.executeWithToolLimiter(ctx, limit, func() (string, error) {
		return input.Operation.CallTool(ctx, mcp.CallInput{
			ToolName:      toolName,
			ArgumentsJSON: strings.TrimSpace(input.ArgumentsJSON),
		})
	})
}
```

Move retry handling entirely into `operation`, where delivery state is available. Keep `MCPToolRetryCount` as `AcquireInput.RetryCount`.

- [ ] **Step 3: Build one operation per selected run/server and reuse it across tool names**

Change `selectedToolRuntime` to hold `map[string]mcp.Operation` by model tool name and a private `map[uint]mcp.Operation` by Server ID while resolving tools. For each first tool on a server, build:

```go
operation, err := s.mcpSessions.Acquire(ctx, mcp.AcquireInput{
	ServerID:        server.ID,
	ServerUpdatedAt: server.UpdatedAt,
	CallConfig:      callConfig,
	RetryCount:      cfg.MCPToolRetryCount,
})
```

Assign the same `operation` to every selected tool from that Server. A second Server receives a distinct operation even if its URL string is identical.

- [ ] **Step 4: Bind the authoritative MCP identity and release after local run finalization**

Add `mcpSessions mcp.SessionManager` to `conversation.Service` and constructor injection in `service.go`. Add an unexported `mcpUserPublicID string` field and `bindMCPContext(TemplateContext)` method to `messageSendRunState`. Immediately after A+B builds the authoritative chat `TemplateContext` in `service_message_send.go`, bind it to `runState`; do not reconstruct a public ID from a numeric user ID.

At the end of `messageSendRunState.finalize`, after `finalizeRun`, both message finalizers, and `createRun` have completed, invoke:

```go
if r.service.mcpSessions != nil && r.mcpUserPublicID != "" {
	_ = r.service.mcpSessions.CloseRun(ctx, r.mcpUserPublicID, r.run.RunID)
}
```

`CloseRun` owns the cancellation-detached five-second deadline described in Task 4. Cleanup is best effort and cannot replace the primary send/finalization error; log only server/session counts and a safe error class. Add table tests in `service_run_mcp_test.go` for success, ordinary error, `ErrMessageGenerationCanceled`, and deadline exceeded. Each case must prove the local run/message finalizers execute before cleanup, DELETE occurs once per opened Server, and a Server never opened receives no DELETE.

- [ ] **Step 5: Use ephemeral operations for admin sync/probe and always close them**

In `application/mcp/service.go`, replace direct `client.ListTools` with:

```go
operation, closeOperation, err := s.mcpSessions.OpenEphemeral(
	ctx,
	callConfig,
	0,
)
if err != nil {
	return fail(err)
}
defer func() {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = closeOperation(cleanupCtx)
}()

tools, err := operation.ListTools(ctx)
```

Sync keeps `ContextModeSync`; probe keeps `ContextModeProbe`. Neither receives a synthetic conversation run session or enters the reusable run map.

- [ ] **Step 6: Wire one shared manager and add shutdown cleanup**

In `app.go`, create one transport/client and one run session manager, inject it into conversation and MCP application services, store it on `App`, and call `CloseAll` near the start of the existing `App.Close()` method with a five-second background timeout before database/telemetry teardown. Do not create a manager per request or per service; the CLI already defers `instance.Close()`.

- [ ] **Step 7: Run application and retry tests, then commit**

Run: `cd backend && go test ./internal/infra/mcp ./internal/application/conversation ./internal/application/mcp ./internal/app -count=1`

Expected: PASS; tests prove retry classification, one run/server initialize, same-session serial calls, different-server independence, ephemeral sync cleanup, and DELETE on every run terminal path.

```bash
git add backend/internal/infra/mcp/retry.go backend/internal/infra/mcp/retry_test.go backend/internal/application/conversation/service.go backend/internal/application/conversation/service_mcp_tools.go backend/internal/application/conversation/service_tool.go backend/internal/application/conversation/service_tool_execution.go backend/internal/application/conversation/service_message_send.go backend/internal/application/conversation/service_run.go backend/internal/application/conversation/service_mcp_tools_test.go backend/internal/application/conversation/service_tool_test.go backend/internal/application/conversation/service_mcp_lifecycle_test.go backend/internal/application/conversation/service_run_mcp_test.go backend/internal/application/mcp/service.go backend/internal/application/mcp/service_lifecycle_test.go backend/internal/app/app.go
git commit -m "refactor: classify mcp retries and cleanup"
```

Expected: commit succeeds; `git diff HEAD^ --name-only` contains backend Go files only and no persistence model/schema, HTTP DTO, Swagger, or frontend path.

---

### Task 6: Race, cleanup, protocol, and repository-wide release gate

**Files:**
- Modify: `backend/internal/infra/mcp/transport_test.go`
- Modify: `backend/internal/infra/mcp/operation_test.go`
- Modify: `backend/internal/infra/mcp/session_manager_test.go`
- Modify: `backend/internal/application/conversation/service_mcp_lifecycle_test.go`
- Modify: `backend/internal/application/conversation/service_run_mcp_test.go`
- Modify: `backend/internal/application/mcp/service_lifecycle_test.go`
- Test: all backend packages

**Interfaces:**
- Consumes: the complete phase-D implementation and all A+B/C frozen contracts.
- Produces: deterministic release evidence for 2025-11-25 Headers, SSE recovery/pagination bounds, retry non-replay, cancellation cleanup, cross-user isolation, and race freedom.

- [ ] **Step 1: Add one end-to-end `httptest.Server` lifecycle matrix**

Add `TestMCPRunLifecycleMatrix` with subtests `json`, `sse_multiple_events`, `sse_resume`, `pagination`, `session_404`, `cancel`, and `timeout`. Every subtest captures method, JSON-RPC method, request ID, `MCP-Protocol-Version`, `MCP-Session-Id`, `Last-Event-ID`, custom Header values, and the count of `tools/call` POSTs.

Assertions are exact:

- all post-initialize physical requests carry `MCP-Protocol-Version: 2025-11-25`;
- all requests in one session carry identical rendered custom Headers;
- signed JWT strings differ after advancing the fake clock beyond the phase-C TTL; `jti`, `iat`, `nbf`, and `exp` differ, while subject, issuer, audience, mode, context IDs, and opt-in PII claims remain identical;
- SSE recovery sends GET with the last event ID and never a second `tools/call` POST;
- pagination sends cursors `cursor-1` and `cursor-2` and returns all three pages;
- with retry budget 1, a 404 on `tools/list` reinitializes once; with either budget, a 404 on `tools/call` does not replay;
- cancellation and timeout close bodies, exit readers, and attempt DELETE through a bounded cleanup context.

- [ ] **Step 2: Add a concurrent isolation stress test**

Run 50 users × 2 runs × 2 Servers, with two goroutines acquiring/calling each operation. Assert 200 distinct session keys, 100 session keys per Server, no cross-user Header or signer input, max in-flight per session equal to one, and max in-flight across different Servers greater than one. Close every run and assert the manager entry count is zero.

Name: `TestSessionManagerConcurrentIsolationStress`.

- [ ] **Step 3: Run focused race and cleanup verification**

Run: `cd backend && go test -race ./internal/infra/mcp ./internal/application/conversation ./internal/application/mcp -count=1`

Expected: PASS with no `WARNING: DATA RACE`; all SSE reader/body cleanup channels close and the stress-test manager count reaches zero.

- [ ] **Step 4: Run the required backend validation commands**

Run: `cd backend && make test`

Expected: `go test ./...` exits 0 with every package passing.

Run: `cd backend && go test -race ./...`

Expected: exits 0 with no race report and no package failure.

Run: `cd backend && go vet ./...`

Expected: exits 0 with no vet diagnostics.

Run: `cd backend && make build`

Expected: version check and `go build` exit 0 and produce `.cache/deeix-chat/deeix-chat` (or the platform-equivalent executable name) without compile/link errors.

- [ ] **Step 5: Verify scope and forbidden changes**

Run: `git diff --name-only refs/codex/phase-d-base`

Expected: no path under `frontend/`, `backend/internal/transport/http/`, `backend/docs/`, `backend/internal/infra/persistence/models/`, or `backend/internal/infra/persistence/schema/`; only the backend MCP/application/app files enumerated in this plan are present. The plan document itself may be ignored by the repository's current `docs` rule and is not part of this implementation diff.

Run: `rg -n 'protocolVersion\s*=\s*"2025-11-25"|MCP-Protocol-Version|Last-Event-ID|maxToolListPages|MCPToolRetryCount' backend/internal/infra/mcp backend/internal/application/conversation`

Expected: the protocol version is defined once in `protocol.go`; protocol and resume Header writes occur only in `transport.go`; the page limit occurs in `operation.go`; `MCPToolRetryCount` is passed into the classified retry budget and is not used by an application-level blanket loop.

- [ ] **Step 6: Commit the release-gate tests**

```bash
git add backend/internal/infra/mcp/transport_test.go backend/internal/infra/mcp/operation_test.go backend/internal/infra/mcp/session_manager_test.go backend/internal/application/conversation/service_mcp_lifecycle_test.go backend/internal/application/conversation/service_run_mcp_test.go backend/internal/application/mcp/service_lifecycle_test.go
git commit -m "test: verify mcp lifecycle release gate"
```

Expected: final commit succeeds, `git status --short` is clean, and the four required backend validation commands from Step 4 remain green.

After recording the verified diff, remove only the temporary local ref:

```powershell
git update-ref -d refs/codex/phase-d-base
```

---

## Completion Checklist

- [ ] Task 1 transport tests and Task 2 implementation were committed together while production still advertised `2025-06-18`.
- [ ] Task 2 made the complete pre-upgrade transport suite green before Task 3 changed the version.
- [ ] The only advertised/supported MCP protocol version is `2025-11-25` after Task 3.
- [ ] Protocol, auth, trace, and `X-DEEIX-*` Headers cannot be supplied by custom templates and are written only by transport.
- [ ] Session reuse is scoped by server ID, user public ID, run ID, auth identity, and config version, never by URL.
- [ ] Template/custom Header/signing inputs are immutable within a session; phase-C JWTs are generated per physical HTTP request.
- [ ] JSON, multi-event SSE, GET resume with `Last-Event-ID`, cursor pagination, and all configured limits are tested.
- [ ] Ambiguously sent `tools/call` requests are never replayed; only explicitly not-sent calls may consume retry budget.
- [ ] Same-session calls serialize, different-server sessions may overlap, and all run terminal paths attempt DELETE for every opened session with an assigned non-empty session ID; never-opened Servers send no DELETE.
- [ ] Cancellation/timeout tests prove readers and bodies exit without relying on goroutine-count timing.
- [ ] No frontend, MCP admin API, Swagger, database model, or schema change is included.
- [ ] `make test`, `go test -race ./...`, `go vet ./...`, and `make build` all exit 0.
