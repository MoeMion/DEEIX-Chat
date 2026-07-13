package identity

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
)

const (
	SnapshotExtraKey = "deeix.identity.snapshot"

	maximumSignedContextBytes = 8192

	reasonSignedContextUnconfigured = "signed_context_unconfigured"
	reasonSignedContextMissing      = "signed_context_missing"
	reasonSignedContextDuplicate    = "signed_context_duplicate"
	reasonSignedContextTooLarge     = "signed_context_too_large"
	reasonSignedContextInvalid      = "signed_context_invalid"
	reasonSignedContextMismatch     = "signed_context_mismatch"
	reasonSignedContextVerified     = "signed_context_verified"
)

type SignedIdentity struct {
	Subject                  string
	Mode                     string
	Name                     string
	Email                    string
	Role                     string
	ConversationPublicID     string
	AssistantMessagePublicID string
	UserMessagePublicID      string
	RequestID                string
	RunID                    string
	TraceID                  string
}

type Verification struct {
	Present    bool
	Configured bool
	Valid      bool
	Verified   bool
	Reason     string
}

type Snapshot struct {
	Identity       HeaderIdentity
	SignedIdentity *SignedIdentity
	Verification   Verification
	Mismatches     []string
}

type SignedConfig struct {
	Header   string
	Secret   string
	Issuer   string
	Audience string
	KeyID    string
}

type Resolver struct {
	cfg        *SignedConfig
	configured bool
	now        func() time.Time
}

type contextClaims struct {
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

func NewResolver(cfg *SignedConfig, now func() time.Time) (*Resolver, error) {
	if now == nil {
		return nil, errors.New("resolver clock is required")
	}
	resolver := &Resolver{now: now}
	if cfg == nil {
		return resolver, nil
	}

	configCopy := *cfg
	resolver.cfg = &configCopy
	configuredValues := 0
	for _, value := range []string{cfg.Secret, cfg.Issuer, cfg.Audience, cfg.KeyID} {
		if value != "" {
			configuredValues++
		}
	}
	if configuredValues != 0 && configuredValues != 4 {
		return nil, errors.New("signed context configuration must be entirely set or empty")
	}
	if configuredValues == 4 {
		if cfg.Header == "" {
			return nil, errors.New("signed context header is required")
		}
		resolver.configured = true
	}
	return resolver, nil
}

func (r *Resolver) Resolve(headers http.Header) (Snapshot, error) {
	identity, err := ParseHeaders(headers)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		Identity:   identity,
		Mismatches: make([]string, 0),
	}
	if r == nil || r.now == nil {
		return Snapshot{}, errors.New("resolver is required")
	}

	var values []string
	if r.cfg != nil && r.cfg.Header != "" {
		values = matchingHeaderValues(headers, r.cfg.Header)
	}
	snapshot.Verification.Present = len(values) > 0
	snapshot.Verification.Configured = r.configured
	if !r.configured {
		snapshot.Verification.Reason = reasonSignedContextUnconfigured
		return snapshot, nil
	}
	if len(values) == 0 {
		snapshot.Verification.Reason = reasonSignedContextMissing
		return snapshot, nil
	}
	if len(values) != 1 {
		snapshot.Verification.Reason = reasonSignedContextDuplicate
		return snapshot, nil
	}
	if len(values[0]) > maximumSignedContextBytes {
		snapshot.Verification.Reason = reasonSignedContextTooLarge
		return snapshot, nil
	}

	signedIdentity, valid := r.parseSignedContext(values[0])
	if !valid {
		snapshot.Verification.Reason = reasonSignedContextInvalid
		return snapshot, nil
	}
	snapshot.SignedIdentity = &signedIdentity
	snapshot.Verification.Valid = true
	snapshot.Mismatches = identityMismatches(identity, signedIdentity)
	if len(snapshot.Mismatches) != 0 {
		snapshot.Verification.Reason = reasonSignedContextMismatch
		return snapshot, nil
	}
	snapshot.Verification.Verified = true
	snapshot.Verification.Reason = reasonSignedContextVerified
	return snapshot, nil
}

