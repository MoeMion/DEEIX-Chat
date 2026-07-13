package identity

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testSignedHeader = "X-MCP-CLIENT-SIGNED-CONTEXT"
	testIssuer       = "https://deeix.example.test"
	testAudience     = "mcp-demo"
	testKeyID        = "key-1"
)

var (
	testNow    = time.Date(2026, time.July, 13, 8, 0, 0, 0, time.UTC)
	testSecret = base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
)

type testContextClaims struct {
	Mode                     string `json:"mode"`
	Name                     string `json:"name,omitempty"`
	Email                    string `json:"email,omitempty"`
	Role                     string `json:"role,omitempty"`
	ConversationPublicID     string `json:"conversation_id,omitempty"`
	AssistantMessagePublicID string `json:"assistant_message_id,omitempty"`
	UserMessagePublicID      string `json:"user_message_id,omitempty"`
	RequestID                string `json:"request_id,omitempty"`
	RunID                    string `json:"run_id,omitempty"`
	TraceID                  string `json:"trace_id,omitempty"`
	jwt.RegisteredClaims
}

func TestNewResolverAllowsConfiguredAndUnconfigured(t *testing.T) {
	t.Parallel()

	configured, err := NewResolver(validSignedConfig(), fixedResolverNow)
	if err != nil {
		t.Fatalf("NewResolver(configured): %v", err)
	}
	if configured == nil {
		t.Fatal("NewResolver(configured) returned nil")
	}

	unconfigured, err := NewResolver(&SignedConfig{Header: testSignedHeader}, fixedResolverNow)
	if err != nil {
		t.Fatalf("NewResolver(unconfigured): %v", err)
	}
	snapshot, err := unconfigured.Resolve(http.Header{
		testSignedHeader: {"untrusted-token"},
	})
	if err != nil {
		t.Fatalf("Resolve(unconfigured): %v", err)
	}
	want := Verification{
		Present:    true,
		Configured: false,
		Valid:      false,
		Verified:   false,
		Reason:     "signed_context_unconfigured",
	}
	if snapshot.Verification != want {
		t.Fatalf("Verification = %#v, want %#v", snapshot.Verification, want)
	}

	withoutConfig, err := NewResolver(nil, fixedResolverNow)
	if err != nil {
		t.Fatalf("NewResolver(nil): %v", err)
	}
	snapshot, err = withoutConfig.Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve(nil config): %v", err)
	}
	if snapshot.Verification.Reason != "signed_context_unconfigured" {
		t.Fatalf("Reason = %q, want signed_context_unconfigured", snapshot.Verification.Reason)
	}
}

func TestNewResolverRejectsNilClockAndPartialConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := NewResolver(nil, nil); err == nil {
		t.Fatal("NewResolver() accepted a nil clock")
	}
	partial := &SignedConfig{Header: testSignedHeader, Secret: "do-not-reflect-this-secret"}
	_, err := NewResolver(partial, fixedResolverNow)
	if err == nil {
		t.Fatal("NewResolver() accepted partial signed configuration")
	}
	if strings.Contains(err.Error(), partial.Secret) {
		t.Fatalf("NewResolver() error leaked secret: %v", err)
	}
}

