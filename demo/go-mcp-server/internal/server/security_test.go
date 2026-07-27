package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DEEIX-AI/DEEIX-Chat/demo/go-mcp-server/internal/config"
	"github.com/DEEIX-AI/DEEIX-Chat/demo/go-mcp-server/internal/identity"
	"github.com/golang-jwt/jwt/v5"
)

func TestNoSensitiveDataInResponsesOrLogs(t *testing.T) {
	t.Parallel()

	const (
		hostMarker      = "leak-host-marker"
		originMarker    = "leak-origin-marker"
		lastEventMarker = "leak-last-event-marker"
		protocolMarker  = "leak-protocol-marker"
		methodMarker    = "leak-method-marker"
		nameMarker      = "leak-name-marker"
		paramsMarker    = "leak-params-marker"
		bearerMarker    = "leak-bearer-marker"
		jwtMarker       = "leak-jwt-marker"
		secretMarker    = "leak-secret-marker"
		unknownMarker   = "leak-unknown-marker"
		identityMarker  = "leak-identity-marker"
		signedMarker    = "leak-signed-marker"
		mismatchMarker  = "leak-mismatch-marker"
	)

	var logBuffer bytes.Buffer
	server, err := New(config.Config{
		Addr:                "127.0.0.1:8090",
		BearerToken:         bearerMarker + strings.Repeat("b", 32),
		SignedContextHeader: "X-MCP-CLIENT-SIGNED-CONTEXT",
		ContextJWT: &config.ContextJWT{
			Secret:   base64.RawURLEncoding.EncodeToString([]byte(secretMarker + strings.Repeat("s", 14))),
			Issuer:   "issuer",
			Audience: "audience",
			KeyID:    "key",
		},
	}, newBufferedLogger(&logBuffer), time.Now)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	handler := server.Handler()

	validSigned := signedContextForSecurityTest(t, secretMarker+strings.Repeat("s", 14), "key", "issuer", "audience", signedMarker, signedMarker)
	rows := []struct {
		name    string
		method  string
		headers http.Header
		body    string
		host    string
	}{
		{
			name: "outer origin", method: http.MethodPost,
			headers: http.Header{"Origin": {"https://" + originMarker + ".example"}},
			body:    `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		},
		{
			name: "outer protocol", method: http.MethodPost,
			headers: http.Header{"Mcp-Protocol-Version": {protocolMarker}},
			body:    `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		},
		{
			name: "outer bearer", method: http.MethodPost,
			headers: http.Header{"Authorization": {"Bearer " + bearerMarker}},
			body:    `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		},
		{
			name: "method not allowed", method: http.MethodPatch,
			headers: http.Header{"Authorization": {"Bearer " + bearerMarker + strings.Repeat("b", 32)}},
			body:    `{"jsonrpc":"2.0","id":1,"method":"` + methodMarker + `","params":{"value":"` + paramsMarker + `"}}`,
		},
		{
			name: "request too large", method: http.MethodPost,
			headers: http.Header{"Authorization": {"Bearer " + bearerMarker + strings.Repeat("b", 32)}},
			body:    strings.Repeat(paramsMarker, (1<<20)/len(paramsMarker)+1),
		},
		{
			name: "unsupported media type", method: http.MethodPost,
			headers: http.Header{
				"Authorization": {"Bearer " + bearerMarker + strings.Repeat("b", 32)},
				"Content-Type":  {"application/" + paramsMarker},
			},
			body: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"value":"` + paramsMarker + `"}}`,
		},
		{
			name: "unsupported accept", method: http.MethodPost,
			headers: http.Header{
				"Authorization": {"Bearer " + bearerMarker + strings.Repeat("b", 32)},
				"Content-Type":  {"application/json"},
				"Accept":        {"application/" + paramsMarker},
			},
			body: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"value":"` + paramsMarker + `"}}`,
		},
		{
			name: "session not found", method: http.MethodGet,
			headers: http.Header{
				"Authorization":               {"Bearer " + bearerMarker + strings.Repeat("b", 32)},
				"Accept":                      {"text/event-stream"},
				"Mcp-Protocol-Version":        {ProtocolVersion},
				"Mcp-Session-Id":              {"session-" + paramsMarker},
				"X-MCP-CLIENT-TRACE-ID":       {identityMarker},
				"X-MCP-CLIENT-REQUEST-ID":     {identityMarker},
				"X-MCP-CLIENT-USER-EMAIL":     {identityMarker + "@example.test"},
				"X-MCP-CLIENT-USER-ROLE":      {identityMarker},
				"X-MCP-CLIENT-USER-PUBLIC-ID": {identityMarker},
			},
		},
		{
			name: "sdk method", method: http.MethodPost,
			headers: mcpRequestHeaders(http.Header{
				"Authorization": {"Bearer " + bearerMarker + strings.Repeat("b", 32)},
			}),
			body: `{"jsonrpc":"2.0","id":1,"method":"` + methodMarker + `","params":{"name":"` + nameMarker + `","value":"` + paramsMarker + `"}}`,
		},
		{
			name: "sdk unknown tool", method: http.MethodPost,
			headers: mcpRequestHeaders(http.Header{
				"Authorization": {"Bearer " + bearerMarker + strings.Repeat("b", 32)},
			}),
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + nameMarker + `","arguments":{"value":"` + paramsMarker + `"}}}`,
		},
		{
			name: "invalid signed context", method: http.MethodPost,
			headers: mcpRequestHeaders(http.Header{
				"Authorization":                  {"Bearer " + bearerMarker + strings.Repeat("b", 32)},
				"X-MCP-CLIENT-SIGNED-CONTEXT":    {jwtMarker},
				"X-MCP-CLIENT-USER-PUBLIC-ID":    {identityMarker},
				"X-MCP-CLIENT-USER-DISPLAY-NAME": {identityMarker},
				"X-MCP-CLIENT-USER-EMAIL":        {identityMarker + "@example.test"},
				"X-MCP-CLIENT-USER-ROLE":         {identityMarker},
				"X-MCP-CLIENT-REQUEST-ID":        {identityMarker},
				"X-MCP-CLIENT-UNKNOWN":           {unknownMarker},
			}),
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"identity_check","arguments":{}}}`,
		},
		{
			name: "valid signed context", method: http.MethodPost,
			headers: mcpRequestHeaders(http.Header{
				"Authorization":               {"Bearer " + bearerMarker + strings.Repeat("b", 32)},
				"X-MCP-CLIENT-SIGNED-CONTEXT": {validSigned},
				"X-MCP-CLIENT-UNKNOWN":        {unknownMarker},
			}),
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"identity_check","arguments":{}}}`,
		},
		{
			name: "mismatched signed context", method: http.MethodPost,
			headers: mcpRequestHeaders(http.Header{
				"Authorization":               {"Bearer " + bearerMarker + strings.Repeat("b", 32)},
				"X-MCP-CLIENT-SIGNED-CONTEXT": {validSigned},
				"X-MCP-CLIENT-USER-PUBLIC-ID": {mismatchMarker},
				"X-MCP-CLIENT-USER-EMAIL":     {mismatchMarker + "@example.test"},
			}),
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"identity_check","arguments":{}}}`,
		},
		{
			name: "localhost host rejection", method: http.MethodPost,
			headers: mcpRequestHeaders(nil),
			body:    `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
			host:    hostMarker + ".localhost",
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			logBuffer.Reset()
			headers := row.headers.Clone()
			if headers == nil {
				headers = make(http.Header)
			}
			headers.Set("Last-Event-ID", lastEventMarker)
			host := row.host
			if host == "" {
				host = "localhost"
			}
			response := performRequestWithHost(handler, row.method, "/mcp", host, strings.NewReader(row.body), headers)
			for _, marker := range []string{
				hostMarker, originMarker, lastEventMarker, protocolMarker, methodMarker,
				nameMarker, paramsMarker, bearerMarker, jwtMarker, secretMarker, unknownMarker,
			} {
				assertMarkerAbsentFromResponseAndLogs(t, response, logBuffer.String(), marker)
			}
			for _, marker := range []string{identityMarker, mismatchMarker} {
				assertMarkerAbsentFromHeadersAndLogs(t, response.Header(), logBuffer.String(), marker)
				assertBodyMarkerOnlyAtJSONPathPrefix(t, response, marker, "$.result.structuredContent.identity.")
			}
			for _, marker := range []string{signedMarker} {
				assertMarkerAbsentFromHeadersAndLogs(t, response.Header(), logBuffer.String(), marker)
				assertBodyMarkerOnlyAtJSONPathPrefix(t, response, marker, "$.result.structuredContent.signedIdentity.")
			}
		})
	}

	t.Run("safe sdk stream conflict status", func(t *testing.T) {
		logBuffer.Reset()
		response := serveBoundary(t, http.StatusConflict, "application/json", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"`+paramsMarker+`","data":{"secret":"`+secretMarker+`"}}}`)
		for _, marker := range []string{paramsMarker, secretMarker} {
			assertMarkerAbsentFromResponseAndLogs(t, response, logBuffer.String(), marker)
		}
		if response.Code != http.StatusConflict || response.Body.String() != "mcp.stream_conflict\n" {
			t.Fatalf("stream conflict response = %d %q", response.Code, response.Body.String())
		}
	})

	t.Run("max header bytes parser response", func(t *testing.T) {
		logBuffer.Reset()
		text := oversizedHeaderParserResponse(t, server, hostMarker, paramsMarker)
		for _, marker := range []string{hostMarker, paramsMarker} {
			assertMarkerAbsent(t, "parser response", text, marker)
			assertMarkerAbsent(t, "slog output", logBuffer.String(), marker)
		}
		if !strings.HasPrefix(text, "HTTP/1.1 431 ") {
			t.Fatalf("parser response = %q", text)
		}
	})
}

func TestPlainHeadersAreDiagnosticOnly(t *testing.T) {
	t.Parallel()

	handler := newConfiguredTestServer(t).Handler()
	sessionID, initialized := initializeRawSession(t, handler, ProtocolVersion, http.Header{
		"X-MCP-CLIENT-USER-PUBLIC-ID": {"plain-only-user"},
	})
	if initialized.Code != http.StatusOK || sessionID == "" {
		t.Fatalf("initialize = %d %q", initialized.Code, initialized.Body.String())
	}
	response := postSessionJSON(t, handler, sessionID, http.Header{
		"X-MCP-CLIENT-USER-PUBLIC-ID": {"plain-only-user"},
	}, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"identity_check","arguments":{}}}`)
	body := decodeJSON(t, response.Body.Bytes())
	result := body["result"].(map[string]any)
	structured := result["structuredContent"].(map[string]any)
	verification := structured["verification"].(map[string]any)
	if verification["verified"] != false || verification["reason"] != "signed_context_missing" {
		t.Fatalf("plain Header verified signed context: %#v", verification)
	}
}

