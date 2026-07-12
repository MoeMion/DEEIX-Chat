package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
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

type waitForContextBody struct{ ctx context.Context }

func (b *waitForContextBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (*waitForContextBody) Close() error { return nil }

type closeTrackingBody struct {
	reader io.Reader
	closed atomic.Bool
}

func (b *closeTrackingBody) Read(buffer []byte) (int, error) {
	return b.reader.Read(buffer)
}

func (b *closeTrackingBody) Close() error {
	b.closed.Store(true)
	return nil
}

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

func validJSONResponseAtSize(t *testing.T, size int64) string {
	t.Helper()
	const response = `{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`
	padding := size - int64(len(response))
	if padding < 0 {
		t.Fatalf("response size %d is smaller than fixture size %d", size, len(response))
	}
	result := response + strings.Repeat(" ", int(padding))
	if int64(len(result)) != size || !json.Valid([]byte(result)) {
		t.Fatalf("failed to build valid JSON response of size %d", size)
	}
	return result
}

func notifyWroteRequest(req *http.Request, writeErr error) {
	trace := httptrace.ContextClientTrace(req.Context())
	if trace != nil && trace.WroteRequest != nil {
		trace.WroteRequest(httptrace.WroteRequestInfo{Err: writeErr})
	}
}

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
		if req.Header.Get("MCP-Protocol-Version") != protocolVersion {
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
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":%q,"capabilities":{},"serverInfo":{"name":"test","version":"1"}}}`, id, protocolVersion)
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
		Mode:                     ContextModeChat,
		UserPublicID:             "user_public",
		UserDisplayName:          "User Fixture",
		UserEmail:                "user@example.test",
		UserRole:                 "member",
		ConversationPublicID:     "conversation_public",
		AssistantMessagePublicID: "assistant_message_public",
		UserMessagePublicID:      "user_message_public",
		RequestID:                "request_public",
		RunID:                    "run_public",
		TraceID:                  "trace_public",
	}
	signedContextConfig := SignedContextConfig{
		Secret:         "secret-fixture",
		Issuer:         "https://chat.example.test",
		Audience:       "urn:deeix:mcp:server-1",
		KeyID:          "ctx_test",
		ExpiresSeconds: 300,
		IncludeName:    true,
		IncludeEmail:   true,
		IncludeRole:    true,
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
		SignedContext: &signedContextConfig,
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
	if len(signer.inputs) != len(requests) {
		t.Fatalf("signer input count = %d, want %d", len(signer.inputs), len(requests))
	}
	for _, input := range signer.inputs {
		if !reflect.DeepEqual(input.TemplateContext, templateContext) {
			t.Fatalf("signer context changed: %#v", input.TemplateContext)
		}
		if !reflect.DeepEqual(input.Config, signedContextConfig) {
			t.Fatalf("signer config changed: %#v", input.Config)
		}
	}

	for _, name := range []string{"mcp-session-id", "MCP-Protocol-Version", "Last-Event-ID", "X-DEEIX-Context"} {
		if err := ValidateRenderedCustomHeaders(map[string]string{name: "malicious"}); err == nil {
			t.Fatalf("reserved Header %q was accepted", name)
		}
	}
}

func TestClientTransportContractStatelessProtocolHeadersBeforeUpgrade(t *testing.T) {
	recorder := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		item, err := recorder.append(req)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		method, _ := item.Body["method"].(string)
		id := item.Body["id"]
		switch method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":%q}}`, id, protocolVersion)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"tools":[]}}`, id)
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := NewClient()
	client.httpClient = server.Client()
	if _, err := client.ListTools(context.Background(), CallConfig{BaseURL: server.URL, TimeoutMS: 1000}); err != nil {
		t.Fatal(err)
	}

	requests := recorder.snapshot()
	if len(requests) != 3 {
		t.Fatalf("request count = %d, want 3", len(requests))
	}
	for index, request := range requests {
		method, _ := request.Body["method"].(string)
		if request.Header.Get("MCP-Session-Id") != "" {
			t.Fatalf("request %d unexpectedly had a session id", index)
		}
		wantVersion := protocolVersion
		if method == "initialize" {
			wantVersion = ""
		}
		if got := request.Header.Get("MCP-Protocol-Version"); got != wantVersion {
			t.Fatalf("request %d (%s) protocol version = %q, want %q", index, method, got, wantVersion)
		}
	}
}

func TestTransportContractResponsesAndBounds(t *testing.T) {
	tests := []struct {
		name          string
		contentType   string
		body          string
		bodySize      int64
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
		{name: "response exact limit", contentType: "application/json", bodySize: maxResponseBytes},
		{name: "response limit plus one", contentType: "application/json", bodySize: maxResponseBytes + 1, wantErr: ErrResponseTooLarge},
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
		{
			name: "list without request id rejects empty",
			request: func(endpoint string) TransportRequest {
				req := contractRequest(endpoint)
				req.RequestID = nil
				return req
			},
			wantErr: errInvalidTransportRequest,
		},
		{
			name: "initialized rejects nonempty response",
			body: `{"unexpected":true}`,
			request: func(endpoint string) TransportRequest {
				req := contractRequest(endpoint)
				req.Operation = OperationInitialized
				req.RequestID = nil
				req.Body = []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
				return req
			},
			wantErr: errInvalidRPCResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body
			if tt.bodySize != 0 {
				body = validJSONResponseAtSize(t, tt.bodySize)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.contentType != "" {
					w.Header().Set("Content-Type", tt.contentType)
				}
				if tt.sessionID != "" {
					w.Header().Set("MCP-Session-Id", tt.sessionID)
				}
				if body != "" {
					_, _ = io.WriteString(w, body)
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
		{name: "timeout", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 10*time.Millisecond)
		}, wantErr: context.DeadlineExceeded},
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
	t.Run("invalid outbound protocol identifiers", func(t *testing.T) {
		var calls atomic.Int32
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			notifyWroteRequest(req, nil)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":7,"result":{}}`)),
			}, nil
		})}
		tests := []struct {
			name    string
			mutate  func(*TransportRequest)
			wantErr error
		}{
			{name: "missing request id", mutate: func(req *TransportRequest) { req.RequestID = nil }, wantErr: errInvalidTransportRequest},
			{name: "invalid session syntax", mutate: func(req *TransportRequest) { req.Session.ID = "bad\n-session" }, wantErr: ErrInvalidSessionID},
			{name: "session space is not visible ascii", mutate: func(req *TransportRequest) { req.Session.ID = "bad session" }, wantErr: ErrInvalidSessionID},
			{name: "oversized session", mutate: func(req *TransportRequest) { req.Session.ID = strings.Repeat("s", maxSessionIDBytes+1) }, wantErr: ErrInvalidSessionID},
			{name: "invalid last event id syntax", mutate: func(req *TransportRequest) { req.LastEventID = "bad\nevent" }, wantErr: ErrInvalidLastEventID},
			{name: "invalid last event id utf8", mutate: func(req *TransportRequest) { req.LastEventID = string([]byte{0xff}) }, wantErr: ErrInvalidLastEventID},
			{name: "oversized last event id", mutate: func(req *TransportRequest) { req.LastEventID = strings.Repeat("e", maxLastEventIDBytes+1) }, wantErr: ErrInvalidLastEventID},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				req := contractRequest("https://mcp.invalid")
				tt.mutate(&req)
				_, err := newHTTPTransport(client, nil).Do(context.Background(), req)
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				var requestErr *RequestError
				if !errors.As(err, &requestErr) || requestErr.Delivery != DeliveryNotSent {
					t.Fatalf("error = %#v, want DeliveryNotSent", err)
				}
				if calls.Load() != 0 {
					t.Fatalf("dispatched %d invalid requests", calls.Load())
				}
			})
		}
	})

	for _, tt := range []struct {
		name       string
		mutate     func(*TransportRequest)
		headerName string
		headerWant string
	}{
		{
			name: "session id exact limit",
			mutate: func(req *TransportRequest) {
				req.Session.ID = strings.Repeat("s", maxSessionIDBytes)
				req.Session.ProtocolVersion = protocolVersion
			},
			headerName: "MCP-Session-Id",
			headerWant: strings.Repeat("s", maxSessionIDBytes),
		},
		{
			name: "last event id exact limit",
			mutate: func(req *TransportRequest) {
				req.Operation = OperationResumeSSE
				req.HTTPMethod = http.MethodGet
				req.Body = nil
				req.Session.ID = "session-1"
				req.Session.ProtocolVersion = protocolVersion
				req.LastEventID = strings.Repeat("e", maxLastEventIDBytes)
			},
			headerName: "Last-Event-ID",
			headerWant: strings.Repeat("e", maxLastEventIDBytes),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if got := req.Header.Get(tt.headerName); got != tt.headerWant {
					t.Fatalf("%s length = %d, want %d", tt.headerName, len(got), len(tt.headerWant))
				}
				notifyWroteRequest(req, nil)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":7,"result":{}}`)),
				}, nil
			})}
			req := contractRequest("https://mcp.invalid")
			tt.mutate(&req)
			if _, err := newHTTPTransport(client, nil).Do(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("dispatch count = %d, want 1", calls.Load())
			}
		})
	}

	for _, tt := range []struct {
		name          string
		sessionID     string
		wantErr       error
		wantSessionID string
	}{
		{name: "inbound session exact limit", sessionID: strings.Repeat("i", maxSessionIDBytes), wantSessionID: strings.Repeat("i", maxSessionIDBytes)},
		{name: "inbound session limit plus one", sessionID: strings.Repeat("i", maxSessionIDBytes+1), wantErr: ErrInvalidSessionID},
		{name: "inbound session invalid syntax", sessionID: "bad\n-session", wantErr: ErrInvalidSessionID},
		{name: "inbound session space", sessionID: "bad session", wantErr: ErrInvalidSessionID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inbound := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				notifyWroteRequest(req, nil)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header: http.Header{
						"Content-Type":   []string{"application/json"},
						"MCP-Session-Id": []string{tt.sessionID},
					},
					Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":7,"result":{}}`)),
				}, nil
			})}
			response, err := newHTTPTransport(inbound, nil).Do(context.Background(), contractRequest("https://mcp.invalid"))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				var requestErr *RequestError
				if !errors.As(err, &requestErr) || requestErr.Delivery != DeliverySent {
					t.Fatalf("error = %#v, want DeliverySent", err)
				}
				if response.SessionID != "" {
					t.Fatalf("invalid inbound session was retained (length %d)", len(response.SessionID))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.SessionID != tt.wantSessionID {
				t.Fatalf("session length = %d, want %d", len(response.SessionID), len(tt.wantSessionID))
			}
		})
	}

	t.Run("signed context exact limit", func(t *testing.T) {
		var calls atomic.Int32
		token := strings.Repeat("x", maxSignedContextHeaderBytes)
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			if got := req.Header.Get("X-DEEIX-Context"); got != token {
				t.Fatalf("signed context length = %d, want %d", len(got), len(token))
			}
			notifyWroteRequest(req, nil)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":7,"result":{}}`)),
			}, nil
		})}
		req := contractRequest("https://mcp.invalid")
		req.SignedContext = &SignedContextConfig{KeyID: "ctx_test"}
		if _, err := newHTTPTransport(client, staticSigner{token: token}).Do(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 1 {
			t.Fatalf("dispatch count = %d, want 1", calls.Load())
		}
	})

	t.Run("signed context limit plus one", func(t *testing.T) {
		var calls atomic.Int32
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("unexpected dispatch")
		})}
		req := contractRequest("https://mcp.invalid")
		req.SignedContext = &SignedContextConfig{KeyID: "ctx_test"}
		oversized := staticSigner{token: strings.Repeat("x", maxSignedContextHeaderBytes+1)}
		_, err := newHTTPTransport(client, oversized).Do(context.Background(), req)
		if !errors.Is(err, ErrInvalidSignedContext) {
			t.Fatalf("error = %v, want ErrInvalidSignedContext", err)
		}
		var requestErr *RequestError
		if !errors.As(err, &requestErr) || requestErr.Delivery != DeliveryNotSent {
			t.Fatalf("error = %#v, want DeliveryNotSent", err)
		}
		if calls.Load() != 0 {
			t.Fatalf("dispatched %d invalid requests", calls.Load())
		}
	})
}