func TestResolverVerifiesValidSignedContext(t *testing.T) {
	t.Parallel()

	resolver := mustTestResolver(t, validSignedConfig())
	claims := validTestContextClaims("chat")
	raw := signTestContext(t, jwt.SigningMethodHS256, claims, "JWT", testKeyID, []byte(testSecret))
	headers := matchingPlainHeaders()
	headers.Set(testSignedHeader, raw)

	snapshot, err := resolver.Resolve(headers)
	if err != nil {
		t.Fatalf("Resolve(): %v", err)
	}
	wantVerification := Verification{
		Present:    true,
		Configured: true,
		Valid:      true,
		Verified:   true,
		Reason:     "signed_context_verified",
	}
	if snapshot.Verification != wantVerification {
		t.Fatalf("Verification = %#v, want %#v", snapshot.Verification, wantVerification)
	}
	wantSigned := SignedIdentity{
		Subject:                  "user_pub",
		Mode:                     "chat",
		Name:                     "Dee Ix",
		Email:                    "user@example.test",
		Role:                     "admin",
		ConversationPublicID:     "conv_pub",
		AssistantMessagePublicID: "assistant_pub",
		UserMessagePublicID:      "message_pub",
		RequestID:                "req_123",
		RunID:                    "run_123",
		TraceID:                  "trace_123",
	}
	if snapshot.SignedIdentity == nil || *snapshot.SignedIdentity != wantSigned {
		t.Fatalf("SignedIdentity = %#v, want %#v", snapshot.SignedIdentity, wantSigned)
	}
	if len(snapshot.Mismatches) != 0 {
		t.Fatalf("Mismatches = %#v, want empty", snapshot.Mismatches)
	}
	if snapshot.Mismatches == nil {
		t.Fatal("Mismatches = nil, want a stable empty slice")
	}
}

func TestResolverReportsStableNonVerifiedReasons(t *testing.T) {
	t.Parallel()

	resolver := mustTestResolver(t, validSignedConfig())
	valid := signTestContext(t, jwt.SigningMethodHS256, validTestContextClaims("chat"), "JWT", testKeyID, []byte(testSecret))
	decodedSecret, err := base64.RawURLEncoding.DecodeString(testSecret)
	if err != nil {
		t.Fatalf("DecodeString(testSecret): %v", err)
	}
	tests := []struct {
		name       string
		headers    http.Header
		wantReason string
		present    bool
	}{
		{name: "missing", headers: http.Header{}, wantReason: "signed_context_missing"},
		{
			name:       "duplicate on one key",
			headers:    http.Header{testSignedHeader: {valid, valid}},
			wantReason: "signed_context_duplicate",
			present:    true,
		},
		{
			name:       "duplicate across case variants",
			headers:    http.Header{testSignedHeader: {valid}, strings.ToLower(testSignedHeader): {valid}},
			wantReason: "signed_context_duplicate",
			present:    true,
		},
		{
			name:       "exact compact boundary remains an ordinary invalid token",
			headers:    http.Header{testSignedHeader: {strings.Repeat("x", 8192)}},
			wantReason: "signed_context_invalid",
			present:    true,
		},
		{
			name:       "one byte over compact boundary",
			headers:    http.Header{testSignedHeader: {strings.Repeat("x", 8193)}},
			wantReason: "signed_context_too_large",
			present:    true,
		},
		{
			name:       "malformed",
			headers:    http.Header{testSignedHeader: {"not-a-jwt"}},
			wantReason: "signed_context_invalid",
			present:    true,
		},
		{
			name:       "wrong signing key",
			headers:    http.Header{testSignedHeader: {signTestContext(t, jwt.SigningMethodHS256, validTestContextClaims("chat"), "JWT", testKeyID, []byte("wrong-key"))}},
			wantReason: "signed_context_invalid",
			present:    true,
		},
		{
			name:       "decoded secret bytes are not the hmac key",
			headers:    http.Header{testSignedHeader: {signTestContext(t, jwt.SigningMethodHS256, validTestContextClaims("chat"), "JWT", testKeyID, decodedSecret)}},
			wantReason: "signed_context_invalid",
			present:    true,
		},
		{
			name:       "non canonical base64url trailing bits",
			headers:    http.Header{testSignedHeader: {makeNonCanonicalCompactJWT(t, valid, []byte(testSecret))}},
			wantReason: "signed_context_invalid",
			present:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			snapshot, resolveErr := resolver.Resolve(tt.headers)
			if resolveErr != nil {
				t.Fatalf("Resolve(): %v", resolveErr)
			}
			want := Verification{
				Present:    tt.present,
				Configured: true,
				Reason:     tt.wantReason,
			}
			if snapshot.Verification != want {
				t.Fatalf("Verification = %#v, want %#v", snapshot.Verification, want)
			}
			if snapshot.SignedIdentity != nil {
				t.Fatalf("SignedIdentity = %#v, want nil", snapshot.SignedIdentity)
			}
		})
	}
}

