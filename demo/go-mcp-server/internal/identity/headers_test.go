package identity

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

var expectedPlainHeaderNames = []string{
	"X-MCP-CLIENT-USER-PUBLIC-ID",
	"X-MCP-CLIENT-USER-DISPLAY-NAME",
	"X-MCP-CLIENT-USER-EMAIL",
	"X-MCP-CLIENT-USER-ROLE",
	"X-MCP-CLIENT-CONVERSATION-PUBLIC-ID",
	"X-MCP-CLIENT-ASSISTANT-MESSAGE-PUBLIC-ID",
	"X-MCP-CLIENT-USER-MESSAGE-PUBLIC-ID",
	"X-MCP-CLIENT-REQUEST-ID",
	"X-MCP-CLIENT-RUN-ID",
	"X-MCP-CLIENT-TRACE-ID",
}

func TestHeaderIdentityPlainHeaderNamesAreExactAndDefensive(t *testing.T) {
	t.Parallel()

	first := PlainHeaderNames()
	if !reflect.DeepEqual(first, expectedPlainHeaderNames) {
		t.Fatalf("PlainHeaderNames() = %#v, want %#v", first, expectedPlainHeaderNames)
	}
	first[0] = "mutated"
	second := PlainHeaderNames()
	if !reflect.DeepEqual(second, expectedPlainHeaderNames) {
		t.Fatalf("PlainHeaderNames() shared mutable storage: %#v", second)
	}
}

func TestParseHeadersMapsExactAllowlistCaseInsensitively(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"x-mcp-client-user-public-id":              {"user_pub"},
		"X-MCP-CLIENT-USER-DISPLAY-NAME":           {"Dee Ix"},
		"x-McP-cLiEnT-uSeR-eMaIl":                  {"user@example.test"},
		"X-MCP-CLIENT-USER-ROLE":                   {"admin"},
		"X-MCP-CLIENT-CONVERSATION-PUBLIC-ID":      {"conv_pub"},
		"X-MCP-CLIENT-ASSISTANT-MESSAGE-PUBLIC-ID": {"assistant_pub"},
		"X-MCP-CLIENT-USER-MESSAGE-PUBLIC-ID":      {"message_pub"},
		"X-MCP-CLIENT-REQUEST-ID":                  {"req_123"},
		"X-MCP-CLIENT-RUN-ID":                      {"run_123"},
		"X-MCP-CLIENT-TRACE-ID":                    {"trace_123"},
	}
	want := HeaderIdentity{
		UserPublicID:             "user_pub",
		UserDisplayName:          "Dee Ix",
		UserEmail:                "user@example.test",
		UserRole:                 "admin",
		ConversationPublicID:     "conv_pub",
		AssistantMessagePublicID: "assistant_pub",
		UserMessagePublicID:      "message_pub",
		RequestID:                "req_123",
		RunID:                    "run_123",
		TraceID:                  "trace_123",
	}
	got, err := ParseHeaders(headers)
	if err != nil {
		t.Fatalf("ParseHeaders(): %v", err)
	}
	if got != want {
		t.Fatalf("ParseHeaders() = %#v, want %#v", got, want)
	}
}

func TestParseHeadersAllowsMissingValuesAndPreservesBytes(t *testing.T) {
	t.Parallel()

	value := "  Cafe\u0301  "
	got, err := ParseHeaders(http.Header{
		"X-MCP-CLIENT-USER-DISPLAY-NAME": {value},
	})
	if err != nil {
		t.Fatalf("ParseHeaders(): %v", err)
	}
	if got.UserDisplayName != value {
		t.Fatalf("UserDisplayName = %q, want byte-preserved %q", got.UserDisplayName, value)
	}
	want := HeaderIdentity{UserDisplayName: value}
	if got != want {
		t.Fatalf("ParseHeaders() populated missing fields: %#v", got)
	}
}

