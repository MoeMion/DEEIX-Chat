package mcp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWTContextSignerSignsChatClaims(t *testing.T) {
	fixedNow := time.Date(2026, time.July, 10, 8, 0, 0, 123456789, time.FixedZone("test", 8*60*60))
	const fixedJTI = "ctx_test_jti"
	secret := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	config := SignedContextConfig{
		Secret:         secret,
		Issuer:         "https://chat.example.com",
		Audience:       "urn:deeix:mcp:mcp_server_public_id",
		KeyID:          "ctx_key_id",
		ExpiresSeconds: 300,
	}
	context := TemplateContext{
		Mode:                     ContextModeChat,
		UserPublicID:             "  usr_public_id  ",
		UserDisplayName:          "Private Name",
		UserEmail:                "private@example.com",
		UserRole:                 "admin",
		ConversationPublicID:     "conv_public_id",
		AssistantMessagePublicID: "assistant_message_public_id",
		UserMessagePublicID:      "user_message_public_id",
		RequestID:                "request_id",
		RunID:                    "run_id",
		TraceID:                  "trace_id",
	}

	signed, err := newJWTContextSigner(func() time.Time { return fixedNow }, func() string { return fixedJTI }).Sign(context, config)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if len(signed) > 8192 {
		t.Fatalf("Sign() token length = %d, want <= 8192", len(signed))
	}

	claims := ContextJWTClaims{}
	token, err := jwt.ParseWithClaims(
		signed,
		&claims,
		func(token *jwt.Token) (any, error) { return []byte(secret), nil },
		jwt.WithTimeFunc(func() time.Time { return fixedNow.UTC() }),
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(config.Issuer),
		jwt.WithAudience(config.Audience),
	)
	if err != nil {
		t.Fatalf("ParseWithClaims() error = %v", err)
	}
	if !token.Valid {
		t.Fatal("ParseWithClaims() token is not valid")
	}
	if got := token.Method.Alg(); got != "HS256" {
		t.Errorf("alg = %q, want HS256", got)
	}
	if got := token.Header["typ"]; got != "JWT" {
		t.Errorf("typ = %#v, want JWT", got)
	}
	if got := token.Header["kid"]; got != config.KeyID {
		t.Errorf("kid = %#v, want %q", got, config.KeyID)
	}
	if len(token.Header) != 3 {
		t.Errorf("JWT Header = %#v, want only alg, typ, and kid", token.Header)
	}
	if claims.Issuer != config.Issuer {
		t.Errorf("iss = %q, want %q", claims.Issuer, config.Issuer)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != config.Audience {
		t.Errorf("aud = %#v, want [%q]", claims.Audience, config.Audience)
	}
	if claims.Subject != "usr_public_id" {
		t.Errorf("sub = %q, want usr_public_id", claims.Subject)
	}
	if claims.ID != fixedJTI {
		t.Errorf("jti = %q, want %q", claims.ID, fixedJTI)
	}
	assertNumericDateEqual(t, "iat", claims.IssuedAt, fixedNow.UTC())
	assertNumericDateEqual(t, "nbf", claims.NotBefore, fixedNow.UTC())
	assertNumericDateEqual(t, "exp", claims.ExpiresAt, fixedNow.UTC().Add(300*time.Second))
	if claims.Mode != string(ContextModeChat) {
		t.Errorf("mode = %q, want %q", claims.Mode, ContextModeChat)
	}
	if claims.ConversationPublicID != context.ConversationPublicID ||
		claims.AssistantMessagePublicID != context.AssistantMessagePublicID ||
		claims.UserMessagePublicID != context.UserMessagePublicID ||
		claims.RequestID != context.RequestID ||
		claims.RunID != context.RunID ||
		claims.TraceID != context.TraceID {
		t.Errorf("context claims = %#v, want IDs from %#v", claims, context)
	}

	payload := jwtPayload(t, signed)
	for _, key := range []string{"name", "email", "role"} {
		if _, present := payload[key]; present {
			t.Errorf("default JWT unexpectedly contains %q", key)
		}
	}
	if _, present := payload["message_id"]; present {
		t.Error("JWT unexpectedly contains compatibility message_id claim")
	}
	expectedPayloadKeys := []string{
		"iss", "aud", "sub", "exp", "nbf", "iat", "jti", "mode",
		"conversation_id", "assistant_message_id", "user_message_id",
		"request_id", "run_id", "trace_id",
	}
	if len(payload) != len(expectedPayloadKeys) {
		t.Errorf("JWT payload keys = %#v, want exactly %v", payload, expectedPayloadKeys)
	}
	for _, key := range expectedPayloadKeys {
		if _, present := payload[key]; !present {
			t.Errorf("JWT payload missing %q", key)
		}
	}

	decodedSecret, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		t.Fatalf("decode fixture secret: %v", err)
	}
	if _, err := jwt.Parse(
		signed,
		func(token *jwt.Token) (any, error) { return decodedSecret, nil },
		jwt.WithTimeFunc(func() time.Time { return fixedNow.UTC() }),
		jwt.WithValidMethods([]string{"HS256"}),
	); !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
		t.Fatalf("Parse() with decoded HMAC bytes error = %v, want signature invalid", err)
	}
}