func TestTransportContractProtocolErrorsCursorAndBodyClosure(t *testing.T) {
	for _, tt := range []struct {
		name        string
		statusCode  int
		contentType string
		body        string
		mutate      func(*TransportRequest)
		wantErr     error
		wantCursor  string
	}{
		{
			name:        "unsupported content type",
			statusCode:  http.StatusOK,
			contentType: "text/plain",
			body:        "remote-secret-payload",
			wantErr:     ErrUnsupportedContentType,
		},
		{
			name:        "server request",
			statusCode:  http.StatusOK,
			contentType: "application/json",
			body:        `{"jsonrpc":"2.0","id":7,"method":"roots/list"}`,
			wantErr:     ErrUnsupportedServerRequest,
		},
		{
			name:        "multiple json values",
			statusCode:  http.StatusOK,
			contentType: "application/json",
			body:        `{"jsonrpc":"2.0","id":7,"result":{}} {}`,
			wantErr:     errInvalidRPCResponse,
		},
		{
			name:       "session 404",
			statusCode: http.StatusNotFound,
			body:       "remote-secret-payload",
			mutate: func(req *TransportRequest) {
				req.Session = sessionState{ID: "session-1", ProtocolVersion: protocolVersion}
			},
			wantErr: ErrSessionInvalid,
		},
		{
			name:        "sse interruption retains cursor",
			statusCode:  http.StatusOK,
			contentType: "text/event-stream",
			body:        "id: event-1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n",
			wantErr:     ErrSSEInterrupted,
			wantCursor:  "event-1",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := &closeTrackingBody{reader: strings.NewReader(tt.body)}
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				notifyWroteRequest(req, nil)
				return &http.Response{
					StatusCode: tt.statusCode,
					Header:     http.Header{"Content-Type": []string{tt.contentType}},
					Body:       body,
				}, nil
			})}
			request := contractRequest("https://mcp.invalid/path?query-secret")
			if tt.mutate != nil {
				tt.mutate(&request)
			}
			response, err := newHTTPTransport(client, nil).Do(context.Background(), request)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			var requestErr *RequestError
			if !errors.As(err, &requestErr) || requestErr.Delivery != DeliverySent {
				t.Fatalf("error = %#v, want DeliverySent", err)
			}
			if response.LastEventID != tt.wantCursor {
				t.Fatalf("cursor = %q, want %q", response.LastEventID, tt.wantCursor)
			}
			if !body.closed.Load() {
				t.Fatal("response body was not closed")
			}
			for _, secret := range []string{"query-secret", "remote-secret-payload"} {
				if strings.Contains(err.Error(), secret) || strings.Contains(SafeErrorSummary(err), secret) {
					t.Fatalf("secret %q leaked through error: %v", secret, err)
				}
			}
		})
	}
}