func TestParseHeadersRejectsDuplicateAllowlistedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers http.Header
	}{
		{
			name: "two values on one key",
			headers: http.Header{
				"X-MCP-CLIENT-USER-EMAIL": {"same@example.test", "same@example.test"},
			},
		},
		{
			name: "same value on case variant keys",
			headers: http.Header{
				"X-MCP-CLIENT-USER-EMAIL": {"same@example.test"},
				"x-mcp-client-user-email": {"same@example.test"},
			},
		},
		{
			name: "different values on case variant keys",
			headers: http.Header{
				"X-MCP-CLIENT-RUN-ID": {"run-a"},
				"x-mcp-client-run-id": {"run-b"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseHeaders(tt.headers); err == nil {
				t.Fatal("ParseHeaders() unexpectedly accepted duplicate values")
			}
		})
	}
}

func TestParseHeadersEnforcesEveryUTF8ByteBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		limit  int
		read   func(HeaderIdentity) string
	}{
		{name: "user public id", header: expectedPlainHeaderNames[0], limit: 128, read: func(v HeaderIdentity) string { return v.UserPublicID }},
		{name: "user display name", header: expectedPlainHeaderNames[1], limit: 256, read: func(v HeaderIdentity) string { return v.UserDisplayName }},
		{name: "user email", header: expectedPlainHeaderNames[2], limit: 320, read: func(v HeaderIdentity) string { return v.UserEmail }},
		{name: "user role", header: expectedPlainHeaderNames[3], limit: 64, read: func(v HeaderIdentity) string { return v.UserRole }},
		{name: "conversation public id", header: expectedPlainHeaderNames[4], limit: 128, read: func(v HeaderIdentity) string { return v.ConversationPublicID }},
		{name: "assistant message public id", header: expectedPlainHeaderNames[5], limit: 128, read: func(v HeaderIdentity) string { return v.AssistantMessagePublicID }},
		{name: "user message public id", header: expectedPlainHeaderNames[6], limit: 128, read: func(v HeaderIdentity) string { return v.UserMessagePublicID }},
		{name: "request id", header: expectedPlainHeaderNames[7], limit: 128, read: func(v HeaderIdentity) string { return v.RequestID }},
		{name: "run id", header: expectedPlainHeaderNames[8], limit: 64, read: func(v HeaderIdentity) string { return v.RunID }},
		{name: "trace id", header: expectedPlainHeaderNames[9], limit: 64, read: func(v HeaderIdentity) string { return v.TraceID }},
	}
	for _, tt := range tests {
		t.Run(tt.name+" accepts exact boundary", func(t *testing.T) {
			t.Parallel()
			value := strings.Repeat("a", tt.limit)
			got, err := ParseHeaders(http.Header{tt.header: {value}})
			if err != nil {
				t.Fatalf("ParseHeaders(): %v", err)
			}
			if tt.read(got) != value {
				t.Fatalf("parsed value length = %d, want %d", len(tt.read(got)), tt.limit)
			}
		})
		t.Run(tt.name+" rejects one byte over", func(t *testing.T) {
			t.Parallel()
			value := strings.Repeat("a", tt.limit+1)
			if _, err := ParseHeaders(http.Header{tt.header: {value}}); err == nil {
				t.Fatal("ParseHeaders() unexpectedly accepted oversized value")
			}
		})
	}

	t.Run("multibyte values are measured in bytes", func(t *testing.T) {
		t.Parallel()
		accepted := strings.Repeat("界", 85) + "a"
		if len(accepted) != 256 {
			t.Fatalf("test value has %d bytes, want 256", len(accepted))
		}
		if _, err := ParseHeaders(http.Header{expectedPlainHeaderNames[1]: {accepted}}); err != nil {
			t.Fatalf("ParseHeaders() rejected exact multibyte boundary: %v", err)
		}
		rejected := accepted + "b"
		if _, err := ParseHeaders(http.Header{expectedPlainHeaderNames[1]: {rejected}}); err == nil {
			t.Fatal("ParseHeaders() accepted multibyte value over byte boundary")
		}
	})
}

