package mcp

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/security"
	"golang.org/x/net/http/httpguts"
)

type ContextMode string

const (
	ContextModeChat  ContextMode = "chat"
	ContextModeProbe ContextMode = "probe"
	ContextModeSync  ContextMode = "sync"
)

type TemplateContext struct {
	Mode                     ContextMode
	UserPublicID             string
	UserDisplayName          string
	UserEmail                string
	UserRole                 string
	ConversationPublicID     string
	AssistantMessagePublicID string
	UserMessagePublicID      string
	RequestID                string
	RunID                    string
	TraceID                  string
}

type HeaderTemplate map[string]string

type HeaderTemplateWarning struct {
	Code       string
	HeaderName string
	Token      string
}

type HeaderTemplateAnalysis struct {
	Tokens              []string
	Warnings            []HeaderTemplateWarning
	SignedContextHeader string
}

type ParsedHeaderTemplate struct {
	Template HeaderTemplate
	Analysis HeaderTemplateAnalysis
}

type TokenDefinition struct {
	Token string
	Value func(TemplateContext) string
}

const (
	SignedContextTemplateToken     = "{{DEEIX_SIGNED_CONTEXT}}"
	RecommendedSignedContextHeader = "X-MCP-CLIENT-SIGNED-CONTEXT"

	maxHeaderCount               = 32
	maxHeaderNameBytes           = 128
	maxHeaderValueBytes          = 4096
	maxHeaderTemplateJSONBytes   = 32768
	maxRenderedHeaderBytes       = 16384
	maxHeaderTokenBodyCharacters = 128
)

var (
	ErrInvalidHeaderTemplateValue  = errors.New("invalid mcp Header template value")
	ErrHeaderTemplateValueTooLarge = errors.New("mcp Header template value exceeds limit")

	headerTemplateTokenCatalog = []TokenDefinition{
		{Token: "{{DEEIX_USER_PUBLIC_ID}}", Value: func(ctx TemplateContext) string { return ctx.UserPublicID }},
		{Token: "{{DEEIX_USER_DISPLAY_NAME}}", Value: func(ctx TemplateContext) string { return ctx.UserDisplayName }},
		{Token: "{{DEEIX_USER_EMAIL}}", Value: func(ctx TemplateContext) string { return ctx.UserEmail }},
		{Token: "{{DEEIX_USER_ROLE}}", Value: func(ctx TemplateContext) string { return ctx.UserRole }},
		{Token: "{{DEEIX_CONVERSATION_PUBLIC_ID}}", Value: func(ctx TemplateContext) string { return ctx.ConversationPublicID }},
		{Token: "{{DEEIX_ASSISTANT_MESSAGE_PUBLIC_ID}}", Value: func(ctx TemplateContext) string { return ctx.AssistantMessagePublicID }},
		{Token: "{{DEEIX_USER_MESSAGE_PUBLIC_ID}}", Value: func(ctx TemplateContext) string { return ctx.UserMessagePublicID }},
		{Token: "{{DEEIX_REQUEST_ID}}", Value: func(ctx TemplateContext) string { return ctx.RequestID }},
		{Token: "{{DEEIX_RUN_ID}}", Value: func(ctx TemplateContext) string { return ctx.RunID }},
		{Token: "{{DEEIX_TRACE_ID}}", Value: func(ctx TemplateContext) string { return ctx.TraceID }},
		{Token: SignedContextTemplateToken, Value: func(TemplateContext) string { return SignedContextTemplateToken }},
	}

	blacklistedHeaderNames = []string{
		"Accept",
		"Accept-Encoding",
		"Authorization",
		"Baggage",
		"Connection",
		"Content-Length",
		"Content-Type",
		"Cookie",
		"Host",
		"Keep-Alive",
		"Last-Event-ID",
		"MCP-Protocol-Version",
		"MCP-Session-Id",
		"Origin",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Proxy-Connection",
		"Set-Cookie",
		"TE",
		"Traceparent",
		"Tracestate",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
		"User-Agent",
		"X-DEEIX-Context",
	}

	blacklistedHeaderPrefixes = []string{
		"MCP-",
		"Proxy-",
		"Sec-",
		"X-DEEIX-",
	}
)

func SupportedHeaderTemplateTokens() []string {
	tokens := make([]string, len(headerTemplateTokenCatalog))
	for i, definition := range headerTemplateTokenCatalog {
		tokens[i] = definition.Token
	}
	return tokens
}

