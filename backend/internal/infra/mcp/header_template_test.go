package mcp

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

var expectedHeaderTemplateTokens = []string{
	"{{DEEIX_USER_PUBLIC_ID}}",
	"{{DEEIX_USER_DISPLAY_NAME}}",
	"{{DEEIX_USER_EMAIL}}",
	"{{DEEIX_USER_ROLE}}",
	"{{DEEIX_CONVERSATION_PUBLIC_ID}}",
	"{{DEEIX_ASSISTANT_MESSAGE_PUBLIC_ID}}",
	"{{DEEIX_USER_MESSAGE_PUBLIC_ID}}",
	"{{DEEIX_REQUEST_ID}}",
	"{{DEEIX_RUN_ID}}",
	"{{DEEIX_TRACE_ID}}",
}

func TestSupportedHeaderTemplateTokensAreExactAndCopying(t *testing.T) {
	t.Parallel()

	got := SupportedHeaderTemplateTokens()
	if !reflect.DeepEqual(got, expectedHeaderTemplateTokens) {
		t.Fatalf("tokens = %#v, want %#v", got, expectedHeaderTemplateTokens)
	}
	got[0] = "mutated"
	if next := SupportedHeaderTemplateTokens(); !reflect.DeepEqual(next, expectedHeaderTemplateTokens) {
		t.Fatalf("catalog was mutated through returned slice: %#v", next)
	}
}