func TestResolverAcceptsModesAndTTLBoundaries(t *testing.T) {
	t.Parallel()

	resolver := mustTestResolver(t, validSignedConfig())
	for _, mode := range []string{"chat", "probe", "sync"} {
		mode := mode
		t.Run("mode "+mode, func(t *testing.T) {
			t.Parallel()
			claims := validTestContextClaims(mode)
			snapshot := resolveTestClaims(t, resolver, claims, jwt.SigningMethodHS256, "JWT", testKeyID, []byte(testSecret))
			if !snapshot.Verification.Verified || snapshot.SignedIdentity == nil || snapshot.SignedIdentity.Mode != mode {
				t.Fatalf("snapshot = %#v, want verified %s identity", snapshot, mode)
			}
		})
	}

	ttls := []struct {
		seconds int
		valid   bool
	}{
		{seconds: 59},
		{seconds: 60, valid: true},
		{seconds: 900, valid: true},
		{seconds: 901},
	}
	for _, tt := range ttls {
		t.Run("ttl "+(time.Duration(tt.seconds)*time.Second).String(), func(t *testing.T) {
			t.Parallel()
			claims := validTestContextClaims("chat")
			issuedAt := testNow.Add(-time.Second)
			claims.IssuedAt = jwt.NewNumericDate(issuedAt)
			claims.NotBefore = jwt.NewNumericDate(issuedAt)
			claims.ExpiresAt = jwt.NewNumericDate(issuedAt.Add(time.Duration(tt.seconds) * time.Second))
			snapshot := resolveTestClaims(t, resolver, claims, jwt.SigningMethodHS256, "JWT", testKeyID, []byte(testSecret))
			if snapshot.Verification.Valid != tt.valid {
				t.Fatalf("Valid = %v, want %v (%#v)", snapshot.Verification.Valid, tt.valid, snapshot.Verification)
			}
		})
	}
}

func TestResolverRejectsJOSEAndRegisteredClaimViolations(t *testing.T) {
	t.Parallel()

	type tokenSetup struct {
		method jwt.SigningMethod
		typ    string
		keyID  string
		key    any
	}
	tests := []struct {
		name   string
		mutate func(*testContextClaims, *tokenSetup)
	}{
		{name: "hs384", mutate: func(_ *testContextClaims, setup *tokenSetup) { setup.method = jwt.SigningMethodHS384 }},
		{name: "none algorithm", mutate: func(_ *testContextClaims, setup *tokenSetup) {
			setup.method = jwt.SigningMethodNone
			setup.key = jwt.UnsafeAllowNoneSignatureType
		}},
		{name: "missing typ", mutate: func(_ *testContextClaims, setup *tokenSetup) { setup.typ = "" }},
		{name: "wrong typ", mutate: func(_ *testContextClaims, setup *tokenSetup) { setup.typ = "jwt" }},
		{name: "missing kid", mutate: func(_ *testContextClaims, setup *tokenSetup) { setup.keyID = "" }},
		{name: "wrong kid", mutate: func(_ *testContextClaims, setup *tokenSetup) { setup.keyID = "other-key" }},
		{name: "missing issuer", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.Issuer = "" }},
		{name: "wrong issuer", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.Issuer = "https://other.example.test" }},
		{name: "missing audience", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.Audience = nil }},
		{name: "multiple audiences", mutate: func(claims *testContextClaims, _ *tokenSetup) {
			claims.Audience = jwt.ClaimStrings{testAudience, "other"}
		}},
		{name: "wrong audience", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.Audience = jwt.ClaimStrings{"other"} }},
		{name: "missing exp", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.ExpiresAt = nil }},
		{name: "missing nbf", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.NotBefore = nil }},
		{name: "missing iat", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.IssuedAt = nil }},
		{name: "iat differs from nbf", mutate: func(claims *testContextClaims, _ *tokenSetup) {
			claims.NotBefore = jwt.NewNumericDate(claims.IssuedAt.Add(time.Second))
		}},
		{name: "iat in future", mutate: func(claims *testContextClaims, _ *tokenSetup) {
			future := testNow.Add(time.Second)
			claims.IssuedAt = jwt.NewNumericDate(future)
			claims.NotBefore = jwt.NewNumericDate(future)
			claims.ExpiresAt = jwt.NewNumericDate(future.Add(time.Minute))
		}},
		{name: "now equals exp", mutate: func(claims *testContextClaims, _ *tokenSetup) {
			issuedAt := testNow.Add(-time.Minute)
			claims.IssuedAt = jwt.NewNumericDate(issuedAt)
			claims.NotBefore = jwt.NewNumericDate(issuedAt)
			claims.ExpiresAt = jwt.NewNumericDate(testNow)
		}},
		{name: "missing jti", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.ID = "" }},
		{name: "missing sub", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.Subject = "" }},
		{name: "blank sub", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.Subject = " \t" }},
		{name: "non canonical sub whitespace", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.Subject = " user_pub " }},
		{name: "unsupported mode", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.Mode = "admin" }},
		{name: "sync requires system subject", mutate: func(claims *testContextClaims, _ *tokenSetup) { claims.Mode = "sync"; claims.Subject = "user_pub" }},
	}
	resolver := mustTestResolver(t, validSignedConfig())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := validTestContextClaims("chat")
			setup := tokenSetup{method: jwt.SigningMethodHS256, typ: "JWT", keyID: testKeyID, key: []byte(testSecret)}
			tt.mutate(&claims, &setup)
			snapshot := resolveTestClaims(t, resolver, claims, setup.method, setup.typ, setup.keyID, setup.key)
			if snapshot.Verification.Reason != "signed_context_invalid" || snapshot.Verification.Valid {
				t.Fatalf("Verification = %#v, want invalid", snapshot.Verification)
			}
		})
	}
}