func (r *Resolver) parseSignedContext(raw string) (SignedIdentity, bool) {
	if !compactJWTIsUTF8(raw) {
		return SignedIdentity{}, false
	}

	claims := new(contextClaims)
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithoutClaimsValidation(),
		jwt.WithStrictDecoding(),
	)
	token, err := parser.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("signed context algorithm is invalid")
		}
		return []byte(r.cfg.Secret), nil
	})
	if err != nil || token == nil || !token.Valid {
		return SignedIdentity{}, false
	}
	typ, typOK := token.Header["typ"].(string)
	keyID, keyIDOK := token.Header["kid"].(string)
	if !typOK || typ != "JWT" || !keyIDOK || keyID != r.cfg.KeyID || !validRequiredText(keyID, 64) {
		return SignedIdentity{}, false
	}
	if !r.validClaims(claims) {
		return SignedIdentity{}, false
	}

	return SignedIdentity{
		Subject:                  claims.Subject,
		Mode:                     claims.Mode,
		Name:                     claims.Name,
		Email:                    claims.Email,
		Role:                     claims.Role,
		ConversationPublicID:     claims.ConversationPublicID,
		AssistantMessagePublicID: claims.AssistantMessagePublicID,
		UserMessagePublicID:      claims.UserMessagePublicID,
		RequestID:                claims.RequestID,
		RunID:                    claims.RunID,
		TraceID:                  claims.TraceID,
	}, true
}

func (r *Resolver) validClaims(claims *contextClaims) bool {
	if claims == nil || !validModeAndSubject(claims.Mode, claims.Subject) {
		return false
	}
	if claims.Issuer != r.cfg.Issuer || !validRequiredText(claims.Issuer, 512) {
		return false
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != r.cfg.Audience ||
		!validRequiredText(claims.Audience[0], 128) {
		return false
	}
	if !validRequiredText(claims.ID, 64) ||
		!validOptionalText(claims.Name, 256) ||
		!validOptionalText(claims.Email, 320) ||
		!validOptionalText(claims.Role, 64) ||
		!validOptionalText(claims.ConversationPublicID, 128) ||
		!validOptionalText(claims.AssistantMessagePublicID, 128) ||
		!validOptionalText(claims.UserMessagePublicID, 128) ||
		!validOptionalText(claims.RequestID, 128) ||
		!validOptionalText(claims.RunID, 64) ||
		!validOptionalText(claims.TraceID, 64) {
		return false
	}
	if claims.ExpiresAt == nil || claims.NotBefore == nil || claims.IssuedAt == nil {
		return false
	}

	now := r.now().UTC()
	issuedAt := claims.IssuedAt.Time.UTC()
	notBefore := claims.NotBefore.Time.UTC()
	expiresAt := claims.ExpiresAt.Time.UTC()
	if !issuedAt.Equal(notBefore) || issuedAt.After(now) || !now.Before(expiresAt) {
		return false
	}
	ttl := expiresAt.Sub(issuedAt)
	return ttl >= time.Minute && ttl <= 15*time.Minute
}

func compactJWTIsUTF8(raw string) bool {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts[:2] {
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(part)
		if err != nil || !utf8.Valid(decoded) {
			return false
		}
	}
	return true
}

func validModeAndSubject(mode, subject string) bool {
	if !validRequiredText(subject, 128) || subject != strings.TrimSpace(subject) {
		return false
	}
	switch mode {
	case "chat", "probe":
		return true
	case "sync":
		return subject == "system:mcp-sync"
	default:
		return false
	}
}

func validRequiredText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && validOptionalText(value, limit)
}

func validOptionalText(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value)
}

func identityMismatches(identity HeaderIdentity, signed SignedIdentity) []string {
	mismatches := make([]string, 0)
	if signed.Mode != "sync" {
		mismatches = appendMismatch(mismatches, "userPublicID", identity.UserPublicID, signed.Subject)
	}
	mismatches = appendMismatch(mismatches, "userDisplayName", identity.UserDisplayName, signed.Name)
	mismatches = appendMismatch(mismatches, "userEmail", identity.UserEmail, signed.Email)
	mismatches = appendMismatch(mismatches, "userRole", identity.UserRole, signed.Role)
	mismatches = appendMismatch(mismatches, "conversationPublicID", identity.ConversationPublicID, signed.ConversationPublicID)
	mismatches = appendMismatch(mismatches, "assistantMessagePublicID", identity.AssistantMessagePublicID, signed.AssistantMessagePublicID)
	mismatches = appendMismatch(mismatches, "userMessagePublicID", identity.UserMessagePublicID, signed.UserMessagePublicID)
	mismatches = appendMismatch(mismatches, "requestID", identity.RequestID, signed.RequestID)
	mismatches = appendMismatch(mismatches, "runID", identity.RunID, signed.RunID)
	mismatches = appendMismatch(mismatches, "traceID", identity.TraceID, signed.TraceID)
	return mismatches
}

func appendMismatch(mismatches []string, field, plain, signed string) []string {
	if plain != "" && signed != "" && plain != signed {
		return append(mismatches, field)
	}
	return mismatches
}