func ParseHeaderTemplateJSON(raw string) (ParsedHeaderTemplate, error) {
	if len(raw) > maxHeaderTemplateJSONBytes {
		return ParsedHeaderTemplate{}, errors.New("mcp Header template JSON exceeds limit")
	}
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}

	values, err := security.ParseHeaderStringMapJSON(raw)
	if err != nil {
		return ParsedHeaderTemplate{}, fmt.Errorf("parse mcp Header template: %w", err)
	}
	if len(values) > maxHeaderCount {
		return ParsedHeaderTemplate{}, errors.New("mcp Header template has too many Headers")
	}

	template := make(HeaderTemplate, len(values))
	canonicalNames := make(map[string]struct{}, len(values))
	totalBytes := 0
	names := sortedHeaderNames(values)
	for _, name := range names {
		if err := validateHeaderName(name, canonicalNames); err != nil {
			return ParsedHeaderTemplate{}, err
		}
		value, err := normalizeTemplateHeaderValue(values[name])
		if err != nil {
			return ParsedHeaderTemplate{}, fmt.Errorf("Header %q: %w", name, err)
		}
		totalBytes += len(name) + len(value)
		if totalBytes > maxRenderedHeaderBytes {
			return ParsedHeaderTemplate{}, errors.New("mcp Header template exceeds aggregate limit")
		}
		template[name] = value
	}

	analysis, err := inspectHeaderTemplate(template)
	if err != nil {
		return ParsedHeaderTemplate{}, err
	}
	return ParsedHeaderTemplate{Template: template, Analysis: analysis}, nil
}

func RenderHeaderTemplate(template HeaderTemplate, ctx TemplateContext) (HeaderTemplate, []HeaderTemplateWarning, error) {
	analysis, err := inspectHeaderTemplate(template)
	if err != nil {
		return nil, analysis.Warnings, err
	}
	rendered := make(HeaderTemplate, len(template))
	for name, value := range template {
		if name == analysis.SignedContextHeader {
			continue
		}
		rendered[name] = renderHeaderTemplateValue(value, ctx)
	}
	if err := ValidateRenderedCustomHeaders(rendered); err != nil {
		return nil, analysis.Warnings, err
	}
	return rendered, analysis.Warnings, nil
}

func ValidateRenderedCustomHeaders(headers map[string]string) error {
	return validateCustomHeadersWithSignedContext(headers, "", "", false)
}

func validateCustomHeadersWithSignedContext(
	headers map[string]string,
	signedContextHeader string,
	signedContextValue string,
	hasSignedContextValue bool,
) error {
	headerCount := len(headers)
	if signedContextHeader != "" {
		headerCount++
	}
	if headerCount > maxHeaderCount {
		return errors.New("mcp custom Headers exceed count limit")
	}

	canonicalNames := make(map[string]struct{}, headerCount)
	totalBytes := 0
	for _, name := range sortedHeaderNames(headers) {
		if err := validateHeaderName(name, canonicalNames); err != nil {
			return err
		}
		value := headers[name]
		if len(value) > maxHeaderValueBytes {
			return ErrHeaderTemplateValueTooLarge
		}
		if !httpguts.ValidHeaderFieldValue(value) {
			return ErrInvalidHeaderTemplateValue
		}
		totalBytes += len(name) + len(value)
		if totalBytes > maxRenderedHeaderBytes {
			return errors.New("mcp custom Headers exceed aggregate limit")
		}
	}
	if signedContextHeader == "" {
		return nil
	}
	if err := validateHeaderName(signedContextHeader, canonicalNames); err != nil {
		return err
	}
	totalBytes += len(signedContextHeader)
	if hasSignedContextValue {
		if signedContextValue == "" || len(signedContextValue) > maxSignedContextHeaderBytes ||
			!httpguts.ValidHeaderFieldValue(signedContextValue) {
			return ErrInvalidSignedContext
		}
		totalBytes += len(signedContextValue)
	}
	if totalBytes > maxRenderedHeaderBytes {
		return errors.New("mcp custom Headers exceed aggregate limit")
	}
	return nil
}

func normalizeTemplateHeaderValue(raw string) (string, error) {
	if len(raw) > maxHeaderValueBytes {
		return "", ErrHeaderTemplateValueTooLarge
	}
	if !httpguts.ValidHeaderFieldValue(raw) {
		return "", ErrInvalidHeaderTemplateValue
	}
	return strings.TrimSpace(raw), nil
}

func validateHeaderName(name string, seen map[string]struct{}) error {
	if len(name) > maxHeaderNameBytes {
		return errors.New("mcp custom Header name exceeds limit")
	}
	if name != strings.TrimSpace(name) {
		return errors.New("mcp custom Header name has edge whitespace")
	}
	if !httpguts.ValidHeaderFieldName(name) {
		return errors.New("invalid mcp custom Header name")
	}
	if isBlacklistedHeaderName(name) {
		return errors.New("mcp custom Header name is reserved")
	}

	canonicalName := strings.ToLower(name)
	if _, duplicate := seen[canonicalName]; duplicate {
		return errors.New("duplicate canonical mcp custom Header name")
	}
	seen[canonicalName] = struct{}{}
	return nil
}