func TestJWTContextSignerIncludesOnlyOptedInPII(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*SignedContextConfig)
		claim      string
		claimValue string
	}{
		{
			name:       "name",
			configure:  func(config *SignedContextConfig) { config.IncludeName = true },
			claim:      "name",
			claimValue: baseTemplateContext().UserDisplayName,
		},
		{
			name:       "email",
			configure:  func(config *SignedContextConfig) { config.IncludeEmail = true },
			claim:      "email",
			claimValue: baseTemplateContext().UserEmail,
		},
		{
			name:       "role",
			configure:  func(config *SignedContextConfig) { config.IncludeRole = true },
			claim:      "role",
			claimValue: baseTemplateContext().UserRole,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := baseSignedContextConfig()
			test.configure(&config)
			signed, err := fixedJWTContextSigner("ctx_opt_in").Sign(baseTemplateContext(), config)
			if err != nil {
				t.Fatalf("Sign() error = %v", err)
			}

			payload := jwtPayload(t, signed)
			for _, claim := range []string{"name", "email", "role"} {
				value, present := payload[claim]
				if claim == test.claim {
					if !present || string(value) != quoteJSON(t, test.claimValue) {
						t.Errorf("claim %q = %s, present %t, want %q", claim, value, present, test.claimValue)
					}
					continue
				}
				if present {
					t.Errorf("claim %q unexpectedly present as %s", claim, value)
				}
			}
		})
	}
}

func TestJWTContextSignerAppliesModeSubjectRules(t *testing.T) {
	tests := []struct {
		name        string
		mode        ContextMode
		userID      string
		wantSubject string
		wantError   bool
	}{
		{name: "chat trims user public id", mode: ContextModeChat, userID: "  usr_chat  ", wantSubject: "usr_chat"},
		{name: "probe trims user public id", mode: ContextModeProbe, userID: "\tusr_probe\n", wantSubject: "usr_probe"},
		{name: "sync uses system subject", mode: ContextModeSync, userID: "usr_ignored", wantSubject: "system:mcp-sync"},
		{name: "chat requires user public id", mode: ContextModeChat, userID: " \t\r\n", wantError: true},
		{name: "probe requires user public id", mode: ContextModeProbe, userID: "", wantError: true},
		{name: "unknown mode is rejected", mode: ContextMode("future"), userID: "usr_public_id", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			context := baseTemplateContext()
			context.Mode = test.mode
			context.UserPublicID = test.userID
			config := baseSignedContextConfig()
			config.IncludeName = true
			config.IncludeEmail = true
			config.IncludeRole = true

			signed, err := fixedJWTContextSigner("ctx_mode").Sign(context, config)
			if test.wantError {
				requireSafeSigningError(t, signed, err, ErrInvalidSignedContext, "")
				return
			}
			if err != nil {
				t.Fatalf("Sign() error = %v", err)
			}
			claims := parseContextClaims(t, signed, config)
			if claims.Subject != test.wantSubject {
				t.Errorf("sub = %q, want %q", claims.Subject, test.wantSubject)
			}
			if claims.Mode != string(test.mode) {
				t.Errorf("mode = %q, want %q", claims.Mode, test.mode)
			}
			if test.mode == ContextModeSync {
				payload := jwtPayload(t, signed)
				for _, claim := range []string{"name", "email", "role"} {
					if _, present := payload[claim]; present {
						t.Errorf("sync JWT unexpectedly contains %q", claim)
					}
				}
			}
		})
	}
}

