package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DEEIX-AI/DEEIX-Chat/demo/go-mcp-server/internal/config"
	"github.com/DEEIX-AI/DEEIX-Chat/demo/go-mcp-server/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testServerBearer = "0123456789abcdef0123456789abcdef"

var testServerNow = time.Date(2026, time.July, 13, 12, 34, 56, 0, time.UTC)

func TestSafeSDKErrorBoundaryHTTPStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, "mcp.bad_request"},
		{http.StatusForbidden, "mcp.forbidden"},
		{http.StatusNotFound, "mcp.session_not_found"},
		{http.StatusMethodNotAllowed, "http.method_not_allowed"},
		{http.StatusConflict, "mcp.stream_conflict"},
		{http.StatusUnsupportedMediaType, "mcp.unsupported_media_type"},
		{http.StatusTeapot, "mcp.request_rejected"},
		{http.StatusServiceUnavailable, "mcp.internal_error"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			t.Parallel()
			handler := safeSDKErrorBoundary(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-Reflected", "hostile-marker")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("hostile-marker"))
			}))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "http://localhost/mcp", nil))

			if recorder.Code != tt.status || recorder.Body.String() != tt.code+"\n" {
				t.Fatalf("response = %d %q, want %d %q", recorder.Code, recorder.Body.String(), tt.status, tt.code+"\n")
			}
			if recorder.Header().Get("X-Reflected") != "" {
				t.Fatal("reflected SDK Header was not discarded")
			}
		})
	}
}

func TestHealthAndHTTPServerDefaults(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	response := performRequest(server.Handler(), http.MethodGet, "/healthz", nil, nil)
	if response.Code != http.StatusOK || response.Body.String() != "ok" {
		t.Fatalf("health response = %d %q", response.Code, response.Body.String())
	}
	if server.httpServer.Addr != "127.0.0.1:8090" || server.httpServer.Handler != server.Handler() {
		t.Fatalf("http server wiring = %#v", server.httpServer)
	}
	if server.httpServer.ReadHeaderTimeout != 5*time.Second ||
		server.httpServer.ReadTimeout != 10*time.Second ||
		server.httpServer.IdleTimeout != 60*time.Second ||
		server.httpServer.WriteTimeout != 0 ||
		server.httpServer.MaxHeaderBytes != 32<<10 ||
		server.shutdownTimeout != 5*time.Second {
		t.Fatalf("unsafe HTTP defaults: %#v, shutdown %v", server.httpServer, server.shutdownTimeout)
	}
}

func TestNewAcceptsValidatedLeadingZeroLoopbackPort(t *testing.T) {
	t.Parallel()

	server, err := New(config.Config{
		Addr:                "127.0.0.1:08090",
		BearerToken:         testServerBearer,
		SignedContextHeader: "X-MCP-CLIENT-SIGNED-CONTEXT",
	}, slog.New(slog.DiscardHandler), time.Now)
	if err != nil || server == nil {
		t.Fatalf("New(validated leading-zero port) = %#v, %v", server, err)
	}
}

func TestBearerGuardAndAuthenticationFailures(t *testing.T) {
	t.Parallel()

	handler := newTestServer(t).Handler()
	tests := []struct {
		name    string
		headers http.Header
		code    string
	}{
		{name: "missing", headers: http.Header{}, code: "auth.invalid_bearer"},
		{name: "malformed", headers: http.Header{"Authorization": {"Basic hostile-marker"}}, code: "auth.invalid_bearer"},
		{name: "empty token", headers: http.Header{"Authorization": {"Bearer "}}, code: "auth.invalid_bearer"},
		{name: "duplicate", headers: http.Header{"Authorization": {"Bearer " + testServerBearer}, "authorization": {"Bearer hostile-marker"}}, code: "auth.invalid_bearer"},
		{name: "wrong", headers: http.Header{"Authorization": {"Bearer hostile-marker"}}, code: "auth.invalid_bearer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := performRequest(handler, http.MethodPost, "/mcp", strings.NewReader("{}"), tt.headers)
			if response.Code != http.StatusUnauthorized || response.Body.String() != tt.code+"\n" {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "hostile-marker") {
				t.Fatal("authentication response reflected input")
			}
		})
	}

	invalidIdentity := http.Header{
		"Authorization":           {"Bearer " + testServerBearer},
		"X-MCP-CLIENT-USER-EMAIL": {"hostile-marker\x00"},
	}
	response := performRequest(handler, http.MethodPost, "/mcp", strings.NewReader("{}"), invalidIdentity)
	if response.Code != http.StatusBadRequest || response.Body.String() != "identity.headers_invalid\n" {
		t.Fatalf("identity response = %d %q", response.Code, response.Body.String())
	}
}

