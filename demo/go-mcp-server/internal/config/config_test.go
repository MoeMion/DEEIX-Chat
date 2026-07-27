package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

const validBearerToken = "0123456789abcdef0123456789abcdef"

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	got, err := Load(envLookup(map[string]string{
		"MCP_DEMO_BEARER_TOKEN": validBearerToken,
	}))
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if got.Addr != "127.0.0.1:8090" {
		t.Fatalf("Addr = %q, want default", got.Addr)
	}
	if got.BearerToken != validBearerToken {
		t.Fatal("BearerToken was not preserved")
	}
	if got.SignedContextHeader != "X-MCP-CLIENT-SIGNED-CONTEXT" {
		t.Fatalf("SignedContextHeader = %q, want default", got.SignedContextHeader)
	}
	if got.ContextJWT != nil {
		t.Fatalf("ContextJWT = %#v, want nil", got.ContextJWT)
	}
}

func TestLoadValidatesBearerToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{name: "missing", env: map[string]string{}, wantErr: true},
		{name: "empty", env: map[string]string{"MCP_DEMO_BEARER_TOKEN": ""}, wantErr: true},
		{name: "31 bytes", env: map[string]string{"MCP_DEMO_BEARER_TOKEN": strings.Repeat("b", 31)}, wantErr: true},
		{name: "32 bytes", env: map[string]string{"MCP_DEMO_BEARER_TOKEN": strings.Repeat("b", 32)}},
		{name: "multibyte counted as bytes", env: map[string]string{"MCP_DEMO_BEARER_TOKEN": strings.Repeat("界", 11)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(envLookup(tt.env))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadValidatesLoopbackAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{name: "ipv4 loopback", addr: "127.0.0.1:8090"},
		{name: "ipv4 loopback range", addr: "127.99.88.77:1"},
		{name: "ipv6 loopback", addr: "[::1]:65535"},
		{name: "localhost", addr: "localhost:8090"},
		{name: "localhost case insensitive", addr: "LoCaLhOsT:8090"},
		{name: "wildcard ipv4", addr: "0.0.0.0:8090", wantErr: true},
		{name: "wildcard ipv6", addr: "[::]:8090", wantErr: true},
		{name: "non loopback ipv4", addr: "192.0.2.1:8090", wantErr: true},
		{name: "non loopback ipv6", addr: "[2001:db8::1]:8090", wantErr: true},
		{name: "dns name", addr: "example.test:8090", wantErr: true},
		{name: "localhost fqdn", addr: "localhost.:8090", wantErr: true},
		{name: "missing port", addr: "127.0.0.1", wantErr: true},
		{name: "zero port", addr: "127.0.0.1:0", wantErr: true},
		{name: "port too large", addr: "127.0.0.1:65536", wantErr: true},
		{name: "nonnumeric port", addr: "127.0.0.1:http", wantErr: true},
		{name: "signed port", addr: "127.0.0.1:+80", wantErr: true},
		{name: "empty host", addr: ":8090", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(envLookup(map[string]string{
				"MCP_DEMO_ADDR":         tt.addr,
				"MCP_DEMO_BEARER_TOKEN": validBearerToken,
			}))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadValidatesContextJWTAsOneConfiguration(t *testing.T) {
	t.Parallel()

	secret := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	complete := map[string]string{
		"MCP_DEMO_BEARER_TOKEN":     validBearerToken,
		"MCP_DEMO_CONTEXT_SECRET":   secret,
		"MCP_DEMO_CONTEXT_ISSUER":   "issuer",
		"MCP_DEMO_CONTEXT_AUDIENCE": "audience",
		"MCP_DEMO_CONTEXT_KEY_ID":   "key-id",
	}

	t.Run("all empty disables verification", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{"MCP_DEMO_BEARER_TOKEN": validBearerToken}
		for _, key := range contextJWTEnvNames {
			env[key] = ""
		}
		got, err := Load(envLookup(env))
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if got.ContextJWT != nil {
			t.Fatalf("ContextJWT = %#v, want nil", got.ContextJWT)
		}
	})

	t.Run("all present enables verification and preserves canonical secret", func(t *testing.T) {
		t.Parallel()
		got, err := Load(envLookup(complete))
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		want := ContextJWT{Secret: secret, Issuer: "issuer", Audience: "audience", KeyID: "key-id"}
		if got.ContextJWT == nil || *got.ContextJWT != want {
			t.Fatalf("ContextJWT = %#v, want %#v", got.ContextJWT, want)
		}
	})

	for _, missing := range contextJWTEnvNames {
		missing := missing
		t.Run("partial missing "+missing, func(t *testing.T) {
			t.Parallel()
			env := cloneEnv(complete)
			delete(env, missing)
			if _, err := Load(envLookup(env)); err == nil {
				t.Fatal("Load() unexpectedly accepted partial context configuration")
			}
		})
	}
}

func TestLoadValidatesCanonicalContextSecret(t *testing.T) {
	t.Parallel()

	canonical := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	tests := []struct {
		name    string
		secret  string
		wantErr bool
	}{
		{name: "canonical 32 bytes", secret: canonical},
		{name: "31 decoded bytes", secret: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 31))), wantErr: true},
		{name: "33 decoded bytes", secret: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 33))), wantErr: true},
		{name: "padded", secret: canonical + "=", wantErr: true},
		{name: "standard alphabet", secret: strings.Repeat("/", 43), wantErr: true},
		{name: "malformed", secret: "not_base64url!", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := completeContextEnv(tt.secret)
			_, err := Load(envLookup(env))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadRejectsIllegalReservedAndCollidingSignedHeaderNames(t *testing.T) {
	t.Parallel()

	reservedNames := []string{
		"Accept", "Accept-Encoding", "Authorization", "Baggage", "Connection",
		"Content-Length", "Content-Type", "Cookie", "Host", "Keep-Alive",
		"Last-Event-ID", "MCP-Protocol-Version", "MCP-Session-Id", "Origin",
		"Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Set-Cookie",
		"TE", "Traceparent", "Tracestate", "Trailer", "Transfer-Encoding", "Upgrade",
		"User-Agent", "X-DEEIX-Context",
	}
	prefixNames := []string{"MCP-Custom", "Proxy-Custom", "Sec-Custom", "X-DEEIX-Custom"}
	plainNames := []string{
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
	cases := []string{"", " X-Custom", "X-Custom ", "X:Custom", strings.Repeat("A", 129)}
	cases = append(cases, reservedNames...)
	cases = append(cases, prefixNames...)
	cases = append(cases, plainNames...)
	for _, name := range cases {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{
				"MCP_DEMO_BEARER_TOKEN":          validBearerToken,
				"MCP_DEMO_SIGNED_CONTEXT_HEADER": alternatingCase(name),
			}
			if _, err := Load(envLookup(env)); err == nil {
				t.Fatalf("Load() unexpectedly accepted signed Header %q", name)
			}
		})
	}
}