func TestParseHeadersRejectsInvalidUTF8AndUnicodeControlsWithoutReflection(t *testing.T) {
	t.Parallel()

	secret := "do-not-reflect-this-value"
	tests := []struct {
		name  string
		value string
	}{
		{name: "invalid utf8", value: string([]byte{0xff, 0xfe}) + secret},
		{name: "nul", value: secret + "\x00"},
		{name: "delete", value: secret + "\x7f"},
		{name: "unicode next line control", value: secret + "\u0085"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseHeaders(http.Header{expectedPlainHeaderNames[2]: {tt.value}})
			if err == nil {
				t.Fatal("ParseHeaders() unexpectedly accepted invalid value")
			}
			if err.Error() != strings.ToLower(err.Error()) {
				t.Fatalf("error = %q, want lowercase", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error reflected rejected value: %v", err)
			}
		})
	}
}

func TestParseHeadersIgnoresUnknownClientHeadersWithoutReflection(t *testing.T) {
	t.Parallel()

	secret := "unknown-header-secret"
	headers := http.Header{
		"X-MCP-CLIENT-UNKNOWN": {secret, secret + "\x00", strings.Repeat(secret, 100)},
		"X-OTHER":              {secret},
	}
	got, err := ParseHeaders(headers)
	if err != nil {
		t.Fatalf("ParseHeaders(): %v", err)
	}
	if got != (HeaderIdentity{}) {
		t.Fatalf("ParseHeaders() returned unknown data: %#v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("unknown Header reflected in identity JSON: %s", encoded)
	}
}

func TestHeaderIdentityJSONContract(t *testing.T) {
	t.Parallel()

	identity := HeaderIdentity{
		UserPublicID:             "1",
		UserDisplayName:          "2",
		UserEmail:                "3",
		UserRole:                 "4",
		ConversationPublicID:     "5",
		AssistantMessagePublicID: "6",
		UserMessagePublicID:      "7",
		RequestID:                "8",
		RunID:                    "9",
		TraceID:                  "10",
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	want := `{"userPublicID":"1","userDisplayName":"2","userEmail":"3","userRole":"4","conversationPublicID":"5","assistantMessagePublicID":"6","userMessagePublicID":"7","requestID":"8","runID":"9","traceID":"10"}`
	if string(encoded) != want {
		t.Fatalf("JSON = %s, want %s", encoded, want)
	}
}

func TestHeaderIdentityCanonicalDigestGolden(t *testing.T) {
	t.Parallel()

	identity := HeaderIdentity{
		UserPublicID:             "user_pub",
		UserDisplayName:          "Dee Ix",
		UserEmail:                "user@example.test",
		UserRole:                 "admin",
		ConversationPublicID:     "conv_pub",
		AssistantMessagePublicID: "assistant_pub",
		UserMessagePublicID:      "message_pub",
		RequestID:                "req_123",
		RunID:                    "run_123",
		TraceID:                  "trace_123",
	}
	digest := identity.CanonicalDigest()
	got := hex.EncodeToString(digest[:])
	const want = "e0fe9a3264e03c84d1ce89c02df1b3cad4738ebe98635754f3db1e63433ac863"
	if got != want {
		t.Fatalf("CanonicalDigest() = %s, want %s", got, want)
	}
}

func TestHeaderIdentityCanonicalDigestResistsConcatenationAmbiguity(t *testing.T) {
	t.Parallel()

	left := HeaderIdentity{UserPublicID: "ab", UserDisplayName: "c"}
	right := HeaderIdentity{UserPublicID: "a", UserDisplayName: "bc"}
	if left.CanonicalDigest() == right.CanonicalDigest() {
		t.Fatal("CanonicalDigest() did not frame field boundaries")
	}
	shifted := HeaderIdentity{UserDisplayName: "ab", UserEmail: "c"}
	if left.CanonicalDigest() == shifted.CanonicalDigest() {
		t.Fatal("CanonicalDigest() did not preserve field positions")
	}
}
