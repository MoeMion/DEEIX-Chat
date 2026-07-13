package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPOfficialClientLifecycle(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	transport := &recordingHeaderTransport{
		base: httpServer.Client().Transport,
		headers: http.Header{
			"Authorization":               {"Bearer " + testServerBearer},
			"Mcp-Protocol-Version":        {ProtocolVersion},
			"X-MCP-CLIENT-USER-PUBLIC-ID": {"official-client-user"},
		},
	}
	httpClient := httpServer.Client()
	httpClient.Transport = transport

	client := mcp.NewClient(&mcp.Implementation{Name: "task10-test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL + "/mcp",
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatalf("Connect(): %v", err)
	}

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools(): %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "identity_check" {
		t.Fatalf("tools = %#v", tools.Tools)
	}

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "identity_check"})
	if err != nil {
		t.Fatalf("CallTool(): %v", err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["authenticated"] != true {
		t.Fatalf("structuredContent = %#v", result.StructuredContent)
	}
	identityMap, _ := structured["identity"].(map[string]any)
	if identityMap["userPublicID"] != "official-client-user" {
		t.Fatalf("identity = %#v", identityMap)
	}
	sessionID := session.ID()
	if sessionID == "" {
		t.Fatal("official client did not retain a session ID")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if transport.deleteStatus() != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", transport.deleteStatus())
	}

	reuse := postSessionJSON(t, server.Handler(), sessionID, http.Header{}, `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`)
	if reuse.Code != http.StatusNotFound || strings.Contains(reuse.Body.String(), "official-client-user") {
		t.Fatalf("closed session reuse = %d %q", reuse.Code, reuse.Body.String())
	}
}

func TestSessionIdentityIsolation(t *testing.T) {
	t.Parallel()

	handler := newTestServer(t).Handler()
	sessionID, initialized := initializeRawSession(t, handler, ProtocolVersion, http.Header{
		"X-MCP-CLIENT-USER-PUBLIC-ID": {"identity-a-marker"},
	})
	if initialized.Code != http.StatusOK || sessionID == "" {
		t.Fatalf("initialize = %d session %q body %q", initialized.Code, sessionID, initialized.Body.String())
	}

	response := postSessionJSON(t, handler, sessionID, http.Header{
		"X-MCP-CLIENT-USER-PUBLIC-ID": {"identity-b-marker"},
	}, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if response.Code < http.StatusBadRequest {
		t.Fatalf("identity B reused identity A session: %d %q", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "identity-a-marker") || strings.Contains(response.Body.String(), "identity-b-marker") {
		t.Fatalf("identity isolation response leaked marker: %q", response.Body.String())
	}
}

func TestSessionRequiresExactProtocol(t *testing.T) {
	t.Parallel()

	handler := newTestServer(t).Handler()
	sessionID, initialized := initializeRawSession(t, handler, ProtocolVersion, nil)
	if initialized.Code != http.StatusOK || sessionID == "" {
		t.Fatalf("initialize = %d session %q body %q", initialized.Code, sessionID, initialized.Body.String())
	}

	for _, tt := range []struct {
		name     string
		protocol string
	}{
		{name: "missing"},
		{name: "old", protocol: "2025-03-26"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := mcpRequestHeaders(nil)
			headers.Set("Mcp-Session-Id", sessionID)
			if tt.protocol == "" {
				headers.Del("Mcp-Protocol-Version")
			} else {
				headers.Set("Mcp-Protocol-Version", tt.protocol)
			}
			response := performRequest(handler, http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`), headers)
			if response.Code != http.StatusBadRequest || response.Body.String() != "mcp.unsupported_protocol\n" {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestMCPRejectsOriginAndOldProtocolBeforeTool(t *testing.T) {
	t.Parallel()

	handler := newTestServer(t).Handler()
	origin := initializeRaw(t, handler, ProtocolVersion, http.Header{"Origin": {"https://origin-leak-marker.example"}})
	if origin.Code != http.StatusForbidden || origin.Body.String() != "http.origin_denied\n" {
		t.Fatalf("origin response = %d %q", origin.Code, origin.Body.String())
	}
	if strings.Contains(origin.Body.String(), "identity_check") || strings.Contains(origin.Body.String(), "origin-leak-marker") {
		t.Fatalf("origin response leaked tool/input: %q", origin.Body.String())
	}

	old := initializeRaw(t, handler, "2025-03-26", nil)
	if old.Code != http.StatusOK || !strings.Contains(old.Body.String(), `"message":"mcp.unsupported_protocol"`) {
		t.Fatalf("old protocol response = %d %q", old.Code, old.Body.String())
	}
	if strings.Contains(old.Body.String(), "identity_check") {
		t.Fatalf("old protocol reached tool path: %q", old.Body.String())
	}
}

func TestMCPReportsMissingAndInvalidSignedContext(t *testing.T) {
	t.Parallel()

	handler := newConfiguredTestServer(t).Handler()
	tests := []struct {
		name    string
		headers http.Header
		reason  string
	}{
		{name: "missing", reason: "signed_context_missing"},
		{name: "invalid", headers: http.Header{"X-MCP-CLIENT-SIGNED-CONTEXT": {"not-a-jwt-marker"}}, reason: "signed_context_invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID, initialized := initializeRawSession(t, handler, ProtocolVersion, tt.headers)
			if initialized.Code != http.StatusOK || sessionID == "" {
				t.Fatalf("initialize = %d %q session %q", initialized.Code, initialized.Body.String(), sessionID)
			}
			response := postSessionJSON(t, handler, sessionID, tt.headers, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"identity_check","arguments":{}}}`)
			if response.Code != http.StatusOK {
				t.Fatalf("call response = %d %q", response.Code, response.Body.String())
			}
			body := decodeJSON(t, response.Body.Bytes())
			result := body["result"].(map[string]any)
			structured := result["structuredContent"].(map[string]any)
			verification := structured["verification"].(map[string]any)
			if verification["verified"] != false || verification["reason"] != tt.reason {
				t.Fatalf("verification = %#v", verification)
			}
			if strings.Contains(response.Body.String(), "not-a-jwt-marker") {
				t.Fatalf("diagnostic leaked invalid JWT marker: %q", response.Body.String())
			}
		})
	}
}

func TestServeCancellationJoinsGoroutine(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- server.Serve(ctx, listener)
	}()

	url := "http://" + listener.Addr().String() + "/healthz"
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, getErr := http.Get(url)
		if getErr == nil {
			_ = response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", getErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Serve() after cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not return after cancellation")
	}
}