func TestRenderHeaderTemplateUsesDEEIXValues(t *testing.T) {
	t.Parallel()
	template := HeaderTemplate{
		"X-User":        "{{DEEIX_USER_PUBLIC_ID}}/{{DEEIX_USER_DISPLAY_NAME}}/{{DEEIX_USER_EMAIL}}/{{DEEIX_USER_ROLE}}",
		"X-Chat":        "{{DEEIX_CONVERSATION_PUBLIC_ID}}/{{DEEIX_ASSISTANT_MESSAGE_PUBLIC_ID}}/{{DEEIX_USER_MESSAGE_PUBLIC_ID}}",
		"X-Correlation": "{{DEEIX_REQUEST_ID}}/{{DEEIX_RUN_ID}}/{{DEEIX_TRACE_ID}}/{{DEEIX_RUN_ID}}",
	}
	ctx := TemplateContext{
		Mode:                     ContextModeChat,
		UserPublicID:             "user_public",
		UserDisplayName:          "User Name",
		UserEmail:                "user@example.test",
		UserRole:                 "user",
		ConversationPublicID:     "conversation_public",
		AssistantMessagePublicID: "assistant_public",
		UserMessagePublicID:      "message_public",
		RequestID:                "request_public",
		RunID:                    "run_public",
		TraceID:                  "trace_public",
	}
	got, warnings, err := RenderHeaderTemplate(template, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	want := HeaderTemplate{
		"X-User":        "user_public/User Name/user@example.test/user",
		"X-Chat":        "conversation_public/assistant_public/message_public",
		"X-Correlation": "request_public/run_public/trace_public/run_public",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("headers = %#v, want %#v", got, want)
	}
}

func TestRenderHeaderTemplateUsesEmptyStringForKnownMissingValues(t *testing.T) {
	t.Parallel()

	template := HeaderTemplate{"X-Context": "before/{{DEEIX_RUN_ID}}/after"}
	got, warnings, err := RenderHeaderTemplate(template, TemplateContext{Mode: ContextModeProbe})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || got["X-Context"] != "before//after" {
		t.Fatalf("headers=%#v warnings=%#v", got, warnings)
	}
}

func TestRenderHeaderTemplatePreservesUnknownAndMalformedText(t *testing.T) {
	t.Parallel()

	template := HeaderTemplate{
		"X-Unknown": "{{deeix_user_public_id}}/{{deeix_user_public_id}}/{{vendor.token}}/{{OPENWEBUI_USER_ID}}",
		"X-Open":    "prefix {{DEEIX_RUN_ID",
		"X-Close":   "suffix DEEIX_RUN_ID}}",
		"X-Unicode": "租户-{{UNKNOWN_租户}}-✓",
		"X-Nested":  "{{OUTER_{{INNER}}}}",
	}
	original := cloneHeaderTemplateForTest(template)
	got, warnings, err := RenderHeaderTemplate(template, TemplateContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("rendered text changed: got=%#v want=%#v", got, original)
	}
	if !reflect.DeepEqual(template, original) {
		t.Fatalf("input template mutated: got=%#v want=%#v", template, original)
	}
	for _, token := range []string{
		"{{deeix_user_public_id}}", "{{vendor.token}}", "{{OPENWEBUI_USER_ID}}", "{{UNKNOWN_租户}}",
	} {
		if countWarnings(warnings, "unknown_token", token) != 1 {
			t.Fatalf("unknown warning count for %q = %d; warnings=%#v", token, countWarnings(warnings, "unknown_token", token), warnings)
		}
	}
	for _, headerName := range []string{"X-Open", "X-Close", "X-Nested"} {
		if !hasWarning(warnings, "malformed_token", headerName, "") {
			t.Fatalf("missing malformed warning for %q: %#v", headerName, warnings)
		}
	}
}

func TestRenderHeaderTemplateDoesNotMutateInputOnValidationError(t *testing.T) {
	t.Parallel()

	template := HeaderTemplate{"X-Test": "{{DEEIX_REQUEST_ID}}"}
	original := cloneHeaderTemplateForTest(template)
	if _, _, err := RenderHeaderTemplate(template, TemplateContext{RequestID: "bad\rvalue"}); err == nil {
		t.Fatal("expected rendered Header validation error")
	}
	if !reflect.DeepEqual(template, original) {
		t.Fatalf("input template mutated: got=%#v want=%#v", template, original)
	}
}

func TestRenderHeaderTemplatePreservesMalformedKnownTokenNesting(t *testing.T) {
	t.Parallel()

	const malformed = "{{OUTER_{{DEEIX_USER_EMAIL}}}}"
	template := HeaderTemplate{"X-Test": malformed}
	got, warnings, err := RenderHeaderTemplate(template, TemplateContext{UserEmail: "private@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if got["X-Test"] != malformed {
		t.Fatalf("malformed value = %q, want preserved %q", got["X-Test"], malformed)
	}
	if !hasWarning(warnings, "malformed_token", "X-Test", "") {
		t.Fatalf("missing malformed warning: %#v", warnings)
	}
}

func TestRenderHeaderTemplateDoesNotEvaluateReplacementValues(t *testing.T) {
	t.Parallel()

	template := HeaderTemplate{"X-Test": "{{DEEIX_USER_DISPLAY_NAME}}"}
	ctx := TemplateContext{
		UserDisplayName: "{{DEEIX_USER_EMAIL}}",
		UserEmail:       "private@example.test",
	}
	got, warnings, err := RenderHeaderTemplate(template, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || got["X-Test"] != "{{DEEIX_USER_EMAIL}}" {
		t.Fatalf("headers=%#v warnings=%#v", got, warnings)
	}
}

func TestRenderHeaderTemplateEnforcesRenderedAggregateLimit(t *testing.T) {
	t.Parallel()

	template := HeaderTemplate{
		"X-A": strings.Repeat("a", 4096),
		"X-B": strings.Repeat("b", 4096),
		"X-C": strings.Repeat("c", 4096),
		"X-D": strings.Repeat("d", 4096),
	}
	if _, _, err := RenderHeaderTemplate(template, TemplateContext{}); err == nil {
		t.Fatal("expected rendered aggregate limit error")
	}
}

func TestParseHeaderTemplateAnalyzesCandidates(t *testing.T) {
	t.Parallel()

	raw := `{
		"X-A":"{{DEEIX_RUN_ID}}/{{DEEIX_RUN_ID}}/{{deeix_run_id}}/{{deeix_run_id}}",
		"X-B":"{{DEEIX_USER_PUBLIC_ID}}/{{vendor.token}}",
		"X-C":"plain unicode 租户 ✓"
	}`
	parsed, err := ParseHeaderTemplateJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	wantTokens := []string{"{{DEEIX_USER_PUBLIC_ID}}", "{{DEEIX_RUN_ID}}"}
	if !reflect.DeepEqual(parsed.Analysis.Tokens, wantTokens) {
		t.Fatalf("analysis tokens = %#v, want %#v", parsed.Analysis.Tokens, wantTokens)
	}
	if countWarnings(parsed.Analysis.Warnings, "unknown_token", "{{deeix_run_id}}") != 1 ||
		countWarnings(parsed.Analysis.Warnings, "unknown_token", "{{vendor.token}}") != 1 {
		t.Fatalf("warnings = %#v", parsed.Analysis.Warnings)
	}
	if parsed.Template["X-C"] != "plain unicode 租户 ✓" {
		t.Fatalf("unicode value = %q", parsed.Template["X-C"])
	}
}

func TestParseHeaderTemplateCandidateBoundaries(t *testing.T) {
	t.Parallel()

	body128 := strings.Repeat("a", 128)
	parsed, err := ParseHeaderTemplateJSON(`{"X-One":"{{x}}","X-Max":"{{` + body128 + `}}"}`)
	if err != nil {
		t.Fatal(err)
	}
	if countWarnings(parsed.Analysis.Warnings, "unknown_token", "{{x}}") != 1 ||
		countWarnings(parsed.Analysis.Warnings, "unknown_token", "{{"+body128+"}}") != 1 {
		t.Fatalf("warnings = %#v", parsed.Analysis.Warnings)
	}

	tooLong := "{{" + strings.Repeat("a", 129) + "}}"
	parsed, err = ParseHeaderTemplateJSON(`{"X-Test":"` + tooLong + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(parsed.Analysis.Warnings, "malformed_token", "X-Test", "") {
		t.Fatalf("missing malformed warning: %#v", parsed.Analysis.Warnings)
	}
}

func TestParseHeaderTemplateValidatesRawValueBeforeWhitespaceNormalization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
	}{
		{name: "carriage return at edge", raw: `{"X-Test":"\rtenant"}`},
		{name: "line feed at edge", raw: `{"X-Test":"tenant\n"}`},
		{name: "nul", raw: `{"X-Test":"tenant\u0000value"}`},
		{name: "delete", raw: `{"X-Test":"tenant\u007fvalue"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseHeaderTemplateJSON(tt.raw); err == nil {
				t.Fatal("expected invalid raw Header value")
			}
		})
	}
}

func TestParseHeaderTemplatePreservesLegacyValidWhitespaceNormalization(t *testing.T) {
	t.Parallel()
	parsed, err := ParseHeaderTemplateJSON(`{"X-Test":"  tenant-a\t "}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Template["X-Test"]; got != "tenant-a" {
		t.Fatalf("value = %q", got)
	}
}

func TestParseHeaderTemplateDefaultsBlankToEmpty(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", " \t\r\n "} {
		parsed, err := ParseHeaderTemplateJSON(raw)
		if err != nil {
			t.Fatalf("ParseHeaderTemplateJSON(%q): %v", raw, err)
		}
		if len(parsed.Template) != 0 || len(parsed.Analysis.Tokens) != 0 || len(parsed.Analysis.Warnings) != 0 {
			t.Fatalf("parsed blank = %#v", parsed)
		}
	}
}

func TestParseHeaderTemplateRejectsInvalidNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "leading whitespace", raw: `{" X-Test":"value"}`},
		{name: "trailing whitespace", raw: `{"X-Test ":"value"}`},
		{name: "invalid syntax", raw: `{"X:Test":"value"}`},
		{name: "over 128 bytes", raw: `{"` + strings.Repeat("A", 129) + `":"value"}`},
		{name: "exact blacklist different case", raw: `{"aUtHoRiZaTiOn":"value"}`},
		{name: "accept blacklist", raw: `{"accept":"value"}`},
		{name: "mcp prefix", raw: `{"MCP-Anything":"value"}`},
		{name: "proxy prefix", raw: `{"pRoXy-Custom":"value"}`},
		{name: "sec prefix", raw: `{"SEC-Custom":"value"}`},
		{name: "deeix prefix", raw: `{"x-deeix-custom":"value"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseHeaderTemplateJSON(tt.raw); err == nil {
				t.Fatalf("ParseHeaderTemplateJSON(%q) unexpectedly succeeded", tt.raw)
			}
		})
	}
}

func TestParseHeaderTemplateRejectsDuplicateNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "exact duplicate", raw: `{"X-Test":"a","X-Test":"b"}`},
		{name: "case variant canonical duplicate", raw: `{"X-Test":"a","x-test":"b"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseHeaderTemplateJSON(tt.raw); err == nil {
				t.Fatalf("ParseHeaderTemplateJSON(%q) unexpectedly succeeded", tt.raw)
			}
		})
	}
}