func TestLoadAcceptsValidSignedHeaderName(t *testing.T) {
	t.Parallel()

	const name = "X-MCP-CLIENT-SIGNED_~CONTEXT"
	got, err := Load(envLookup(map[string]string{
		"MCP_DEMO_BEARER_TOKEN":          validBearerToken,
		"MCP_DEMO_SIGNED_CONTEXT_HEADER": name,
	}))
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if got.SignedContextHeader != name {
		t.Fatalf("SignedContextHeader = %q, want %q", got.SignedContextHeader, name)
	}
}

func TestLoadErrorsAreLowercaseAndDoNotLeakSecrets(t *testing.T) {
	t.Parallel()

	bearer := "TOP-SECRET-BEARER"
	secret := "TOP-SECRET-CONTEXT"
	_, err := Load(envLookup(map[string]string{
		"MCP_DEMO_BEARER_TOKEN":     bearer,
		"MCP_DEMO_CONTEXT_SECRET":   secret,
		"MCP_DEMO_CONTEXT_ISSUER":   "issuer",
		"MCP_DEMO_CONTEXT_AUDIENCE": "audience",
		"MCP_DEMO_CONTEXT_KEY_ID":   "key",
	}))
	if err == nil {
		t.Fatal("Load() unexpectedly succeeded")
	}
	message := err.Error()
	if message != strings.ToLower(message) {
		t.Fatalf("error = %q, want lowercase", message)
	}
	for _, sensitive := range []string{bearer, secret, "issuer", "audience"} {
		if strings.Contains(message, sensitive) {
			t.Fatalf("error leaked sensitive value %q", sensitive)
		}
	}
}

var contextJWTEnvNames = []string{
	"MCP_DEMO_CONTEXT_SECRET",
	"MCP_DEMO_CONTEXT_ISSUER",
	"MCP_DEMO_CONTEXT_AUDIENCE",
	"MCP_DEMO_CONTEXT_KEY_ID",
}

func envLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func completeContextEnv(secret string) map[string]string {
	return map[string]string{
		"MCP_DEMO_BEARER_TOKEN":     validBearerToken,
		"MCP_DEMO_CONTEXT_SECRET":   secret,
		"MCP_DEMO_CONTEXT_ISSUER":   "issuer",
		"MCP_DEMO_CONTEXT_AUDIENCE": "audience",
		"MCP_DEMO_CONTEXT_KEY_ID":   "key-id",
	}
}

func cloneEnv(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func alternatingCase(value string) string {
	var result strings.Builder
	for index, char := range value {
		if index%2 == 0 {
			result.WriteString(strings.ToLower(string(char)))
		} else {
			result.WriteString(strings.ToUpper(string(char)))
		}
	}
	return result.String()
}