func TestResolverEnforcesEveryClaimByteBoundary(t *testing.T) {
	t.Parallel()

	type boundaryCase struct {
		name  string
		limit int
		set   func(*testContextClaims, *SignedConfig, *string, string)
	}
	tests := []boundaryCase{
		{name: "subject", limit: 128, set: func(c *testContextClaims, _ *SignedConfig, _ *string, v string) { c.Subject = v }},
		{name: "name", limit: 256, set: func(c *testContextClaims, _ *SignedConfig, _ *string, v string) { c.Name = v }},
		{name: "email", limit: 320, set: func(c *testContextClaims, _ *SignedConfig, _ *string, v string) { c.Email = v }},
		{name: "role", limit: 64, set: func(c *testContextClaims, _ *SignedConfig, _ *string, v string) { c.Role = v }},
		{name: "conversation id", limit: 128, set: func(c *testContextClaims, _ *SignedConfig, _ *string, v string) { c.ConversationPublicID = v }},
		{name: "assistant message id", limit: 128, set: func(c *testContextClaims, _ *SignedConfig, _ *string, v string) { c.AssistantMessagePublicID = v }},
		{name: "user message id", limit: 128, set: func(c *testContextClaims, _ *SignedConfig, _ *string, v string) { c.UserMessagePublicID = v }},
		{name: "request id", limit: 128, set: func(c *testContextClaims, _ *SignedConfig, _ *string, v string) { c.RequestID = v }},
		{name: "run id", limit: 64, set: func(c *testContextClaims, _ *SignedConfig, _ *string, v string) { c.RunID = v }},
		{name: "trace id", limit: 64, set: func(c *testContextClaims, _ *SignedConfig, _ *string, v string) { c.TraceID = v }},
		{name: "issuer", limit: 512, set: func(c *testContextClaims, cfg *SignedConfig, _ *string, v string) { c.Issuer = v; cfg.Issuer = v }},
		{name: "audience", limit: 128, set: func(c *testContextClaims, cfg *SignedConfig, _ *string, v string) {
			c.Audience = jwt.ClaimStrings{v}
			cfg.Audience = v
		}},
		{name: "key id", limit: 64, set: func(_ *testContextClaims, cfg *SignedConfig, kid *string, v string) { cfg.KeyID = v; *kid = v }},
		{name: "jti", limit: 64, set: func(c *testContextClaims, _ *SignedConfig, _ *string, v string) { c.ID = v }},
	}
	for _, tt := range tests {
		t.Run(tt.name+" exact", func(t *testing.T) {
			t.Parallel()
			assertClaimBoundary(t, tt.limit, true, tt.set)
		})
		t.Run(tt.name+" over", func(t *testing.T) {
			t.Parallel()
			assertClaimBoundary(t, tt.limit+1, false, tt.set)
		})
	}

	t.Run("multibyte name measured in bytes", func(t *testing.T) {
		t.Parallel()
		accepted := strings.Repeat("界", 85) + "a"
		if len(accepted) != 256 {
			t.Fatalf("accepted test value = %d bytes", len(accepted))
		}
		claims := validTestContextClaims("chat")
		claims.Name = accepted
		resolver := mustTestResolver(t, validSignedConfig())
		if snapshot := resolveTestClaims(t, resolver, claims, jwt.SigningMethodHS256, "JWT", testKeyID, []byte(testSecret)); !snapshot.Verification.Valid {
			t.Fatalf("exact multibyte boundary rejected: %#v", snapshot.Verification)
		}
		claims.Name += "b"
		if snapshot := resolveTestClaims(t, resolver, claims, jwt.SigningMethodHS256, "JWT", testKeyID, []byte(testSecret)); snapshot.Verification.Valid {
			t.Fatal("multibyte value over boundary accepted")
		}
	})
}