func TestJWTContextSignerValidatesTTLAndRequiredConfig(t *testing.T) {
	for _, ttl := range []int{60, 900} {
		t.Run("accepts TTL "+time.Duration(ttl).String(), func(t *testing.T) {
			config := baseSignedContextConfig()
			config.ExpiresSeconds = ttl
			signed, err := fixedJWTContextSigner("ctx_ttl").Sign(baseTemplateContext(), config)
			if err != nil {
				t.Fatalf("Sign() error = %v", err)
			}
			claims := parseContextClaims(t, signed, config)
			wantExpiry := fixedSignerTime().Add(time.Duration(ttl) * time.Second).Truncate(time.Second)
			assertNumericDateEqual(t, "exp", claims.ExpiresAt, wantExpiry)
		})
	}

	invalid := []struct {
		name       string
		fieldClass string
		mutate     func(*SignedContextConfig)
	}{
		{name: "TTL below minimum", fieldClass: "expires seconds", mutate: func(config *SignedContextConfig) { config.ExpiresSeconds = 59 }},
		{name: "TTL above maximum", fieldClass: "expires seconds", mutate: func(config *SignedContextConfig) { config.ExpiresSeconds = 901 }},
		{name: "missing issuer", fieldClass: "issuer", mutate: func(config *SignedContextConfig) { config.Issuer = " \t" }},
		{name: "missing audience", fieldClass: "audience", mutate: func(config *SignedContextConfig) { config.Audience = "" }},
		{name: "missing key id", fieldClass: "key id", mutate: func(config *SignedContextConfig) { config.KeyID = "\r\n" }},
		{name: "missing secret", fieldClass: "secret", mutate: func(config *SignedContextConfig) { config.Secret = "" }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			config := baseSignedContextConfig()
			test.mutate(&config)
			signed, err := fixedJWTContextSigner("ctx_invalid_config").Sign(baseTemplateContext(), config)
			requireSafeSigningError(t, signed, err, ErrInvalidSignedContext, "")
			if !strings.Contains(err.Error(), test.fieldClass) {
				t.Errorf("error %q does not identify field class %q", err, test.fieldClass)
			}
		})
	}
}

func TestJWTContextSignerValidatesCanonicalSecret(t *testing.T) {
	valid := canonicalTestSecret()
	if err := ValidateSignedContextSecret(valid); err != nil {
		t.Fatalf("ValidateSignedContextSecret(valid) error = %v", err)
	}

	nonCanonical := nonCanonicalRawURLSecret(t, valid)
	tests := []struct {
		name   string
		secret string
	}{
		{name: "missing", secret: ""},
		{name: "malformed", secret: "not/base64"},
		{name: "padded", secret: valid + "="},
		{name: "31 decoded bytes", secret: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 31)))},
		{name: "33 decoded bytes", secret: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 33)))},
		{name: "non canonical pad bits", secret: nonCanonical},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateSignedContextSecret(test.secret)
			if !errors.Is(err, ErrInvalidSignedContext) {
				t.Fatalf("ValidateSignedContextSecret() error = %v, want ErrInvalidSignedContext", err)
			}
			if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Errorf("error %q leaks rejected secret", err)
			}

			config := baseSignedContextConfig()
			config.Secret = test.secret
			signed, signErr := fixedJWTContextSigner("ctx_secret").Sign(baseTemplateContext(), config)
			requireSafeSigningError(t, signed, signErr, ErrInvalidSignedContext, test.secret)
		})
	}
}