func TestServeReturnsListenerError(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	want := errors.New("listener-error-marker")
	err := server.Serve(context.Background(), &failingListener{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("Serve() = %v, want %v", err, want)
	}
}

type recordingHeaderTransport struct {
	base    http.RoundTripper
	headers http.Header
	mu      sync.Mutex
	deletes []int
}

func (t *recordingHeaderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	for name, values := range t.headers {
		cloned.Header.Del(name)
		for _, value := range values {
			cloned.Header.Add(name, value)
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(cloned)
	if cloned.Method == http.MethodDelete && response != nil {
		t.mu.Lock()
		t.deletes = append(t.deletes, response.StatusCode)
		t.mu.Unlock()
	}
	return response, err
}

func (t *recordingHeaderTransport) deleteStatus() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.deletes) == 0 {
		return 0
	}
	return t.deletes[len(t.deletes)-1]
}

type failingListener struct {
	err error
}

func (l *failingListener) Accept() (net.Conn, error) { return nil, l.err }
func (l *failingListener) Close() error              { return nil }
func (l *failingListener) Addr() net.Addr            { return dummyAddr("listener") }

type dummyAddr string

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }

func newBufferedLogger(buffer *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func readAllAndClose(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll(): %v", err)
	}
	return string(body)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	return string(data)
}