func TestTransportContractSession404PrecedesBodyFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		reader io.Reader
	}{
		{name: "oversized body", reader: strings.NewReader(strings.Repeat("x", int(maxResponseBytes)+1))},
		{name: "failing body", reader: failingSSEReader{err: errors.New("body-reader-secret")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := &closeTrackingBody{reader: tt.reader}
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				notifyWroteRequest(req, nil)
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       body,
				}, nil
			})}
			request := contractRequest("https://mcp.invalid/session-404-secret")
			request.Session = sessionState{ID: "session-1", ProtocolVersion: protocolVersion}
			_, err := newHTTPTransport(client, nil).Do(context.Background(), request)
			if !errors.Is(err, ErrSessionInvalid) {
				t.Fatalf("error = %v, want ErrSessionInvalid", err)
			}
			var requestErr *RequestError
			if !errors.As(err, &requestErr) || requestErr.Operation != OperationListTools ||
				requestErr.Delivery != DeliverySent || requestErr.StatusCode != http.StatusNotFound ||
				requestErr.Class != ClientErrorProtocol {
				t.Fatalf("request error = %#v", requestErr)
			}
			if !body.closed.Load() {
				t.Fatal("404 response body was not closed")
			}
			for _, secret := range []string{"session-404-secret", "body-reader-secret"} {
				if strings.Contains(err.Error(), secret) || strings.Contains(SafeErrorSummary(err), secret) {
					t.Fatalf("secret %q leaked through error: %v", secret, err)
				}
			}
		})
	}
}