func TestJWTContextSignerEnforcesEveryByteLimit(t *testing.T) {
	tests := []struct {
		name       string
		fieldClass string
		limit      int
		apply      func(*TemplateContext, *SignedContextConfig, string)
		jti        bool
	}{
		{name: "user public id", fieldClass: "user public id", limit: 128, apply: func(context *TemplateContext, _ *SignedContextConfig, value string) { context.UserPublicID = value }},
		{name: "conversation public id", fieldClass: "conversation public id", limit: 128, apply: func(context *TemplateContext, _ *SignedContextConfig, value string) {
			context.ConversationPublicID = value
		}},
		{name: "assistant message public id", fieldClass: "assistant message public id", limit: 128, apply: func(context *TemplateContext, _ *SignedContextConfig, value string) {
			context.AssistantMessagePublicID = value
		}},
		{name: "user message public id", fieldClass: "user message public id", limit: 128, apply: func(context *TemplateContext, _ *SignedContextConfig, value string) {
			context.UserMessagePublicID = value
		}},
		{name: "display name", fieldClass: "display name", limit: 256, apply: func(context *TemplateContext, config *SignedContextConfig, value string) {
			context.UserDisplayName, config.IncludeName = value, true
		}},
		{name: "email", fieldClass: "email", limit: 320, apply: func(context *TemplateContext, config *SignedContextConfig, value string) {
			context.UserEmail, config.IncludeEmail = value, true
		}},
		{name: "role", fieldClass: "role", limit: 64, apply: func(context *TemplateContext, config *SignedContextConfig, value string) {
			context.UserRole, config.IncludeRole = value, true
		}},
		{name: "request id", fieldClass: "request id", limit: 128, apply: func(context *TemplateContext, _ *SignedContextConfig, value string) { context.RequestID = value }},
		{name: "run id", fieldClass: "run id", limit: 64, apply: func(context *TemplateContext, _ *SignedContextConfig, value string) { context.RunID = value }},
		{name: "trace id", fieldClass: "trace id", limit: 64, apply: func(context *TemplateContext, _ *SignedContextConfig, value string) { context.TraceID = value }},
		{name: "issuer", fieldClass: "issuer", limit: 512, apply: func(_ *TemplateContext, config *SignedContextConfig, value string) { config.Issuer = value }},
		{name: "audience", fieldClass: "audience", limit: 128, apply: func(_ *TemplateContext, config *SignedContextConfig, value string) { config.Audience = value }},
		{name: "key id", fieldClass: "key id", limit: 64, apply: func(_ *TemplateContext, config *SignedContextConfig, value string) { config.KeyID = value }},
		{name: "jti", fieldClass: "jti", limit: 64, apply: func(_ *TemplateContext, _ *SignedContextConfig, _ string) {}, jti: true},
	}

	for _, test := range tests {
		t.Run(test.name+" accepts maximum", func(t *testing.T) {
			context := baseTemplateContext()
			config := baseSignedContextConfig()
			value := strings.Repeat("x", test.limit)
			test.apply(&context, &config, value)
			jti := "ctx_limit"
			if test.jti {
				jti = value
			}
			if _, err := fixedJWTContextSigner(jti).Sign(context, config); err != nil {
				t.Fatalf("Sign() at %s byte limit error = %v", test.name, err)
			}
		})

		t.Run(test.name+" rejects over maximum", func(t *testing.T) {
			context := baseTemplateContext()
			config := baseSignedContextConfig()
			value := strings.Repeat("z", test.limit+1)
			test.apply(&context, &config, value)
			jti := "ctx_limit"
			if test.jti {
				jti = value
			}
			signed, err := fixedJWTContextSigner(jti).Sign(context, config)
			requireSafeSigningError(t, signed, err, ErrInvalidSignedContext, value)
			if !strings.Contains(err.Error(), test.fieldClass) {
				t.Errorf("error %q does not identify field class %q", err, test.fieldClass)
			}
		})

		t.Run(test.name+" rejects invalid UTF-8", func(t *testing.T) {
			context := baseTemplateContext()
			config := baseSignedContextConfig()
			value := string([]byte{'x', 0xff, 'y'})
			test.apply(&context, &config, value)
			jti := "ctx_invalid_utf8"
			if test.jti {
				jti = value
			}
			signed, err := fixedJWTContextSigner(jti).Sign(context, config)
			requireSafeSigningError(t, signed, err, ErrInvalidSignedContext, value)
			if !strings.Contains(err.Error(), test.fieldClass) {
				t.Errorf("error %q does not identify field class %q", err, test.fieldClass)
			}
		})
	}

	t.Run("limits count UTF-8 bytes", func(t *testing.T) {
		context := baseTemplateContext()
		context.UserPublicID = strings.Repeat("界", 43)
		signed, err := fixedJWTContextSigner("ctx_utf8").Sign(context, baseSignedContextConfig())
		requireSafeSigningError(t, signed, err, ErrInvalidSignedContext, context.UserPublicID)
	})
}

