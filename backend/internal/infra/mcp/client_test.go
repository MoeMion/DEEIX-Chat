package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClientContextSigner struct {
	token   string
	err     error
	calls   atomic.Int32
	started chan struct{}
	release <-chan struct{}
}

func (s *fakeClientContextSigner) Sign(TemplateContext, SignedContextConfig) (string, error) {
	s.calls.Add(1)
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if s.release != nil {
		<-s.release
	}
	return s.token, s.err
}

func TestBuildCallToolParamsUsesPublicMetadataOnly(t *testing.T) {
	t.Parallel()
	params, err := buildCallToolParams(CallConfig{Context: TemplateContext{
		UserPublicID:             "user-public",
		ConversationPublicID:     "conversation-public",
		AssistantMessagePublicID: "assistant-public",
		UserMessagePublicID:      "user-message-public",
		RequestID:                "request-public",
		RunID:                    "run-public",
		TraceID:                  "trace-public",
	}}, CallInput{ToolName: "memory.list", ArgumentsJSON: `{"scope":"user"}`})
	if err != nil {
		t.Fatal(err)
	}
	meta, ok := params["_meta"].(map[string]interface{})
	if !ok {
		t.Fatalf("meta = %#v", params["_meta"])
	}
	want := map[string]interface{}{
		"deeix_user_public_id":              "user-public",
		"deeix_conversation_public_id":      "conversation-public",
		"deeix_assistant_message_public_id": "assistant-public",
		"deeix_user_message_public_id":      "user-message-public",
		"deeix_request_id":                  "request-public",
		"deeix_run_id":                      "run-public",
		"deeix_trace_id":                    "trace-public",
	}
	if !reflect.DeepEqual(meta, want) {
		t.Fatalf("meta = %#v, want %#v", meta, want)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"user_id", "conversation_id", `"99"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("forbidden metadata %q in %s", forbidden, encoded)
		}
	}
}

func TestClientCallToolOwnsHeadersAcrossLifecycleAndDeletesSession(t *testing.T) {
	t.Parallel()
	type captured struct {
		method    string
		rpcMethod string
		header    http.Header
	}
	var mu sync.Mutex
	requests := make([]captured, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item := captured{method: r.Method, header: r.Header.Clone()}
		var request struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
		}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			item.rpcMethod = request.Method
		}
		mu.Lock()
		requests = append(requests, item)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case request.Method == "initialize":
			w.Header().Set("MCP-Session-Id", "session-1")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result": map[string]interface{}{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]interface{}{},
					"serverInfo":      map[string]string{"name": "test", "version": "1"},
				},
			})
		case request.Method == "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case request.Method == "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result": map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": "ok"}},
				},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, request.Method)
		}
	}))
	defer server.Close()

	client := NewClient()
	_, err := client.CallTool(context.Background(), CallConfig{
		BaseURL:       server.URL,
		AuthToken:     "bearer-value",
		CustomHeaders: map[string]string{"X-Tenant": "tenant-a"},
	}, CallInput{ToolName: "memory.list", ArgumentsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	got := append([]captured(nil), requests...)
	mu.Unlock()
	if len(got) != 4 {
		t.Fatalf("requests = %#v", got)
	}
	methods := []string{"initialize", "notifications/initialized", "tools/call", ""}
	for index, request := range got {
		if request.rpcMethod != methods[index] {
			t.Fatalf("request %d method = %q", index, request.rpcMethod)
		}
		if request.header.Get("X-Tenant") != "tenant-a" ||
			request.header.Get("Authorization") != "Bearer bearer-value" {
			t.Fatalf("request %d lost business/auth Headers: %#v", index, request.header)
		}
		if index == 0 {
			if request.header.Get("MCP-Session-Id") != "" ||
				request.header.Get("MCP-Protocol-Version") != "" {
				t.Fatalf("initialize has session Headers: %#v", request.header)
			}
			continue
		}
		if request.header.Get("MCP-Session-Id") != "session-1" ||
			request.header.Get("MCP-Protocol-Version") != "2025-06-18" {
			t.Fatalf("request %d protocol Headers = %#v", index, request.header)
		}
	}
}

func TestClientContextJWTUsesOneTokenAcrossOperation(t *testing.T) {
	tests := []struct {
		name      string
		mode      ContextMode
		rpcMethod string
		invoke    func(context.Context, *Client, CallConfig) error
	}{
		{
			name:      "call tool",
			mode:      ContextModeChat,
			rpcMethod: "tools/call",
			invoke: func(ctx context.Context, client *Client, cfg CallConfig) error {
				_, err := client.CallTool(ctx, cfg, CallInput{ToolName: "memory.list", ArgumentsJSON: `{}`})
				return err
			},
		},
		{
			name:      "list tools",
			mode:      ContextModeProbe,
			rpcMethod: "tools/list",
			invoke: func(ctx context.Context, client *Client, cfg CallConfig) error {
				_, err := client.ListTools(ctx, cfg)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			type capturedRequest struct {
				method    string
				rpcMethod string
				header    http.Header
			}

			var mu sync.Mutex
			requests := make([]capturedRequest, 0, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured := capturedRequest{method: r.Method, header: r.Header.Clone()}
				var payload struct {
					ID     interface{} `json:"id"`
					Method string      `json:"method"`
				}
				if r.Method == http.MethodPost {
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatalf("decode request: %v", err)
					}
					captured.rpcMethod = payload.Method
				}
				mu.Lock()
				requests = append(requests, captured)
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				case payload.Method == "initialize":
					w.Header().Set("MCP-Session-Id", "signed-session")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      payload.ID,
						"result": map[string]interface{}{
							"protocolVersion": protocolVersion,
						},
					})
				case payload.Method == "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
				case payload.Method == "tools/list":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      payload.ID,
						"result":  map[string]interface{}{"tools": []interface{}{}},
					})
				case payload.Method == "tools/call":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      payload.ID,
						"result": map[string]interface{}{
							"content": []map[string]string{{"type": "text", "text": "ok"}},
						},
					})
				default:
					t.Fatalf("unexpected request %s %s", r.Method, payload.Method)
				}
			}))
			defer server.Close()

			started := make(chan struct{}, 1)
			release := make(chan struct{})
			signer := &fakeClientContextSigner{
				token:   "signed-context-token",
				started: started,
				release: release,
			}
			client := NewClient()
			client.contextSigner = signer
			customHeaders := map[string]string{"X-Tenant": "tenant-a"}
			cfg := CallConfig{
				BaseURL:       server.URL,
				CustomHeaders: customHeaders,
				Context: TemplateContext{
					Mode:         test.mode,
					UserPublicID: "user-public",
				},
				SignedContext: &SignedContextConfig{
					Secret:         "secret-fixture",
					Issuer:         "https://chat.example.test",
					Audience:       "urn:deeix:mcp:test",
					KeyID:          "ctx_test",
					ExpiresSeconds: 300,
				},
			}

			errCh := make(chan error, 1)
			go func() {
				errCh <- test.invoke(context.Background(), client, cfg)
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("signer was not called")
			}
			customHeaders["X-Tenant"] = "mutated-after-clone"
			close(release)
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("operation error = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("operation did not complete")
			}

			if signer.calls.Load() != 1 {
				t.Fatalf("signer calls = %d, want 1", signer.calls.Load())
			}
			mu.Lock()
			got := append([]capturedRequest(nil), requests...)
			mu.Unlock()
			if len(got) != 4 {
				t.Fatalf("request count = %d, want 4", len(got))
			}
			wantRPCMethods := []string{"initialize", "notifications/initialized", test.rpcMethod, ""}
			for index, request := range got {
				if request.rpcMethod != wantRPCMethods[index] {
					t.Fatalf("request %d rpc method = %q, want %q", index, request.rpcMethod, wantRPCMethods[index])
				}
				if token := request.header.Get("X-DEEIX-Context"); token != "signed-context-token" || token == "" {
					t.Fatalf("request %d signed context token mismatch (length %d)", index, len(token))
				}
				if tenant := request.header.Get("X-Tenant"); tenant != "tenant-a" {
					t.Fatalf("request %d tenant Header = %q, want cloned value", index, tenant)
				}
			}
		})
	}
}

func TestClientContextJWTRejectsCustomHeaderOverride(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()

	signer := &fakeClientContextSigner{token: "signed-context-token"}
	client := NewClient()
	client.contextSigner = signer
	_, err := client.CallTool(context.Background(), CallConfig{
		BaseURL:       server.URL,
		CustomHeaders: map[string]string{"x-deeix-context": "attacker-token"},
		Context:       TemplateContext{Mode: ContextModeChat, UserPublicID: "user-public"},
		SignedContext: &SignedContextConfig{},
	}, CallInput{ToolName: "memory.list", ArgumentsJSON: `{}`})
	if err == nil {
		t.Fatal("expected reserved Header validation error")
	}
	if signer.calls.Load() != 0 {
		t.Fatalf("signer calls = %d, want 0", signer.calls.Load())
	}
	if hits.Load() != 0 {
		t.Fatalf("server received %d requests", hits.Load())
	}
}

func TestClientContextJWTFailsClosedWithoutSigner(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()

	client := NewClient()
	client.contextSigner = nil
	_, err := client.ListTools(context.Background(), CallConfig{
		BaseURL:       server.URL,
		Context:       TemplateContext{Mode: ContextModeProbe, UserPublicID: "user-public"},
		SignedContext: &SignedContextConfig{},
	})
	if !errors.Is(err, ErrContextSignerUnavailable) {
		t.Fatalf("ListTools() error = %v, want ErrContextSignerUnavailable", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server received %d requests", hits.Load())
	}
}

func TestClientContextJWTFailsClosedOnSignerError(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()

	signErr := fmt.Errorf("signer fixture: %w", ErrInvalidSignedContext)
	signer := &fakeClientContextSigner{err: signErr}
	client := NewClient()
	client.contextSigner = signer
	_, err := client.CallTool(context.Background(), CallConfig{
		BaseURL:       server.URL,
		Context:       TemplateContext{Mode: ContextModeChat, UserPublicID: "user-public"},
		SignedContext: &SignedContextConfig{},
	}, CallInput{ToolName: "memory.list", ArgumentsJSON: `{}`})
	if err != signErr || !errors.Is(err, ErrInvalidSignedContext) {
		t.Fatalf("CallTool() error = %v, want unchanged signer error", err)
	}
	if signer.calls.Load() != 1 {
		t.Fatalf("signer calls = %d, want 1", signer.calls.Load())
	}
	if hits.Load() != 0 {
		t.Fatalf("server received %d requests", hits.Load())
	}

	emptySigner := &fakeClientContextSigner{}
	client.contextSigner = emptySigner
	_, err = client.CallTool(context.Background(), CallConfig{
		BaseURL:       server.URL,
		Context:       TemplateContext{Mode: ContextModeChat, UserPublicID: "user-public"},
		SignedContext: &SignedContextConfig{},
	}, CallInput{ToolName: "memory.list", ArgumentsJSON: `{}`})
	if !errors.Is(err, ErrInvalidSignedContext) {
		t.Fatalf("CallTool() empty-token error = %v, want ErrInvalidSignedContext", err)
	}
	if emptySigner.calls.Load() != 1 {
		t.Fatalf("empty-token signer calls = %d, want 1", emptySigner.calls.Load())
	}
	if hits.Load() != 0 {
		t.Fatalf("server received %d requests after empty signed token", hits.Load())
	}
}

func TestClientRejectsDirectCustomHeaderBypassBeforeDispatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		headers func() map[string]string
	}{
		{name: "authorization", headers: func() map[string]string {
			return map[string]string{"authorization": "attacker"}
		}},
		{name: "signed context", headers: func() map[string]string {
			return map[string]string{"X-DEEIX-Context": "attacker"}
		}},
		{name: "crlf", headers: func() map[string]string {
			return map[string]string{"X-Test": "ok\r\ninjected: true"}
		}},
		{name: "too many", headers: func() map[string]string {
			result := make(map[string]string, 33)
			for i := 0; i < 33; i++ {
				result[fmt.Sprintf("X-Test-%02d", i)] = "value"
			}
			return result
		}},
		{name: "aggregate overflow", headers: func() map[string]string {
			return map[string]string{
				"X-A": strings.Repeat("a", 4096),
				"X-B": strings.Repeat("b", 4096),
				"X-C": strings.Repeat("c", 4096),
				"X-D": strings.Repeat("d", 4096),
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				hits.Add(1)
			}))
			defer server.Close()
			_, err := NewClient().CallTool(context.Background(), CallConfig{
				BaseURL:       server.URL,
				CustomHeaders: tt.headers(),
			}, CallInput{ToolName: "test", ArgumentsJSON: `{}`})
			if err == nil {
				t.Fatal("expected validation error")
			}
			if hits.Load() != 0 {
				t.Fatalf("server received %d requests", hits.Load())
			}
		})
	}
}

func TestClientDeniesRedirectBeforeSensitiveHeadersReachTarget(t *testing.T) {
	t.Parallel()
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	_, err := NewClient().CallTool(context.Background(), CallConfig{
		BaseURL:       redirect.URL,
		AuthToken:     "redirect-secret",
		CustomHeaders: map[string]string{"X-User": "user-public"},
	}, CallInput{ToolName: "test", ArgumentsJSON: `{}`})
	if err == nil {
		t.Fatal("expected redirect denial")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target received %d requests", targetHits.Load())
	}
}

func TestClientDeletesAssignedSessionAfterProtocolMismatch(t *testing.T) {
	t.Parallel()
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("MCP-Session-Id", "session-mismatch")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      request.ID,
			"result": map[string]interface{}{
				"protocolVersion": "unsupported-version",
			},
		})
	}))
	defer server.Close()

	_, err := NewClient().CallTool(context.Background(), CallConfig{BaseURL: server.URL}, CallInput{
		ToolName: "test", ArgumentsJSON: `{}`,
	})
	if !errors.Is(err, ErrUnsupportedProtocolVersion) {
		t.Fatalf("error = %v", err)
	}
	if deletes.Load() != 1 {
		t.Fatalf("DELETE count = %d", deletes.Load())
	}
}

func TestClientErrorNeverContainsRemoteOrURLSecret(t *testing.T) {
	t.Parallel()
	const secret = "echoed-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(secret))
	}))
	defer server.Close()

	_, err := NewClient().CallTool(context.Background(), CallConfig{
		BaseURL: server.URL + "/" + secret,
	}, CallInput{ToolName: "test", ArgumentsJSON: `{}`})
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(SafeErrorSummary(err), secret) {
		t.Fatalf("secret leaked through error: %v", err)
	}
}

func TestClientCleanupAfterInitializedFailureAndSessionlessSuccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		sessionID       string
		failInitialized bool
		wantDeletes     int32
	}{
		{name: "initialized failure closes assigned session", sessionID: "session-1", failInitialized: true, wantDeletes: 1},
		{name: "no session skips delete", sessionID: "", failInitialized: false, wantDeletes: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var deletes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deletes.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var request struct {
					ID     interface{} `json:"id"`
					Method string      `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				w.Header().Set("Content-Type", "application/json")
				switch request.Method {
				case "initialize":
					if tt.sessionID != "" {
						w.Header().Set("MCP-Session-Id", tt.sessionID)
					}
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0", "id": request.ID,
						"result": map[string]interface{}{"protocolVersion": "2025-06-18"},
					})
				case "notifications/initialized":
					if tt.failInitialized {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusAccepted)
				case "tools/call":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0", "id": request.ID,
						"result": map[string]interface{}{"content": []interface{}{}},
					})
				default:
					t.Fatalf("unexpected method %q", request.Method)
				}
			}))
			defer server.Close()

			_, err := NewClient().CallTool(context.Background(), CallConfig{BaseURL: server.URL}, CallInput{
				ToolName: "test", ArgumentsJSON: `{}`,
			})
			if tt.failInitialized && err == nil {
				t.Fatal("expected initialized failure")
			}
			if !tt.failInitialized && err != nil {
				t.Fatal(err)
			}
			if got := deletes.Load(); got != tt.wantDeletes {
				t.Fatalf("DELETE count = %d, want %d", got, tt.wantDeletes)
			}
		})
	}
}