func TestSecurityScannerRejectsIdentityMarkersOutsideStructuredIdentity(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"verification":{"reason":"leak-identity-marker"}}}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"mismatches":["leak-identity-marker"]}}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"_meta":{"secret":"leak-identity-marker"},"structuredContent":{"identity":{"userPublicID":"allowed-leak-identity-marker"}}}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"message":"leak-identity-marker"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			response := httptest.NewRecorder()
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusOK)
			_, _ = response.WriteString(body)
			if !bodyMarkerViolatesJSONPathPrefix(response, "leak-identity-marker", "$.result.structuredContent.identity.") {
				t.Fatalf("scanner accepted forbidden identity marker path in %s", body)
			}
		})
	}
}

func assertMarkerAbsentFromResponseAndLogs(t *testing.T, response *httptest.ResponseRecorder, logs, marker string) {
	t.Helper()
	assertMarkerAbsentFromHeadersAndLogs(t, response.Header(), logs, marker)
	assertMarkerAbsent(t, "response body", response.Body.String(), marker)
}

func assertMarkerAbsentFromHeadersAndLogs(t *testing.T, headers http.Header, logs, marker string) {
	t.Helper()
	for name, values := range headers {
		assertMarkerAbsent(t, "response header name", name, marker)
		for _, value := range values {
			assertMarkerAbsent(t, "response header value for "+name, value, marker)
		}
	}
	assertMarkerAbsent(t, "slog output", logs, marker)
}