func TestJWTContextSignerRejectsExpandedHeaderAbove8192Bytes(t *testing.T) {
	control := func(count int) string { return strings.Repeat("\x00", count) }
	context := TemplateContext{
		Mode:                     ContextModeChat,
		UserPublicID:             control(128),
		UserDisplayName:          control(256),
		UserEmail:                control(320),
		UserRole:                 control(64),
		ConversationPublicID:     control(128),
		AssistantMessagePublicID: control(128),
		UserMessagePublicID:      control(128),
		RequestID:                control(128),
		RunID:                    control(64),
		TraceID:                  control(64),
	}
	config := SignedContextConfig{
		Secret:         canonicalTestSecret(),
		Issuer:         control(512),
		Audience:       control(128),
		KeyID:          control(64),
		ExpiresSeconds: 300,
		IncludeName:    true,
		IncludeEmail:   true,
		IncludeRole:    true,
	}

	signed, err := fixedJWTContextSigner(control(64)).Sign(context, config)
	requireSafeSigningError(t, signed, err, ErrSignedContextHeaderTooLarge, control(512))
}

func TestJWTContextSignerProductionConstructorUsesPrefixedDashlessUUIDs(t *testing.T) {
	signer := NewJWTContextSigner()
	if signer == nil || signer.now == nil || signer.newID == nil {
		t.Fatal("NewJWTContextSigner() returned an unavailable signer")
	}
	if id := signer.newID(); !regexp.MustCompile(`^ctx_[0-9a-f]{32}$`).MatchString(id) {
		t.Errorf("generated jti = %q, want ctx_ plus dashless UUID", id)
	}
	var _ ContextSigner = signer
}

func TestJWTContextSignerUnavailableHooksFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		signer *JWTContextSigner
	}{
		{name: "nil receiver", signer: nil},
		{name: "zero value", signer: &JWTContextSigner{}},
		{name: "missing clock", signer: &JWTContextSigner{newID: func() string { return "ctx_id" }}},
		{name: "missing id generator", signer: &JWTContextSigner{now: fixedSignerTime}},
		{
			name: "panicking clock",
			signer: &JWTContextSigner{
				now:   func() time.Time { panic("clock entropy unavailable") },
				newID: func() string { return "ctx_id" },
			},
		},
		{
			name: "panicking id generator",
			signer: &JWTContextSigner{
				now:   fixedSignerTime,
				newID: func() string { panic("uuid entropy unavailable") },
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signed, err := test.signer.Sign(baseTemplateContext(), baseSignedContextConfig())
			requireSafeSigningError(t, signed, err, ErrContextSignerUnavailable, "")
		})
	}
}