func TestResolverAcceptsSignerCompatibleControlsAndRejectsRawInvalidUTF8(t *testing.T) {
	t.Parallel()

	controlFields := []struct {
		name   string
		mutate func(*testContextClaims, *SignedConfig, *string)
	}{
		{name: "subject", mutate: func(c *testContextClaims, _ *SignedConfig, _ *string) { c.Subject += "\x00" }},
		{name: "name", mutate: func(c *testContextClaims, _ *SignedConfig, _ *string) { c.Name += "\u0085" }},
		{name: "email", mutate: func(c *testContextClaims, _ *SignedConfig, _ *string) { c.Email += "\x7f" }},
		{name: "role", mutate: func(c *testContextClaims, _ *SignedConfig, _ *string) { c.Role += "\n" }},
		{name: "conversation", mutate: func(c *testContextClaims, _ *SignedConfig, _ *string) { c.ConversationPublicID += "\x00" }},
		{name: "assistant message", mutate: func(c *testContextClaims, _ *SignedConfig, _ *string) { c.AssistantMessagePublicID += "\x00" }},
		{name: "user message", mutate: func(c *testContextClaims, _ *SignedConfig, _ *string) { c.UserMessagePublicID += "\x00" }},
		{name: "request", mutate: func(c *testContextClaims, _ *SignedConfig, _ *string) { c.RequestID += "\x00" }},
		{name: "run", mutate: func(c *testContextClaims, _ *SignedConfig, _ *string) { c.RunID += "\x00" }},
		{name: "trace", mutate: func(c *testContextClaims, _ *SignedConfig, _ *string) { c.TraceID += "\x00" }},
		{name: "issuer", mutate: func(c *testContextClaims, cfg *SignedConfig, _ *string) { c.Issuer += "\x00"; cfg.Issuer = c.Issuer }},
		{name: "audience", mutate: func(c *testContextClaims, cfg *SignedConfig, _ *string) {
			value := testAudience + "\x00"
			c.Audience = jwt.ClaimStrings{value}
			cfg.Audience = value
		}},
		{name: "key id", mutate: func(_ *testContextClaims, cfg *SignedConfig, kid *string) { *kid += "\x00"; cfg.KeyID = *kid }},
		{name: "jti", mutate: func(c *testContextClaims, _ *SignedConfig, _ *string) { c.ID += "\x00" }},
	}
	for _, tt := range controlFields {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := *validSignedConfig()
			claims := validTestContextClaims("chat")
			kid := testKeyID
			tt.mutate(&claims, &cfg, &kid)
			resolver := mustTestResolver(t, &cfg)
			snapshot := resolveTestClaims(t, resolver, claims, jwt.SigningMethodHS256, "JWT", kid, []byte(testSecret))
			if !snapshot.Verification.Valid {
				t.Fatalf("signer-compatible control rejected: %#v", snapshot.Verification)
			}
		})
	}

	claims := validTestContextClaims("chat")
	raw := signTestContext(t, jwt.SigningMethodHS256, claims, "JWT", testKeyID, []byte(testSecret))
	raw = replaceSignedPayloadValueWithInvalidUTF8(t, raw, "Dee Ix", []byte(testSecret))
	resolver := mustTestResolver(t, validSignedConfig())
	snapshot := resolveRawTestToken(t, resolver, raw, nil)
	if snapshot.Verification.Valid || snapshot.Verification.Reason != "signed_context_invalid" {
		t.Fatalf("raw invalid UTF-8 accepted: %#v", snapshot.Verification)
	}
}