func TestClientRejectsUnsafeEndpointComponentsBeforeDispatch(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()

	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "userinfo", baseURL: strings.Replace(server.URL, "http://", "http://user:password@", 1)},
		{name: "query", baseURL: server.URL + "?token=query-secret"},
		{name: "empty query", baseURL: server.URL + "?"},
		{name: "fragment", baseURL: server.URL + "#fragment-secret"},
		{name: "unsupported scheme", baseURL: strings.Replace(server.URL, "http://", "ftp://", 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient().CallTool(t.Context(), CallConfig{BaseURL: tt.baseURL}, CallInput{
				ToolName: "test", ArgumentsJSON: `{}`,
			})
			var clientErr *ClientError
			if !errors.As(err, &clientErr) || clientErr.Kind != ClientErrorProtocol {
				t.Fatalf("error = %#v", err)
			}
			if strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("endpoint data leaked through error: %v", err)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("server received %d requests", hits.Load())
	}
}

func TestClientRevalidatesSSRFPolicyBeforeDispatch(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()

	_, err := NewClientWithEnv("production", true).CallTool(t.Context(), CallConfig{BaseURL: server.URL}, CallInput{
		ToolName: "test", ArgumentsJSON: `{}`,
	})
	var clientErr *ClientError
	if !errors.As(err, &clientErr) || clientErr.Kind != ClientErrorProtocol {
		t.Fatalf("error = %#v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server received %d requests", hits.Load())
	}
}