func assertNumericDateEqual(t *testing.T, name string, got *jwt.NumericDate, want time.Time) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is nil", name)
	}
	if !got.Time.Equal(want.Truncate(time.Second)) {
		t.Errorf("%s = %s, want %s", name, got.Time, want.Truncate(time.Second))
	}
}

func fixedSignerTime() time.Time {
	return time.Date(2026, time.July, 10, 8, 0, 0, 123456789, time.UTC)
}

func fixedJWTContextSigner(jti string) *JWTContextSigner {
	return newJWTContextSigner(fixedSignerTime, func() string { return jti })
}

func canonicalTestSecret() string {
	return base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
}

func baseSignedContextConfig() SignedContextConfig {
	return SignedContextConfig{
		Secret:         canonicalTestSecret(),
		Issuer:         "https://chat.example.com",
		Audience:       "urn:deeix:mcp:mcp_server_public_id",
		KeyID:          "ctx_key_id",
		ExpiresSeconds: 300,
	}
}

func baseTemplateContext() TemplateContext {
	return TemplateContext{
		Mode:                     ContextModeChat,
		UserPublicID:             "usr_public_id",
		UserDisplayName:          "Private Name",
		UserEmail:                "private@example.com",
		UserRole:                 "admin",
		ConversationPublicID:     "conv_public_id",
		AssistantMessagePublicID: "assistant_message_public_id",
		UserMessagePublicID:      "user_message_public_id",
		RequestID:                "request_id",
		RunID:                    "run_id",
		TraceID:                  "trace_id",
	}
}

func parseContextClaims(t *testing.T, signed string, config SignedContextConfig) ContextJWTClaims {
	t.Helper()
	claims := ContextJWTClaims{}
	token, err := jwt.ParseWithClaims(
		signed,
		&claims,
		func(token *jwt.Token) (any, error) { return []byte(config.Secret), nil },
		jwt.WithTimeFunc(fixedSignerTime),
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(config.Issuer),
		jwt.WithAudience(config.Audience),
	)
	if err != nil {
		t.Fatalf("ParseWithClaims() error = %v", err)
	}
	if !token.Valid {
		t.Fatal("ParseWithClaims() token is not valid")
	}
	return claims
}

func jwtPayload(t *testing.T, signed string) map[string]json.RawMessage {
	t.Helper()
	parts := strings.Split(signed, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d segments, want 3", len(parts))
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	payload := make(map[string]json.RawMessage)
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		t.Fatalf("unmarshal JWT payload: %v", err)
	}
	return payload
}

func quoteJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal expected JSON string: %v", err)
	}
	return string(encoded)
}

func nonCanonicalRawURLSecret(t *testing.T, canonical string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, canonical[len(canonical)-1])
	if last < 0 || last%4 != 0 {
		t.Fatalf("fixture secret has unexpected terminal base64 index %d", last)
	}
	nonCanonical := canonical[:len(canonical)-1] + string(alphabet[last+1])
	decoded, err := base64.RawURLEncoding.DecodeString(nonCanonical)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("non-canonical fixture does not decode to 32 bytes: len=%d err=%v", len(decoded), err)
	}
	if base64.RawURLEncoding.EncodeToString(decoded) == nonCanonical {
		t.Fatal("non-canonical fixture unexpectedly re-encodes identically")
	}
	return nonCanonical
}

func requireSafeSigningError(t *testing.T, signed string, err, want error, rejectedValue string) {
	t.Helper()
	if signed != "" {
		t.Errorf("Sign() returned token on error: length %d", len(signed))
	}
	if !errors.Is(err, want) {
		t.Fatalf("Sign() error = %v, want %v", err, want)
	}
	if rejectedValue != "" && strings.Contains(err.Error(), rejectedValue) {
		t.Errorf("Sign() error leaks rejected claim/config value")
	}
}