func TestParseHeaderTemplateRejectsNonStringJSONValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{name: "null", value: "null"},
		{name: "number", value: "42"},
		{name: "boolean", value: "true"},
		{name: "array", value: `[]`},
		{name: "object", value: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := `{"X-Test":` + tt.value + `}`
			if _, err := ParseHeaderTemplateJSON(raw); err == nil {
				t.Fatalf("ParseHeaderTemplateJSON(%q) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestParseHeaderTemplateEnforcesLimits(t *testing.T) {
	t.Parallel()

	t.Run("33 Headers", func(t *testing.T) {
		t.Parallel()
		values := make(map[string]string, 33)
		for i := range 33 {
			values["X-Header-"+string(rune('A'+i))] = "value"
		}
		raw, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseHeaderTemplateJSON(string(raw)); err == nil {
			t.Fatal("expected Header count limit error")
		}
	})

	t.Run("4097 byte value", func(t *testing.T) {
		t.Parallel()
		_, err := ParseHeaderTemplateJSON(`{"X-Test":"` + strings.Repeat("v", 4097) + `"}`)
		if !errors.Is(err, ErrHeaderTemplateValueTooLarge) {
			t.Fatalf("error = %v, want ErrHeaderTemplateValueTooLarge", err)
		}
	})

	t.Run("raw JSON over 32768 bytes", func(t *testing.T) {
		t.Parallel()
		if _, err := ParseHeaderTemplateJSON(strings.Repeat(" ", 32769)); err == nil {
			t.Fatal("expected raw JSON size limit error")
		}
	})

	t.Run("template aggregate over 16384 bytes", func(t *testing.T) {
		t.Parallel()
		values := map[string]string{
			"X-A": strings.Repeat("a", 4096),
			"X-B": strings.Repeat("b", 4096),
			"X-C": strings.Repeat("c", 4096),
			"X-D": strings.Repeat("d", 4096),
		}
		raw, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseHeaderTemplateJSON(string(raw)); err == nil {
			t.Fatal("expected template aggregate limit error")
		}
	})
}

func TestValidateRenderedCustomHeadersRevalidatesAndDoesNotMutate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers map[string]string
	}{
		{name: "name edge whitespace", headers: map[string]string{" X-Test": "value"}},
		{name: "name too large", headers: map[string]string{strings.Repeat("A", 129): "value"}},
		{name: "blacklisted exact", headers: map[string]string{"cOoKiE": "value"}},
		{name: "blacklisted prefix", headers: map[string]string{"mcp-anything": "value"}},
		{name: "canonical duplicate", headers: map[string]string{"X-Test": "a", "x-test": "b"}},
		{name: "invalid raw value", headers: map[string]string{"X-Test": " bad\n"}},
		{name: "value too large", headers: map[string]string{"X-Test": strings.Repeat("v", 4097)}},
		{name: "aggregate too large", headers: map[string]string{
			"X-A": strings.Repeat("a", 4096),
			"X-B": strings.Repeat("b", 4096),
			"X-C": strings.Repeat("c", 4096),
			"X-D": strings.Repeat("d", 4096),
		}},
	}
	many := make(map[string]string, 33)
	for i := range 33 {
		many["X-Count-"+string(rune('A'+i))] = "value"
	}
	tests = append(tests, struct {
		name    string
		headers map[string]string
	}{name: "too many Headers", headers: many})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original := cloneHeaderTemplateForTest(tt.headers)
			if err := ValidateRenderedCustomHeaders(tt.headers); err == nil {
				t.Fatal("expected validation error")
			}
			if !reflect.DeepEqual(tt.headers, original) {
				t.Fatalf("input mutated: got=%#v want=%#v", tt.headers, original)
			}
		})
	}
}

func TestValidateRenderedCustomHeadersAcceptsBoundaryValues(t *testing.T) {
	t.Parallel()

	headers := map[string]string{
		strings.Repeat("A", 128): strings.Repeat("v", 4096),
		"X-Whitespace":           "  legal\t ",
	}
	original := cloneHeaderTemplateForTest(headers)
	if err := ValidateRenderedCustomHeaders(headers); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(headers, original) {
		t.Fatalf("input mutated: got=%#v want=%#v", headers, original)
	}
}

func cloneHeaderTemplateForTest[T ~map[string]string](input T) T {
	result := make(T, len(input))
	for name, value := range input {
		result[name] = value
	}
	return result
}

func countWarnings(warnings []HeaderTemplateWarning, code string, token string) int {
	count := 0
	for _, warning := range warnings {
		if warning.Code == code && warning.Token == token {
			count++
		}
	}
	return count
}

func hasWarning(warnings []HeaderTemplateWarning, code string, headerName string, token string) bool {
	for _, warning := range warnings {
		if warning.Code != code || warning.HeaderName != headerName {
			continue
		}
		if token == "" || warning.Token == token {
			return true
		}
	}
	return false
}