func assertMarkerAbsent(t *testing.T, surface, text, marker string) {
	t.Helper()
	if strings.Contains(text, marker) {
		t.Fatalf("marker %q leaked in %s: %q", marker, surface, text)
	}
}

func assertBodyMarkerOnlyAtJSONPathPrefix(t *testing.T, response *httptest.ResponseRecorder, marker, allowedPrefix string) {
	t.Helper()
	if bodyMarkerViolatesJSONPathPrefix(response, marker, allowedPrefix) {
		t.Fatalf("marker %q leaked outside %s in response: status=%d headers=%#v body=%q", marker, allowedPrefix, response.Code, response.Header(), response.Body.String())
	}
}

func bodyMarkerViolatesJSONPathPrefix(response *httptest.ResponseRecorder, marker, allowedPrefix string) bool {
	body := response.Body.String()
	if !strings.Contains(body, marker) {
		return false
	}
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
		return true
	}
	var decoded any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		return true
	}
	violates := false
	walkJSONStrings(decoded, "$", func(path, value string) {
		if strings.Contains(value, marker) && !strings.HasPrefix(path, allowedPrefix) {
			violates = true
		}
	})
	return violates
}

func walkJSONStrings(value any, path string, visit func(string, string)) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			walkJSONStrings(child, path+"."+key, visit)
		}
	case []any:
		for index, child := range typed {
			walkJSONStrings(child, fmt.Sprintf("%s[%d]", path, index), visit)
		}
	case string:
		visit(path, typed)
	}
}

func performRequestWithHost(handler http.Handler, method, path, host string, body io.Reader, headers http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://"+host+path, body)
	request.Host = host
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	local := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8090}
	request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, local))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func oversizedHeaderParserResponse(t *testing.T, server *Server, hostMarker, paramsMarker string) string {
	t.Helper()
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
	oversized := strings.Repeat(paramsMarker, (64<<10)/len(paramsMarker)+1)
	_, err = fmt.Fprintf(conn, "GET /healthz HTTP/1.1\r\nHost: %s.localhost\r\nX-Oversized: %s\r\n\r\n", hostMarker, oversized)
	if err != nil {
		t.Fatalf("write request: %v", err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("ReadAll(): %v", err)
	}
	return string(raw)
}

func signedContextForSecurityTest(t *testing.T, secret, keyID, issuer, audience, subject, emailPrefix string) string {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	claims := jwt.MapClaims{
		"sub":   subject,
		"mode":  "chat",
		"email": emailPrefix + "@example.test",
		"iss":   issuer,
		"aud":   audience,
		"jti":   "jwt-id",
		"iat":   now.Unix(),
		"nbf":   now.Unix(),
		"exp":   now.Add(10 * time.Minute).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["typ"] = "JWT"
	token.Header["kid"] = keyID
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("SignedString(): %v", err)
	}
	return signed
}

var _ = identity.PlainHeaderNames