func TestOriginMethodProtocolAndBodyGuardPrecedence(t *testing.T) {
	t.Parallel()

	handler := newTestServer(t).Handler()
	tests := []struct {
		name    string
		method  string
		headers http.Header
		body    io.Reader
		status  int
		code    string
	}{
		{
			name: "origin before method and auth", method: http.MethodPut,
			headers: http.Header{"Origin": {"https://hostile-marker.example"}},
			status:  http.StatusForbidden, code: "http.origin_denied",
		},
		{name: "method before auth", method: http.MethodPut, status: http.StatusMethodNotAllowed, code: "http.method_not_allowed"},
		{
			name: "protocol before auth", method: http.MethodPost,
			headers: http.Header{"Mcp-Protocol-Version": {"hostile-marker"}},
			status:  http.StatusBadRequest, code: "mcp.unsupported_protocol",
		},
		{
			name: "duplicate protocol", method: http.MethodPost,
			headers: http.Header{"Mcp-Protocol-Version": {ProtocolVersion}, "mcp-protocol-version": {ProtocolVersion}},
			status:  http.StatusBadRequest, code: "mcp.unsupported_protocol",
		},
		{
			name: "session requires protocol", method: http.MethodGet,
			headers: http.Header{"Mcp-Session-Id": {"hostile-marker"}},
			status:  http.StatusBadRequest, code: "mcp.unsupported_protocol",
		},
		{
			name: "auth before body", method: http.MethodPost,
			body:   strings.NewReader(strings.Repeat("x", (1<<20)+1)),
			status: http.StatusUnauthorized, code: "auth.invalid_bearer",
		},
		{
			name: "bounded body", method: http.MethodPost,
			headers: http.Header{"Authorization": {"Bearer " + testServerBearer}},
			body:    strings.NewReader(strings.Repeat("x", (1<<20)+1)),
			status:  http.StatusRequestEntityTooLarge, code: "http.request_too_large",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := performRequest(handler, tt.method, "/mcp", tt.body, tt.headers)
			if response.Code != tt.status || response.Body.String() != tt.code+"\n" {
				t.Fatalf("response = %d %q, want %d %q", response.Code, response.Body.String(), tt.status, tt.code+"\n")
			}
			if strings.Contains(response.Body.String(), "hostile-marker") {
				t.Fatal("guard response reflected input")
			}
		})
	}
}

func TestBodyReadFailureIsSafe(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", nil)
	request.Host = "localhost"
	request.Header.Set("Authorization", "Bearer "+testServerBearer)
	request.Body = &failingReadCloser{}
	recorder := httptest.NewRecorder()
	newTestServer(t).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || recorder.Body.String() != "mcp.bad_request\n" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestScrubSensitiveHeadersClonesAndRemovesCaseInsensitively(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", nil)
	request.Header = http.Header{
		"authorization":                       {"Bearer hostile-bearer"},
		"x-mcp-client-signed-context":         {"hostile-jwt"},
		"x-mcp-client-user-email":             {"hostile@example.test"},
		"X-MCP-CLIENT-CONVERSATION-PUBLIC-ID": {"hostile-conversation"},
		"Mcp-Protocol-Version":                {ProtocolVersion},
	}
	original := request.Header.Clone()
	captured := make(http.Header)
	handler := scrubSensitiveHeaders("X-MCP-CLIENT-SIGNED-CONTEXT", http.HandlerFunc(func(_ http.ResponseWriter, got *http.Request) {
		captured = got.Header.Clone()
	}))
	handler.ServeHTTP(httptest.NewRecorder(), request)
	for _, name := range append([]string{"Authorization", "X-MCP-CLIENT-SIGNED-CONTEXT"}, identity.PlainHeaderNames()...) {
		if matchingTestHeaderValues(captured, name) != 0 {
			t.Fatalf("captured Header retained %q: %#v", name, captured)
		}
	}
	if captured.Get("Mcp-Protocol-Version") != ProtocolVersion {
		t.Fatalf("protocol Header was removed: %#v", captured)
	}
	if fmt.Sprint(request.Header) != fmt.Sprint(original) {
		t.Fatalf("original request mutated: got %#v want %#v", request.Header, original)
	}
}

func TestIdentityCheckReturnsOnlySnapshotAndSafeContent(t *testing.T) {
	t.Parallel()

	marker := "identity-pii-marker"
	snapshot := identity.Snapshot{
		Identity:       identity.HeaderIdentity{UserEmail: marker},
		SignedIdentity: &identity.SignedIdentity{Subject: marker, Mode: "chat"},
		Verification:   identity.Verification{Present: true, Configured: true, Valid: true, Verified: true, Reason: "signed_context_verified"},
		Mismatches:     []string{},
	}
	request := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{Extra: map[string]any{
		identity.SnapshotExtraKey: snapshot,
	}}}}
	result, output, err := identityCheck(func() time.Time { return testServerNow })(t.Context(), request, IdentityCheckInput{})
	if err != nil {
		t.Fatalf("identityCheck(): %v", err)
	}
	if !output.Authenticated || output.CheckedAt != "2026-07-13T12:34:56Z" || output.Identity.UserEmail != marker ||
		output.SignedIdentity == nil || output.SignedIdentity.Subject != marker || !output.Verification.Verified || output.Mismatches == nil {
		t.Fatalf("output = %#v", output)
	}
	contentJSON, marshalErr := json.Marshal(result.Content)
	if marshalErr != nil {
		t.Fatalf("json.Marshal(content): %v", marshalErr)
	}
	if strings.Contains(string(contentJSON), marker) || !strings.Contains(string(contentJSON), "identity_check") {
		t.Fatalf("unsafe tool content = %s", contentJSON)
	}
	outputJSON, marshalErr := json.Marshal(output)
	if marshalErr != nil || !bytes.Contains(outputJSON, []byte("\"mismatches\":[]")) {
		t.Fatalf("output JSON = %s, %v", outputJSON, marshalErr)
	}
}

