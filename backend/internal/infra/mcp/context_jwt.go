package mcp

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/net/http/httpguts"
)

type SignedContextConfig struct {
	Secret         string
	Issuer         string
	Audience       string
	KeyID          string
	ExpiresSeconds int
	IncludeName    bool
	IncludeEmail   bool
	IncludeRole    bool
}

type ContextSigner interface {
	Sign(context TemplateContext, config SignedContextConfig) (string, error)
}

type ContextJWTClaims struct {
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

type JWTContextSigner struct {
	now   func() time.Time
	newID func() string
}

const (
	maxSignedContextHeaderBytes = 8192
	maxContextPublicIDBytes     = 128
	maxContextDisplayNameBytes  = 256
	maxContextEmailBytes        = 320
	maxContextRoleBytes         = 64
	maxContextRequestIDBytes    = 128
	maxContextRunIDBytes        = 64
	maxContextTraceIDBytes      = 64
	maxContextIssuerBytes       = 512
	maxContextAudienceBytes     = 128
	maxContextKeyIDBytes        = 64
	maxContextJTIBytes          = 64
)

var (
	ErrContextSignerUnavailable    = errors.New("mcp context signer unavailable")
	ErrInvalidSignedContext        = errors.New("invalid mcp signed context")
	ErrSignedContextHeaderTooLarge = errors.New("mcp signed context header exceeds limit")
)

var _ ContextSigner = (*JWTContextSigner)(nil)

func NewJWTContextSigner() *JWTContextSigner {
	return newJWTContextSigner(time.Now, func() string {
		return "ctx_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	})
}

func newJWTContextSigner(now func() time.Time, newID func() string) *JWTContextSigner {
	return &JWTContextSigner{now: now, newID: newID}
}

func ValidateSignedContextSecret(secret string) error {
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != secret {
		return invalidSignedContextField("secret")
	}
	return nil
}

func (signer *JWTContextSigner) Sign(context TemplateContext, config SignedContextConfig) (string, error) {
	if signer == nil || signer.now == nil || signer.newID == nil {
		return "", ErrContextSignerUnavailable
	}
	if err := validateSignedContextConfig(config); err != nil {
		return "", err
	}

	subject, err := validateSignedContext(context, config)
	if err != nil {
		return "", err
	}
	jti, err := callJWTContextIDHook(signer.newID)
	if err != nil {
		return "", err
	}
	if err := validateRequiredSignedContextField("jti", jti, maxContextJTIBytes); err != nil {
		return "", err
	}

	now, err := callJWTContextTimeHook(signer.now)
	if err != nil {
		return "", err
	}
	now = now.UTC()
	claims := ContextJWTClaims{
		Mode:                     string(context.Mode),
		ConversationPublicID:     context.ConversationPublicID,
		AssistantMessagePublicID: context.AssistantMessagePublicID,
		UserMessagePublicID:      context.UserMessagePublicID,
		RequestID:                context.RequestID,
		RunID:                    context.RunID,
		TraceID:                  context.TraceID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    config.Issuer,
			Subject:   subject,
			Audience:  jwt.ClaimStrings{config.Audience},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(config.ExpiresSeconds) * time.Second)),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        jti,
		},
	}
	if context.Mode != ContextModeSync {
		if config.IncludeName {
			claims.Name = context.UserDisplayName
		}
		if config.IncludeEmail {
			claims.Email = context.UserEmail
		}
		if config.IncludeRole {
			claims.Role = context.UserRole
		}
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["typ"] = "JWT"
	token.Header["kid"] = config.KeyID
	signed, err := token.SignedString([]byte(config.Secret))
	if err != nil {
		return "", fmt.Errorf("sign mcp context: %w", ErrInvalidSignedContext)
	}
	if len(signed) > maxSignedContextHeaderBytes {
		return "", ErrSignedContextHeaderTooLarge
	}
	if !httpguts.ValidHeaderFieldValue(signed) {
		return "", ErrInvalidSignedContext
	}
	return signed, nil
}

func validateSignedContextConfig(config SignedContextConfig) error {
	if err := ValidateSignedContextSecret(config.Secret); err != nil {
		return err
	}
	if err := validateRequiredSignedContextField("issuer", config.Issuer, maxContextIssuerBytes); err != nil {
		return err
	}
	if err := validateRequiredSignedContextField("audience", config.Audience, maxContextAudienceBytes); err != nil {
		return err
	}
	if err := validateRequiredSignedContextField("key id", config.KeyID, maxContextKeyIDBytes); err != nil {
		return err
	}
	if config.ExpiresSeconds < 60 || config.ExpiresSeconds > 900 {
		return invalidSignedContextField("expires seconds")
	}
	return nil
}

func validateSignedContext(context TemplateContext, config SignedContextConfig) (string, error) {
	var subject string
	switch context.Mode {
	case ContextModeChat, ContextModeProbe:
		subject = strings.TrimSpace(context.UserPublicID)
		if err := validateRequiredSignedContextField("user public id", subject, maxContextPublicIDBytes); err != nil {
			return "", err
		}
	case ContextModeSync:
		subject = "system:mcp-sync"
	default:
		return "", invalidSignedContextField("mode")
	}

	fields := []struct {
		name  string
		value string
		limit int
	}{
		{name: "conversation public id", value: context.ConversationPublicID, limit: maxContextPublicIDBytes},
		{name: "assistant message public id", value: context.AssistantMessagePublicID, limit: maxContextPublicIDBytes},
		{name: "user message public id", value: context.UserMessagePublicID, limit: maxContextPublicIDBytes},
		{name: "request id", value: context.RequestID, limit: maxContextRequestIDBytes},
		{name: "run id", value: context.RunID, limit: maxContextRunIDBytes},
		{name: "trace id", value: context.TraceID, limit: maxContextTraceIDBytes},
	}
	for _, field := range fields {
		if err := validateOptionalSignedContextField(field.name, field.value, field.limit); err != nil {
			return "", err
		}
	}
	if context.Mode != ContextModeSync {
		if config.IncludeName {
			if err := validateOptionalSignedContextField("display name", context.UserDisplayName, maxContextDisplayNameBytes); err != nil {
				return "", err
			}
		}
		if config.IncludeEmail {
			if err := validateOptionalSignedContextField("email", context.UserEmail, maxContextEmailBytes); err != nil {
				return "", err
			}
		}
		if config.IncludeRole {
			if err := validateOptionalSignedContextField("role", context.UserRole, maxContextRoleBytes); err != nil {
				return "", err
			}
		}
	}
	return subject, nil
}

func validateRequiredSignedContextField(name, value string, limit int) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len(value) > limit {
		return invalidSignedContextField(name)
	}
	return nil
}

func validateOptionalSignedContextField(name, value string, limit int) error {
	if !utf8.ValidString(value) || len(value) > limit {
		return invalidSignedContextField(name)
	}
	return nil
}

func callJWTContextTimeHook(hook func() time.Time) (value time.Time, err error) {
	defer func() {
		if recover() != nil {
			value = time.Time{}
			err = ErrContextSignerUnavailable
		}
	}()
	return hook(), nil
}

func callJWTContextIDHook(hook func() string) (value string, err error) {
	defer func() {
		if recover() != nil {
			value = ""
			err = ErrContextSignerUnavailable
		}
	}()
	return hook(), nil
}

func invalidSignedContextField(name string) error {
	return fmt.Errorf("%s: %w", name, ErrInvalidSignedContext)
}