func TestResolverReportsOrderedMismatchesWithoutValues(t *testing.T) {
	t.Parallel()

	headers := matchingPlainHeaders()
	for name := range headers {
		headers[name] = []string{"plain-secret-marker"}
	}
	resolver := mustTestResolver(t, validSignedConfig())
	raw := signTestContext(t, jwt.SigningMethodHS256, validTestContextClaims("chat"), "JWT", testKeyID, []byte(testSecret))
	snapshot := resolveRawTestToken(t, resolver, raw, headers)
	want := []string{
		"userPublicID",
		"userDisplayName",
		"userEmail",
		"userRole",
		"conversationPublicID",
		"assistantMessagePublicID",
		"userMessagePublicID",
		"requestID",
		"runID",
		"traceID",
	}
	if !reflect.DeepEqual(snapshot.Mismatches, want) {
		t.Fatalf("Mismatches = %#v, want %#v", snapshot.Mismatches, want)
	}
	if !snapshot.Verification.Valid || snapshot.Verification.Verified || snapshot.Verification.Reason != "signed_context_mismatch" {
		t.Fatalf("Verification = %#v, want valid mismatch", snapshot.Verification)
	}
	for _, mismatch := range snapshot.Mismatches {
		if strings.Contains(mismatch, "plain-secret-marker") {
			t.Fatalf("mismatch reflected identity value: %q", mismatch)
		}
	}
}

func TestResolverComparesOnlyOverlappingFieldsAndSkipsSyncSubject(t *testing.T) {
	t.Parallel()

	resolver := mustTestResolver(t, validSignedConfig())
	claims := validTestContextClaims("chat")
	claims.Name = ""
	claims.Email = ""
	headers := http.Header{
		"X-MCP-CLIENT-USER-DISPLAY-NAME": {"different"},
		"X-MCP-CLIENT-USER-EMAIL":        {"different"},
	}
	snapshot := resolveTestClaimsWithHeaders(t, resolver, claims, jwt.SigningMethodHS256, "JWT", testKeyID, []byte(testSecret), headers)
	if !snapshot.Verification.Verified || len(snapshot.Mismatches) != 0 {
		t.Fatalf("non-overlapping values mismatched: %#v", snapshot)
	}

	syncClaims := validTestContextClaims("sync")
	syncHeaders := http.Header{"X-MCP-CLIENT-USER-PUBLIC-ID": {"different-user"}}
	snapshot = resolveTestClaimsWithHeaders(t, resolver, syncClaims, jwt.SigningMethodHS256, "JWT", testKeyID, []byte(testSecret), syncHeaders)
	if !snapshot.Verification.Verified || len(snapshot.Mismatches) != 0 {
		t.Fatalf("sync subject was compared to end-user Header: %#v", snapshot)
	}
}