func TestIdentityCheckRejectsMissingSnapshotBridge(t *testing.T) {
	t.Parallel()

	_, output, err := identityCheck(func() time.Time { return testServerNow })(t.Context(), &mcp.CallToolRequest{}, IdentityCheckInput{})
	if err == nil || output.Authenticated {
		t.Fatalf("identityCheck() = %#v, %v", output, err)
	}
}

func TestJSONRPCErrorSanitization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code    int
		message string
	}{
		{-32700, "mcp.parse_error"},
		{-32600, "mcp.invalid_request"},
		{-32601, "mcp.method_not_found"},
		{-32602, "mcp.invalid_params"},
		{-32603, "mcp.internal_error"},
		{-32042, "mcp.request_failed"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.code), func(t *testing.T) {
			t.Parallel()
			body := fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":\"request-id\",\"error\":{\"code\":%d,\"message\":\"hostile-marker\",\"data\":{\"secret\":\"hostile-marker\"}}}", tt.code)
			response := serveBoundary(t, http.StatusOK, "application/json", body)
			want := fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":\"request-id\",\"error\":{\"code\":%d,\"message\":%q}}", tt.code, tt.message)
			if response.Code != http.StatusOK || response.Body.String() != want {
				t.Fatalf("response = %d %q, want 200 %q", response.Code, response.Body.String(), want)
			}
			if strings.Contains(response.Body.String(), "hostile-marker") || response.Header().Get("X-Reflected") != "" {
				t.Fatal("JSON-RPC error reflected hostile data")
			}
		})
	}

	allowed := serveBoundary(t, http.StatusOK, "application/json", "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32602,\"message\":\"mcp.unsupported_protocol\"}}")
	if !strings.Contains(allowed.Body.String(), "mcp.unsupported_protocol") {
		t.Fatalf("allowlisted protocol error = %q", allowed.Body.String())
	}
}

func TestToolErrorSanitization(t *testing.T) {
	t.Parallel()

	body := "{\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"_meta\":{\"secret\":\"hostile-marker\"},\"content\":[{\"type\":\"text\",\"text\":\"hostile-marker\"}],\"structuredContent\":{\"secret\":\"hostile-marker\"},\"isError\":true}}"
	response := serveBoundary(t, http.StatusOK, "application/json", body)
	want := "{\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"mcp.tool_error\"}],\"isError\":true}}"
	if response.Code != http.StatusOK || response.Body.String() != want {
		t.Fatalf("response = %d %q, want 200 %q", response.Code, response.Body.String(), want)
	}
	if strings.Contains(response.Body.String(), "hostile-marker") || response.Header().Get("X-Reflected") != "" {
		t.Fatal("tool error reflected hostile data")
	}
}

func TestSafeSDKErrorBoundarySuccessAndEmptyResponses(t *testing.T) {
	t.Parallel()

	jsonBody := " {\n  \"jsonrpc\": \"2.0\", \"id\": 1, \"result\": {\"ok\": true}\n} "
	response := serveBoundary(t, http.StatusOK, "application/json; charset=utf-8", jsonBody)
	if response.Code != http.StatusOK || response.Body.String() != jsonBody || response.Header().Get("X-Reflected") != "success-header" {
		t.Fatalf("successful JSON changed: %d %q %#v", response.Code, response.Body.String(), response.Header())
	}

	for _, status := range []int{http.StatusAccepted, http.StatusNoContent} {
		response = serveBoundary(t, status, "", "")
		if response.Code != status || response.Body.Len() != 0 {
			t.Fatalf("empty %d response = %d %q", status, response.Code, response.Body.String())
		}
	}
}

func TestSafeSDKErrorBoundaryFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "invalid json", contentType: "application/json", body: "{hostile-marker"},
		{name: "trailing json", contentType: "application/json", body: "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}} {}"},
		{name: "unexpected media type", contentType: "text/plain", body: "hostile-marker"},
		{name: "oversized", contentType: "application/json", body: strings.Repeat("x", (1<<20)+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := serveBoundary(t, http.StatusOK, tt.contentType, tt.body)
			if response.Code != http.StatusInternalServerError || response.Body.String() != "mcp.internal_error\n" {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "hostile-marker") {
				t.Fatal("fail-closed response reflected input")
			}
		})
	}
}

func TestSafeSDKErrorBoundaryStatusPrecedesBodySize(t *testing.T) {
	t.Parallel()

	response := serveBoundary(t, http.StatusBadRequest, "text/plain", strings.Repeat("hostile-marker", 1<<17))
	if response.Code != http.StatusBadRequest || response.Body.String() != "mcp.bad_request\n" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}

func TestSafeSDKErrorBoundaryRejectsNonemptyEmptyOnlyStatuses(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusAccepted, http.StatusNoContent} {
		response := serveBoundary(t, status, "application/json", "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}")
		if response.Code != http.StatusInternalServerError || response.Body.String() != "mcp.internal_error\n" {
			t.Fatalf("nonempty %d response = %d %q", status, response.Code, response.Body.String())
		}
	}
}

func TestJSONRPCIDsAndMalformedEnvelopes(t *testing.T) {
	t.Parallel()

	validIDs := []string{"\"string-id\"", "17", "null"}
	for _, id := range validIDs {
		response := serveBoundary(t, http.StatusOK, "application/json", "{\"jsonrpc\":\"2.0\",\"id\":"+id+",\"error\":{\"code\":-32603,\"message\":\"hostile-marker\"}}")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "\"id\":"+id) {
			t.Fatalf("ID %s response = %d %q", id, response.Code, response.Body.String())
		}
	}

	invalid := []string{
		"{\"jsonrpc\":\"2.0\",\"error\":{\"code\":-32603,\"message\":\"hostile-marker\"}}",
		"{\"jsonrpc\":\"2.0\",\"id\":true,\"error\":{\"code\":-32603,\"message\":\"hostile-marker\"}}",
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32603.5,\"message\":\"hostile-marker\"}}",
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32603,\"message\":\"hostile-marker\"},\"result\":{}}",
		"{\"jsonrpc\":\"2.0\",\"id\":1}",
		"[]",
	}
	for index, body := range invalid {
		response := serveBoundary(t, http.StatusOK, "application/json", body)
		if response.Code != http.StatusInternalServerError || response.Body.String() != "mcp.internal_error\n" {
			t.Fatalf("invalid envelope %d response = %d %q", index, response.Code, response.Body.String())
		}
	}

	for _, status := range []int{http.StatusOK, http.StatusMovedPermanently} {
		response := serveBoundary(t, status, "application/json", "")
		if response.Code != http.StatusInternalServerError || response.Body.String() != "mcp.internal_error\n" {
			t.Fatalf("empty status %d response = %d %q", status, response.Code, response.Body.String())
		}
	}
}