func TestTransportContractSignsPOSTGETAndDELETEImmediatelyBeforeDispatch(t *testing.T) {
	signer := &incrementingSigner{}
	var mu sync.Mutex
	methods := make([]string, 0, 3)
	tokens := make([]string, 0, 3)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		notifyWroteRequest(req, nil)
		mu.Lock()
		methods = append(methods, req.Method)
		tokens = append(tokens, req.Header.Get("X-DEEIX-Context"))
		mu.Unlock()
		if req.Method == http.MethodDelete {
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Header:     http.Header{},
				Body:       http.NoBody,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":7,"result":{}}`)),
		}, nil
	})}
	transport := newHTTPTransport(client, signer)
	signed := &SignedContextConfig{KeyID: "ctx_test"}

	post := contractRequest("https://mcp.invalid")
	post.SignedContext = signed
	get := contractRequest("https://mcp.invalid")
	get.Operation = OperationResumeSSE
	get.HTTPMethod = http.MethodGet
	get.Body = nil
	get.Session = sessionState{ID: "session-1", ProtocolVersion: protocolVersion}
	get.LastEventID = "event-1"
	get.SignedContext = signed
	deleteRequest := contractRequest("https://mcp.invalid")
	deleteRequest.Operation = OperationTerminate
	deleteRequest.HTTPMethod = http.MethodDelete
	deleteRequest.Body = nil
	deleteRequest.RequestID = nil
	deleteRequest.Session = sessionState{ID: "session-1", ProtocolVersion: protocolVersion}
	deleteRequest.SignedContext = signed

	for _, request := range []TransportRequest{post, get, deleteRequest} {
		if _, err := transport.Do(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(methods, []string{http.MethodPost, http.MethodGet, http.MethodDelete}) {
		t.Fatalf("methods = %v", methods)
	}
	if len(tokens) != 3 || tokens[0] == "" || tokens[1] == "" || tokens[2] == "" ||
		tokens[0] == tokens[1] || tokens[1] == tokens[2] || tokens[0] == tokens[2] {
		t.Fatalf("signed tokens = %v", tokens)
	}
	signer.mu.Lock()
	defer signer.mu.Unlock()
	if len(signer.inputs) != 3 {
		t.Fatalf("signer calls = %d, want 3", len(signer.inputs))
	}
}

func TestTransportContractTLSPolicyFailureIsDeterministicAndNotRetried(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached an untrusted TLS server")
	}))
	defer server.Close()

	transport := newHTTPTransport(&http.Client{Timeout: time.Second}, nil)
	_, err := transport.Do(context.Background(), contractRequest(server.URL))
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Class != ClientErrorNetwork {
		t.Fatalf("error = %#v, want network RequestError", err)
	}
	if !errors.Is(err, errTLSPolicyFailure) {
		t.Fatalf("error = %#v, want safe TLS-policy classification", err)
	}
	decision := (ClassifiedRetryPolicy{}).Decide(RetryInput{
		Operation: OperationListTools,
		Attempt:   0,
		Budget:    3,
		Delivery:  requestErr.Delivery,
		Err:       err,
	})
	if decision != RetryStop {
		t.Fatalf("decision = %v, want %v", decision, RetryStop)
	}
	for _, forbidden := range []string{server.URL, "x509", "certificate", "unknown authority"} {
		if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(forbidden)) ||
			strings.Contains(strings.ToLower(SafeErrorSummary(err)), strings.ToLower(forbidden)) {
			t.Fatalf("TLS detail %q leaked through error: %v", forbidden, err)
		}
	}
}