func TestClientRejectsExplicitEmptyProtocolAndDeletesSession(t *testing.T) {
	t.Parallel()
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request struct {
			ID interface{} `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("MCP-Session-Id", "session-empty-protocol")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      request.ID,
			"result":  map[string]interface{}{"protocolVersion": ""},
		})
	}))
	defer server.Close()

	_, err := NewClient().CallTool(t.Context(), CallConfig{BaseURL: server.URL}, CallInput{
		ToolName: "test", ArgumentsJSON: `{}`,
	})
	if !errors.Is(err, ErrUnsupportedProtocolVersion) {
		t.Fatalf("error = %v", err)
	}
	if deletes.Load() != 1 {
		t.Fatalf("DELETE count = %d", deletes.Load())
	}
}

func TestClientNetworkErrorDiscardsRawURLAndCause(t *testing.T) {
	t.Parallel()
	const secret = "network-url-secret"
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(secret)
	})}}

	_, err := client.CallTool(t.Context(), CallConfig{BaseURL: "http://example.invalid/" + secret}, CallInput{
		ToolName: "test", ArgumentsJSON: `{}`,
	})
	var clientErr *ClientError
	if !errors.As(err, &clientErr) || clientErr.Kind != ClientErrorNetwork {
		t.Fatalf("error = %#v", err)
	}
	if errors.Unwrap(clientErr) != nil {
		t.Fatalf("network error retained raw cause: %#v", errors.Unwrap(clientErr))
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(SafeErrorSummary(err), secret) {
		t.Fatalf("network secret leaked through error: %v", err)
	}
}

func TestClientNetworkCancellationRemainsInspectable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("must not be exposed")
	})}}

	_, err := client.CallTool(ctx, CallConfig{BaseURL: "http://example.invalid/mcp"}, CallInput{
		ToolName: "test", ArgumentsJSON: `{}`,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %#v", err)
	}
	var clientErr *ClientError
	if !errors.As(err, &clientErr) || clientErr.Kind != ClientErrorNetwork {
		t.Fatalf("client error = %#v", clientErr)
	}
}

func TestClientCleanupIgnoresDeleteFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("cleanup-secret"))
			return
		}
		var request struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "initialize":
			w.Header().Set("MCP-Session-Id", "session-delete-failure")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]interface{}{"protocolVersion": protocolVersion},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]interface{}{"content": []map[string]string{{"type": "text", "text": "ok"}}},
			})
		default:
			t.Fatalf("unexpected method %q", request.Method)
		}
	}))
	defer server.Close()

	output, err := NewClient().CallTool(t.Context(), CallConfig{BaseURL: server.URL}, CallInput{
		ToolName: "test", ArgumentsJSON: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if output != `{"content":[{"text":"ok","type":"text"}]}` {
		t.Fatalf("output = %s", output)
	}
}

func TestClientCleanupSurvivesCanceledParent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "initialize":
			w.Header().Set("MCP-Session-Id", "session-canceled-parent")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]interface{}{"protocolVersion": protocolVersion},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			cancel()
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected method %q", request.Method)
		}
	}))
	defer server.Close()

	_, err := NewClient().CallTool(ctx, CallConfig{BaseURL: server.URL}, CallInput{
		ToolName: "test", ArgumentsJSON: `{}`,
	})
	if err == nil {
		t.Fatal("expected primary call error")
	}
	if deletes.Load() != 1 {
		t.Fatalf("DELETE count = %d", deletes.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestClientCallToolUsesStreamableHTTPJSONRPC(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/mcp" {
			t.Fatalf("expected /mcp path, got %s", r.URL.Path)
		}
		var req struct {
			ID     interface{}            `json:"id"`
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		methods = append(methods, req.Method)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session_1")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]interface{}{
					"protocolVersion": protocolVersion,
					"capabilities":    map[string]interface{}{},
					"serverInfo":      map[string]string{"name": "test", "version": "1.0.0"},
				},
			})
		case "notifications/initialized":
			if r.Header.Get("Mcp-Session-Id") != "session_1" {
				t.Fatalf("expected session header on initialized notification")
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if r.Header.Get("Mcp-Session-Id") != "session_1" {
				t.Fatalf("expected session header on tools/call")
			}
			if req.Params["name"] != "memory.list" {
				t.Fatalf("expected tool name, got %#v", req.Params["name"])
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": "ok"}},
				},
			})
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer server.Close()

	client := NewClient()
	output, err := client.CallTool(context.Background(), CallConfig{BaseURL: server.URL + "/mcp"}, CallInput{
		ToolName:      "memory.list",
		ArgumentsJSON: `{"scope":"user"}`,
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if output != `{"content":[{"text":"ok","type":"text"}]}` {
		t.Fatalf("unexpected output: %s", output)
	}
	if len(methods) != 3 || methods[0] != "initialize" || methods[1] != "notifications/initialized" || methods[2] != "tools/call" {
		t.Fatalf("unexpected methods: %#v", methods)
	}
}

func TestClientCallToolRejectsInvalidArgumentsJSON(t *testing.T) {
	client := NewClient()
	_, err := client.CallTool(context.Background(), CallConfig{BaseURL: "http://127.0.0.1/mcp"}, CallInput{
		ToolName:      "bing_search",
		ArgumentsJSON: `{bad`,
	})
	if err == nil || err.Error() != "mcp tool arguments must be a valid JSON object" {
		t.Fatalf("expected invalid arguments error, got %v", err)
	}
}

func TestClientCallToolTreatsMCPResultErrorAsExecutionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session_1")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": map[string]interface{}{}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]interface{}{
					"isError": true,
					"content": []map[string]string{{"type": "text", "text": "missing required field query"}},
				},
			})
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer server.Close()

	client := NewClient()
	_, err := client.CallTool(context.Background(), CallConfig{BaseURL: server.URL}, CallInput{
		ToolName:      "bing_search",
		ArgumentsJSON: `{}`,
	})
	if err == nil || err.Error() != "mcp client error: kind=tool_result status=0 rpc_code=0" {
		t.Fatalf("expected MCP tool error, got %v", err)
	}
}

func TestClientCallToolTreatsWrappedMCPProtocolErrorAsExecutionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session_1")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": map[string]interface{}{}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]interface{}{
					"content": []map[string]string{{
						"type": "text",
						"text": "MCP error -32602: Input validation error: Invalid arguments for tool bing_search",
					}},
				},
			})
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer server.Close()

	client := NewClient()
	_, err := client.CallTool(context.Background(), CallConfig{BaseURL: server.URL}, CallInput{
		ToolName:      "bing_search",
		ArgumentsJSON: `{}`,
	})
	if err == nil || err.Error() != "mcp client error: kind=tool_result status=0 rpc_code=0" {
		t.Fatalf("expected wrapped MCP protocol error, got %v", err)
	}
}

func TestClientListToolsParsesSSEJSONRPC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": map[string]interface{}{}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"tools\":[{\"name\":\"memory.list\",\"description\":\"List memories\"}]}}\n\n"))
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer server.Close()

	client := NewClient()
	tools, err := client.ListTools(context.Background(), CallConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "memory.list" || tools[0].Description != "List memories" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
}

func TestParseRPCResponseParsesLargeSingleLineSSEData(t *testing.T) {
	toolDescription := strings.Repeat("x", 70*1024)
	payload := `event: message
data: {"jsonrpc":"2.0","id":3,"result":{"tools":[{"name":"memory.list","description":"` + toolDescription + `"}]}}

`

	result, err := parseRPCResponse("text/event-stream", []byte(payload))
	if err != nil {
		t.Fatalf("parse rpc response: %v", err)
	}

	var parsed struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(parsed.Tools) != 1 || parsed.Tools[0].Name != "memory.list" {
		t.Fatalf("unexpected tools: %#v", parsed.Tools)
	}
	if parsed.Tools[0].Description != toolDescription {
		t.Fatalf("unexpected tool description length: got %d want %d", len(parsed.Tools[0].Description), len(toolDescription))
	}
}