func TestSafeSDKErrorBoundaryConcurrentWrites(t *testing.T) {
	t.Parallel()

	handler := safeSDKErrorBoundary(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var group sync.WaitGroup
		for range 32 {
			group.Go(func() {
				_, _ = w.Write([]byte("{}"))
			})
		}
		group.Wait()
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "http://localhost/mcp", nil))
	if recorder.Code != http.StatusInternalServerError || recorder.Body.String() != "mcp.internal_error\n" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestIdentityCheckOutputUsesLowerCamelNestedWireKeys(t *testing.T) {
	t.Parallel()

	output := IdentityCheckOutput{
		Verification: identity.Verification{Present: true, Configured: true, Valid: true, Verified: true, Reason: "signed_context_verified"},
		SignedIdentity: &identity.SignedIdentity{
			Subject: "subject", Mode: "chat", Name: "name", Email: "email", Role: "role",
			ConversationPublicID: "conversation", AssistantMessagePublicID: "assistant",
			UserMessagePublicID: "message", RequestID: "request", RunID: "run", TraceID: "trace",
		},
		Mismatches: []string{},
	}
	wire, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	for _, forbidden := range []string{"Present", "Configured", "Valid", "Verified", "Reason", "Subject", "Mode", "ConversationPublicID", "RequestID"} {
		if bytes.Contains(wire, []byte("\""+forbidden+"\"")) {
			t.Fatalf("wire output contains PascalCase key %q: %s", forbidden, wire)
		}
	}
	var decoded struct {
		Verification   map[string]any `json:"verification"`
		SignedIdentity map[string]any `json:"signedIdentity"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(): %v", err)
	}
	for _, key := range []string{"present", "configured", "valid", "verified", "reason"} {
		if _, ok := decoded.Verification[key]; !ok {
			t.Fatalf("verification missing %q: %s", key, wire)
		}
	}
	for _, key := range []string{"subject", "mode", "name", "email", "role", "conversationPublicID", "assistantMessagePublicID", "userMessagePublicID", "requestID", "runID", "traceID"} {
		if _, ok := decoded.SignedIdentity[key]; !ok {
			t.Fatalf("signed identity missing %q: %s", key, wire)
		}
	}
}

func TestIdentityCheckOfficialSDKLifecycleKeepsPIIOutOfTextContent(t *testing.T) {
	t.Parallel()

	server := newConfiguredTestServer(t)
	marker := "identity-pii-marker"
	identityHeaders := http.Header{
		"X-MCP-CLIENT-USER-PUBLIC-ID": {"user-public-id"},
		"X-MCP-CLIENT-USER-EMAIL":     {marker},
		"X-MCP-CLIENT-SIGNED-CONTEXT": {"invalid-jwt-marker"},
	}
	sessionID, initialize := initializeRawSession(t, server.Handler(), ProtocolVersion, identityHeaders)
	if initialize.Code != http.StatusOK || sessionID == "" {
		t.Fatalf("initialize = %d %q session %q", initialize.Code, initialize.Body.String(), sessionID)
	}
	initializeObject := decodeJSON(t, initialize.Body.Bytes())
	result, ok := initializeObject["result"].(map[string]any)
	if !ok || result["protocolVersion"] != ProtocolVersion {
		t.Fatalf("initialize result = %#v", initializeObject)
	}

	notification := postSessionJSON(t, server.Handler(), sessionID, identityHeaders,
		"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\",\"params\":{}}")
	if notification.Code != http.StatusAccepted || notification.Body.Len() != 0 {
		t.Fatalf("initialized notification = %d %q", notification.Code, notification.Body.String())
	}

	call := postSessionJSON(t, server.Handler(), sessionID, identityHeaders,
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"identity_check\",\"arguments\":{}}}")
	if call.Code != http.StatusOK {
		t.Fatalf("tool call = %d %q", call.Code, call.Body.String())
	}
	callObject := decodeJSON(t, call.Body.Bytes())
	callResult, ok := callObject["result"].(map[string]any)
	if !ok {
		t.Fatalf("tool result = %#v", callObject)
	}
	content, ok := callResult["content"].([]any)
	if !ok || len(content) != 1 || strings.Contains(fmt.Sprint(content), marker) || !strings.Contains(fmt.Sprint(content), "identity_check") {
		t.Fatalf("unsafe content = %#v", content)
	}
	structured, ok := callResult["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("structuredContent = %#v", callResult["structuredContent"])
	}
	identityObject, _ := structured["identity"].(map[string]any)
	verification, _ := structured["verification"].(map[string]any)
	if identityObject["userEmail"] != marker || verification["verified"] != false || verification["reason"] != "signed_context_invalid" {
		t.Fatalf("structured identity = %#v verification = %#v", identityObject, verification)
	}
	mismatches, ok := structured["mismatches"].([]any)
	if !ok || len(mismatches) != 0 {
		t.Fatalf("mismatches = %#v", structured["mismatches"])
	}
	checkedAt, ok := structured["checkedAt"].(string)
	parsed, parseErr := time.Parse(time.RFC3339, checkedAt)
	if !ok || parseErr != nil || parsed.Location() != time.UTC {
		t.Fatalf("checkedAt = %#v, %v", structured["checkedAt"], parseErr)
	}
}

func TestProtocolInitializeMismatchIsSanitizedJSONRPC(t *testing.T) {
	t.Parallel()

	response := initializeRaw(t, newTestServer(t).Handler(), "hostile-marker-version", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	want := "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32602,\"message\":\"mcp.unsupported_protocol\"}}"
	if response.Body.String() != want || response.Header().Get("Mcp-Session-Id") != "" || strings.Contains(response.Body.String(), "hostile-marker") {
		t.Fatalf("response = %q headers %#v", response.Body.String(), response.Header())
	}
}

func TestJSONRPCOfficialSDKErrorsAreSanitized(t *testing.T) {
	t.Parallel()

	handler := newTestServer(t).Handler()
	sessionID, response := initializeRawSession(t, handler, ProtocolVersion, nil)
	if response.Code != http.StatusOK || sessionID == "" {
		t.Fatalf("initialize = %d %q session %q", response.Code, response.Body.String(), sessionID)
	}
	response = postSessionJSON(t, handler, sessionID, nil,
		"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\",\"params\":{}}")
	if response.Code != http.StatusAccepted || response.Body.Len() != 0 {
		t.Fatalf("initialized = %d %q", response.Code, response.Body.String())
	}

	tests := []struct {
		name        string
		body        string
		status      int
		message     string
		withoutSess bool
		headers     http.Header
	}{
		{
			name: "unknown tool", body: "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"hostile-tool-marker\",\"arguments\":{}}}",
			status: http.StatusOK, message: "mcp.invalid_params",
		},
		{
			name: "invalid typed arguments become tool error", body: "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/call\",\"params\":{\"name\":\"identity_check\",\"arguments\":{\"hostile-param-marker\":true}}}",
			status: http.StatusOK, message: "mcp.tool_error",
		},
		{
			name: "known unimplemented method", body: "{\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"resources/subscribe\",\"params\":{\"uri\":\"https://hostile-resource-marker.example\"}}",
			status: http.StatusOK, message: "mcp.method_not_found",
		},
		{
			name: "arbitrary unknown method is HTTP bad request", body: "{\"jsonrpc\":\"2.0\",\"id\":6,\"method\":\"hostile-method-marker\",\"params\":{}}",
			status: http.StatusBadRequest, message: "mcp.bad_request",
		},
		{
			name: "post last event id", body: "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"ping\",\"params\":{}}",
			status: http.StatusBadRequest, message: "mcp.bad_request", headers: http.Header{"Last-Event-Id": {"hostile-event-marker"}},
		},
		{
			name: "batch", body: "[{\"jsonrpc\":\"2.0\",\"id\":8,\"method\":\"ping\",\"params\":{}}]",
			status: http.StatusBadRequest, message: "mcp.bad_request",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := tt.headers.Clone()
			var got *httptest.ResponseRecorder
			if tt.withoutSess {
				got = performRequest(handler, http.MethodPost, "/mcp", strings.NewReader(tt.body), mcpRequestHeaders(headers))
			} else {
				got = postSessionJSON(t, handler, sessionID, headers, tt.body)
			}
			if got.Code != tt.status || !strings.Contains(got.Body.String(), tt.message) {
				t.Fatalf("response = %d %q, want %d containing %q", got.Code, got.Body.String(), tt.status, tt.message)
			}
			for _, marker := range []string{"hostile-tool-marker", "hostile-param-marker", "hostile-resource-marker", "hostile-method-marker", "hostile-event-marker"} {
				if strings.Contains(got.Body.String(), marker) || headerContains(got.Header(), marker) {
					t.Fatalf("response reflected %q: %q %#v", marker, got.Body.String(), got.Header())
				}
			}
		})
	}
}

func TestSafeSDKHTTPErrorFamiliesAndSessionBinding(t *testing.T) {
	t.Parallel()

	handler := newTestServer(t).Handler()
	tests := []struct {
		name    string
		method  string
		headers http.Header
		body    string
		status  int
		code    string
	}{
		{
			name: "unsupported content type", method: http.MethodPost,
			headers: http.Header{"Content-Type": {"text/hostile-marker"}}, body: "{}",
			status: http.StatusUnsupportedMediaType, code: "mcp.unsupported_media_type",
		},
		{
			name: "invalid accept", method: http.MethodPost,
			headers: http.Header{"Accept": {"application/hostile-marker"}, "Content-Type": {"application/json"}}, body: "{}",
			status: http.StatusBadRequest, code: "mcp.bad_request",
		},
		{
			name: "unknown session", method: http.MethodGet,
			headers: http.Header{"Accept": {"text/event-stream"}, "Mcp-Protocol-Version": {ProtocolVersion}, "Mcp-Session-Id": {"hostile-session-marker"}},
			status:  http.StatusNotFound, code: "mcp.session_not_found",
		},
		{
			name: "get without session", method: http.MethodGet,
			headers: http.Header{"Accept": {"text/event-stream"}},
			status:  http.StatusBadRequest, code: "mcp.bad_request",
		},
		{
			name: "delete without session", method: http.MethodDelete,
			status: http.StatusBadRequest, code: "mcp.bad_request",
		},
		{
			name: "empty post", method: http.MethodPost,
			headers: http.Header{"Content-Type": {"application/json"}},
			status:  http.StatusBadRequest, code: "mcp.bad_request",
		},
		{
			name: "malformed post", method: http.MethodPost,
			headers: http.Header{"Content-Type": {"application/json"}}, body: "{hostile-json-marker",
			status: http.StatusBadRequest, code: "mcp.bad_request",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := mcpRequestHeaders(tt.headers)
			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			response := performRequest(handler, tt.method, "/mcp", body, headers)
			if response.Code != tt.status || response.Body.String() != tt.code+"\n" {
				t.Fatalf("response = %d %q, want %d %q", response.Code, response.Body.String(), tt.status, tt.code+"\n")
			}
			for _, marker := range []string{"hostile-marker", "hostile-session-marker", "hostile-json-marker"} {
				if strings.Contains(response.Body.String(), marker) || headerContains(response.Header(), marker) {
					t.Fatalf("response reflected %q: %q %#v", marker, response.Body.String(), response.Header())
				}
			}
		})
	}

	plainA := http.Header{"X-MCP-CLIENT-USER-PUBLIC-ID": {"user-a"}}
	sessionID, initialized := initializeRawSession(t, handler, ProtocolVersion, plainA)
	if initialized.Code != http.StatusOK || sessionID == "" {
		t.Fatalf("initialize = %d %q session %q", initialized.Code, initialized.Body.String(), sessionID)
	}
	plainB := http.Header{"X-MCP-CLIENT-USER-PUBLIC-ID": {"hostile-user-b"}}
	mismatch := postSessionJSON(t, handler, sessionID, plainB, "{\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"ping\",\"params\":{}}")
	if mismatch.Code != http.StatusForbidden || mismatch.Body.String() != "mcp.forbidden\n" || strings.Contains(mismatch.Body.String(), "hostile-user-b") {
		t.Fatalf("session mismatch = %d %q", mismatch.Code, mismatch.Body.String())
	}
}

func TestSafeSDKValidDeleteReturnsEmptyNoContent(t *testing.T) {
	t.Parallel()

	handler := newTestServer(t).Handler()
	sessionID, initialized := initializeRawSession(t, handler, ProtocolVersion, nil)
	if initialized.Code != http.StatusOK || sessionID == "" {
		t.Fatalf("initialize = %d %q session %q", initialized.Code, initialized.Body.String(), sessionID)
	}
	headers := mcpRequestHeaders(nil)
	headers.Set("Mcp-Session-Id", sessionID)
	response := performRequest(handler, http.MethodDelete, "/mcp", nil, headers)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("delete = %d %q", response.Code, response.Body.String())
	}
}

func TestSafeSDKRealGETSSEPassthrough(t *testing.T) {
	t.Parallel()

	handler := newTestServer(t).Handler()
	sessionID, initialized := initializeRawSession(t, handler, ProtocolVersion, nil)
	if initialized.Code != http.StatusOK || sessionID == "" {
		t.Fatalf("initialize = %d %q session %q", initialized.Code, initialized.Body.String(), sessionID)
	}

	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, testServer.URL+"/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext(): %v", err)
	}
	request.Host = "localhost"
	request.Header = mcpRequestHeaders(http.Header{
		"Accept":         {"text/event-stream"},
		"Mcp-Session-Id": {sessionID},
	})
	response, err := testServer.Client().Do(request)
	if err != nil {
		t.Fatalf("GET SSE: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("GET SSE = %d %q", response.StatusCode, body)
	}
	if mediaType := response.Header.Get("Content-Type"); !strings.HasPrefix(mediaType, "text/event-stream") {
		t.Fatalf("Content-Type = %q", mediaType)
	}
	if headerContains(response.Header, "hostile") {
		t.Fatalf("SSE headers reflected marker: %#v", response.Header)
	}
}

func TestHTTPServerMaxHeaderBytesUsesGo431ParserResponse(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(): %v", err)
	}
	httpServer := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           server.Handler(),
		ReadHeaderTimeout: server.httpServer.ReadHeaderTimeout,
		ReadTimeout:       server.httpServer.ReadTimeout,
		WriteTimeout:      server.httpServer.WriteTimeout,
		IdleTimeout:       server.httpServer.IdleTimeout,
		MaxHeaderBytes:    server.httpServer.MaxHeaderBytes,
	}
	go func() {
		_ = httpServer.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = httpServer.Close()
	})

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer conn.Close()
	_, err = fmt.Fprintf(conn, "GET /healthz HTTP/1.1\r\nHost: localhost\r\nX-Oversized: %s\r\n\r\n", strings.Repeat("h", 64<<10))
	if err != nil {
		t.Fatalf("write request: %v", err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("ReadAll(): %v", err)
	}
	text := string(raw)
	if !strings.HasPrefix(text, "HTTP/1.1 431 ") || !strings.HasSuffix(text, "\r\n\r\n431 Request Header Fields Too Large") {
		t.Fatalf("431 parser response = %q", text)
	}
	if strings.Contains(text, "nosniff") || strings.Contains(text, "hhostile") {
		t.Fatalf("unexpected 431 response headers/body = %q", text)
	}
}

func TestSafeSDKLocalhostHostRejectionDoesNotReflectHost(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader("{}"))
	request.Host = "hostile-host-marker.example"
	request.Header = mcpRequestHeaders(nil)
	local := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8090}
	request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, local))
	recorder := httptest.NewRecorder()
	newTestServer(t).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || recorder.Body.String() != "mcp.forbidden\n" ||
		strings.Contains(recorder.Body.String(), "hostile-host-marker") || headerContains(recorder.Header(), "hostile-host-marker") {
		t.Fatalf("response = %d %q %#v", recorder.Code, recorder.Body.String(), recorder.Header())
	}
}

func TestSafeSDKErrorBoundarySSEFlushPassthrough(t *testing.T) {
	t.Parallel()

	unwrapped := false
	handler := safeSDKErrorBoundary(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(interface{ FlushError() error }); ok {
			panic("safe writer unexpectedly implements FlushError")
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok || wrapper.Unwrap() == nil {
			panic("safe writer does not unwrap")
		}
		unwrapped = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, "event: first\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "event: second\n\n")
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://localhost/mcp", nil))
	if !unwrapped || !recorder.Flushed || recorder.Code != http.StatusOK {
		t.Fatalf("SSE state = unwrap:%v flush:%v status:%d", unwrapped, recorder.Flushed, recorder.Code)
	}
	if recorder.Body.String() != "event: first\n\nevent: second\n\n" || recorder.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("SSE response = %q %#v", recorder.Body.String(), recorder.Header())
	}
}

func TestSafeSDKErrorBoundaryNonSSEFlushFailsClosed(t *testing.T) {
	t.Parallel()

	handler := safeSDKErrorBoundary(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("hostile-marker"))
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "http://localhost/mcp", nil))
	if recorder.Code != http.StatusInternalServerError || recorder.Body.String() != "mcp.internal_error\n" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func serveBoundary(t *testing.T, status int, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	handler := safeSDKErrorBoundary(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Reflected", "success-header")
		if strings.Contains(body, "hostile-marker") {
			w.Header().Set("X-Reflected", "hostile-marker")
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = io.Copy(w, bytes.NewBufferString(body))
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "http://localhost/mcp", nil))
	return recorder
}

func decodeJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("json.Unmarshal(): %v", err)
	}
	return value
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	server, err := New(config.Config{
		Addr:                "127.0.0.1:8090",
		BearerToken:         testServerBearer,
		SignedContextHeader: "X-MCP-CLIENT-SIGNED-CONTEXT",
	}, slog.New(slog.DiscardHandler), time.Now)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return server
}

func newConfiguredTestServer(t *testing.T) *Server {
	t.Helper()
	server, err := New(config.Config{
		Addr:                "127.0.0.1:8090",
		BearerToken:         testServerBearer,
		SignedContextHeader: "X-MCP-CLIENT-SIGNED-CONTEXT",
		ContextJWT: &config.ContextJWT{
			Secret:   base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
			Issuer:   "https://issuer.example",
			Audience: "deeix-mcp-demo",
			KeyID:    "test-key",
		},
	}, slog.New(slog.DiscardHandler), time.Now)
	if err != nil {
		t.Fatalf("New(configured): %v", err)
	}
	return server
}

func initializeRaw(t *testing.T, handler http.Handler, protocol string, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
		protocol,
	)
	return performRequest(handler, http.MethodPost, "/mcp", strings.NewReader(body), mcpRequestHeaders(headers))
}

func initializeRawSession(t *testing.T, handler http.Handler, protocol string, headers http.Header) (string, *httptest.ResponseRecorder) {
	t.Helper()
	response := initializeRaw(t, handler, protocol, headers)
	return response.Header().Get("Mcp-Session-Id"), response
}

func postSessionJSON(t *testing.T, handler http.Handler, sessionID string, headers http.Header, body string) *httptest.ResponseRecorder {
	t.Helper()
	merged := mcpRequestHeaders(headers)
	merged.Set("Mcp-Session-Id", sessionID)
	return performRequest(handler, http.MethodPost, "/mcp", strings.NewReader(body), merged)
}

func mcpRequestHeaders(extra http.Header) http.Header {
	headers := http.Header{
		"Authorization":        {"Bearer " + testServerBearer},
		"Content-Type":         {"application/json"},
		"Accept":               {"application/json, text/event-stream"},
		"Mcp-Protocol-Version": {ProtocolVersion},
	}
	for name, values := range extra {
		headers.Del(name)
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	return headers
}

func performRequest(handler http.Handler, method, path string, body io.Reader, headers http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://localhost"+path, body)
	request.Host = "localhost"
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func headerContains(headers http.Header, marker string) bool {
	for name, values := range headers {
		if strings.Contains(name, marker) {
			return true
		}
		for _, value := range values {
			if strings.Contains(value, marker) {
				return true
			}
		}
	}
	return false
}

func matchingTestHeaderValues(headers http.Header, name string) int {
	count := 0
	for candidate, values := range headers {
		if strings.EqualFold(candidate, name) {
			count += len(values)
		}
	}
	return count
}

type failingReadCloser struct{}

func (*failingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("hostile-marker")
}

func (*failingReadCloser) Close() error {
	return nil
}