func isBlacklistedHeaderName(name string) bool {
	for _, blacklisted := range blacklistedHeaderNames {
		if strings.EqualFold(name, blacklisted) {
			return true
		}
	}
	lowerName := strings.ToLower(name)
	for _, prefix := range blacklistedHeaderPrefixes {
		if strings.HasPrefix(lowerName, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

type headerTemplateCandidate struct {
	Token string
	Start int
	End   int
}

func analyzeHeaderTemplate(template HeaderTemplate) HeaderTemplateAnalysis {
	knownTokens := make(map[string]struct{})
	warnings := make([]HeaderTemplateWarning, 0)
	for _, name := range sortedHeaderNames(template) {
		value := template[name]
		unknownSeen := make(map[string]struct{})
		malformed := false
		for _, candidate := range scanHeaderTemplateCandidates(value, &malformed) {
			if isKnownHeaderTemplateToken(candidate.Token) {
				knownTokens[candidate.Token] = struct{}{}
				continue
			}
			if _, duplicate := unknownSeen[candidate.Token]; duplicate {
				continue
			}
			unknownSeen[candidate.Token] = struct{}{}
			warnings = append(warnings, HeaderTemplateWarning{
				Code:       "unknown_token",
				HeaderName: name,
				Token:      candidate.Token,
			})
		}
		if malformed {
			warnings = append(warnings, HeaderTemplateWarning{
				Code:       "malformed_token",
				HeaderName: name,
			})
		}
	}

	tokens := make([]string, 0, len(knownTokens))
	for _, definition := range headerTemplateTokenCatalog {
		if _, present := knownTokens[definition.Token]; present {
			tokens = append(tokens, definition.Token)
		}
	}
	return HeaderTemplateAnalysis{Tokens: tokens, Warnings: warnings}
}

func inspectHeaderTemplate(template HeaderTemplate) (HeaderTemplateAnalysis, error) {
	analysis := analyzeHeaderTemplate(template)
	for _, name := range sortedHeaderNames(template) {
		value := template[name]
		if !strings.Contains(value, SignedContextTemplateToken) {
			continue
		}
		normalized, err := normalizeTemplateHeaderValue(value)
		if err != nil {
			return analysis, fmt.Errorf("Header %q: %w", name, err)
		}
		if normalized != SignedContextTemplateToken {
			return analysis, fmt.Errorf("Header %q: signed context token must be the complete value", name)
		}
		if analysis.SignedContextHeader != "" {
			return analysis, errors.New("mcp Header template has multiple signed context bindings")
		}
		analysis.SignedContextHeader = name
	}
	if analysis.SignedContextHeader == "" {
		return analysis, nil
	}

	canonicalNames := make(map[string]struct{}, len(template))
	for _, name := range sortedHeaderNames(template) {
		if err := validateHeaderName(name, canonicalNames); err != nil {
			return analysis, err
		}
	}
	return analysis, nil
}

func scanHeaderTemplateCandidates(value string, malformed *bool) []headerTemplateCandidate {
	candidates := make([]headerTemplateCandidate, 0)
	for offset := 0; offset < len(value); {
		remainder := value[offset:]
		openIndex := strings.Index(remainder, "{{")
		closeIndex := strings.Index(remainder, "}}")
		if closeIndex >= 0 && (openIndex < 0 || closeIndex < openIndex) {
			*malformed = true
			offset += closeIndex + 2
			continue
		}
		if openIndex < 0 {
			break
		}

		start := offset + openIndex
		closingFromBody := strings.Index(value[start+2:], "}}")
		if closingFromBody < 0 {
			*malformed = true
			break
		}
		end := start + 2 + closingFromBody
		body := value[start+2 : end]
		if body == "" || utf8.RuneCountInString(body) > maxHeaderTokenBodyCharacters || strings.ContainsAny(body, "{}\r\n") {
			*malformed = true
		} else {
			candidates = append(candidates, headerTemplateCandidate{
				Token: value[start : end+2],
				Start: start,
				End:   end + 2,
			})
		}
		offset = end + 2
	}
	return candidates
}

func renderHeaderTemplateValue(value string, ctx TemplateContext) string {
	malformed := false
	candidates := scanHeaderTemplateCandidates(value, &malformed)
	var rendered strings.Builder
	last := 0
	for _, candidate := range candidates {
		definition, ok := findHeaderTemplateToken(candidate.Token)
		if !ok {
			continue
		}
		rendered.WriteString(value[last:candidate.Start])
		rendered.WriteString(strings.ReplaceAll(candidate.Token, definition.Token, definition.Value(ctx)))
		last = candidate.End
	}
	if last == 0 {
		return value
	}
	rendered.WriteString(value[last:])
	return rendered.String()
}

func findHeaderTemplateToken(candidate string) (TokenDefinition, bool) {
	for _, definition := range headerTemplateTokenCatalog {
		if candidate == definition.Token {
			return definition, true
		}
	}
	return TokenDefinition{}, false
}

func isKnownHeaderTemplateToken(candidate string) bool {
	_, ok := findHeaderTemplateToken(candidate)
	return ok
}

func sortedHeaderNames[T ~map[string]string](headers T) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