func TestResolverReturnsPlainHeaderErrorsWithoutReflection(t *testing.T) {
	t.Parallel()

	secret := "plain-header-secret"
	resolver := mustTestResolver(t, validSignedConfig())
	_, err := resolver.Resolve(http.Header{
		"X-MCP-CLIENT-USER-EMAIL": {secret + "\x00"},
	})
	if err == nil {
		t.Fatal("Resolve() accepted malformed plain identity")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Resolve() error reflected plain identity: %v", err)
	}
}

func TestResolverDefensivelyCopiesConfiguration(t *testing.T) {
	t.Parallel()

	cfg := validSignedConfig()
	resolver := mustTestResolver(t, cfg)
	cfg.Secret = "mutated"
	cfg.Issuer = "mutated"
	raw := signTestContext(t, jwt.SigningMethodHS256, validTestContextClaims("chat"), "JWT", testKeyID, []byte(testSecret))
	snapshot := resolveRawTestToken(t, resolver, raw, nil)
	if !snapshot.Verification.Valid {
		t.Fatalf("resolver retained caller-owned configuration: %#v", snapshot.Verification)
	}
}

func validSignedConfig() *SignedConfig {
	return &SignedConfig{
		Header:   testSignedHeader,
		Secret:   testSecret,
		Issuer:   testIssuer,
		Audience: testAudience,
		KeyID:    testKeyID,
	}
}

func fixedResolverNow() time.Time {
	return testNow
}

func mustTestResolver(t *testing.T, cfg *SignedConfig) *Resolver {
	t.Helper()
	resolver, err := NewResolver(cfg, fixedResolverNow)
	if err != nil {
		t.Fatalf("NewResolver(): %v", err)
	}
	return resolver
}

func validTestContextClaims(mode string) testContextClaims {
	subject := "user_pub"
	if mode == "sync" {
		subject = "system:mcp-sync"
	}
	issuedAt := testNow.Add(-time.Minute)
	return testContextClaims{
		Mode:                     mode,
		Name:                     "Dee Ix",
		Email:                    "user@example.test",
		Role:                     "admin",
		ConversationPublicID:     "conv_pub",
		AssistantMessagePublicID: "assistant_pub",
		UserMessagePublicID:      "message_pub",
		RequestID:                "req_123",
		RunID:                    "run_123",
		TraceID:                  "trace_123",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    testIssuer,
			Subject:   subject,
			Audience:  jwt.ClaimStrings{testAudience},
			ExpiresAt: jwt.NewNumericDate(issuedAt.Add(5 * time.Minute)),
			NotBefore: jwt.NewNumericDate(issuedAt),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ID:        "jti_123",
		},
	}
}

func signTestContext(
	t *testing.T,
	method jwt.SigningMethod,
	claims testContextClaims,
	typ string,
	keyID string,
	key any,
) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	if typ == "" {
		delete(token.Header, "typ")
	} else {
		token.Header["typ"] = typ
	}
	if keyID == "" {
		delete(token.Header, "kid")
	} else {
		token.Header["kid"] = keyID
	}
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("SignedString(): %v", err)
	}
	return raw
}

func matchingPlainHeaders() http.Header {
	return http.Header{
		"X-MCP-CLIENT-USER-PUBLIC-ID":              {"user_pub"},
		"X-MCP-CLIENT-USER-DISPLAY-NAME":           {"Dee Ix"},
		"X-MCP-CLIENT-USER-EMAIL":                  {"user@example.test"},
		"X-MCP-CLIENT-USER-ROLE":                   {"admin"},
		"X-MCP-CLIENT-CONVERSATION-PUBLIC-ID":      {"conv_pub"},
		"X-MCP-CLIENT-ASSISTANT-MESSAGE-PUBLIC-ID": {"assistant_pub"},
		"X-MCP-CLIENT-USER-MESSAGE-PUBLIC-ID":      {"message_pub"},
		"X-MCP-CLIENT-REQUEST-ID":                  {"req_123"},
		"X-MCP-CLIENT-RUN-ID":                      {"run_123"},
		"X-MCP-CLIENT-TRACE-ID":                    {"trace_123"},
	}
}

func assertClaimBoundary(
	t *testing.T,
	length int,
	wantValid bool,
	set func(*testContextClaims, *SignedConfig, *string, string),
) {
	t.Helper()
	cfg := *validSignedConfig()
	claims := validTestContextClaims("chat")
	keyID := testKeyID
	set(&claims, &cfg, &keyID, strings.Repeat("a", length))
	resolver := mustTestResolver(t, &cfg)
	snapshot := resolveTestClaims(t, resolver, claims, jwt.SigningMethodHS256, "JWT", keyID, []byte(testSecret))
	if snapshot.Verification.Valid != wantValid {
		t.Fatalf("Valid = %v, want %v (%#v)", snapshot.Verification.Valid, wantValid, snapshot.Verification)
	}
}

func resolveTestClaims(
	t *testing.T,
	resolver *Resolver,
	claims testContextClaims,
	method jwt.SigningMethod,
	typ string,
	keyID string,
	key any,
) Snapshot {
	t.Helper()
	return resolveTestClaimsWithHeaders(t, resolver, claims, method, typ, keyID, key, nil)
}

func resolveTestClaimsWithHeaders(
	t *testing.T,
	resolver *Resolver,
	claims testContextClaims,
	method jwt.SigningMethod,
	typ string,
	keyID string,
	key any,
	headers http.Header,
) Snapshot {
	t.Helper()
	raw := signTestContext(t, method, claims, typ, keyID, key)
	return resolveRawTestToken(t, resolver, raw, headers)
}

func resolveRawTestToken(t *testing.T, resolver *Resolver, raw string, plain http.Header) Snapshot {
	t.Helper()
	headers := plain.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set(testSignedHeader, raw)
	snapshot, err := resolver.Resolve(headers)
	if err != nil {
		t.Fatalf("Resolve(): %v", err)
	}
	return snapshot
}

func replaceSignedPayloadValueWithInvalidUTF8(t *testing.T, raw, value string, key []byte) string {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("compact token has %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("DecodeString(payload): %v", err)
	}
	needle := []byte(`"` + value + `"`)
	replacement := append([]byte{'"'}, 0xff, '"')
	corrupted := bytes.Replace(payload, needle, replacement, 1)
	if bytes.Equal(corrupted, payload) {
		t.Fatalf("payload does not contain %q", value)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(corrupted)
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signingInput))
	parts[2] = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return strings.Join(parts, ".")
}

func makeNonCanonicalCompactJWT(t *testing.T, raw string, key []byte) string {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("compact token has %d parts", len(parts))
	}
	if candidate, ok := nonCanonicalSegment(parts[0]); ok {
		parts[0] = candidate
		return resignCompactParts(parts, key)
	}
	if candidate, ok := nonCanonicalSegment(parts[1]); ok {
		parts[1] = candidate
		return resignCompactParts(parts, key)
	}

	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("DecodeString(header): %v", err)
	}
	for padding := 1; padding <= 2; padding++ {
		padded := append(bytes.Clone(header), bytes.Repeat([]byte{' '}, padding)...)
		canonical := base64.RawURLEncoding.EncodeToString(padded)
		if candidate, ok := nonCanonicalSegment(canonical); ok {
			parts[0] = candidate
			return resignCompactParts(parts, key)
		}
	}
	t.Fatal("could not construct non-canonical raw-base64url segment")
	return ""
}

func nonCanonicalSegment(canonical string) (string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(canonical)
	if err != nil || canonical == "" {
		return "", false
	}
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for index := range len(alphabet) {
		if alphabet[index] == canonical[len(canonical)-1] {
			continue
		}
		candidate := canonical[:len(canonical)-1] + alphabet[index:index+1]
		candidateDecoded, candidateErr := base64.RawURLEncoding.DecodeString(candidate)
		_, strictErr := base64.RawURLEncoding.Strict().DecodeString(candidate)
		if candidateErr == nil && strictErr != nil && bytes.Equal(candidateDecoded, decoded) {
			return candidate, true
		}
	}
	return "", false
}

func resignCompactParts(parts []string, key []byte) string {
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signingInput))
	parts[2] = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return strings.Join(parts, ".")
}
