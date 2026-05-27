package conversation

import (
	"strings"
	"testing"

	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/channel"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/llm"
)

func TestResolveMessageSystemPromptInjectionUsesNativeSystemPrompt(t *testing.T) {
	route := &channel.ResolvedRoute{
		Protocol:              llm.AdapterOpenAIResponses,
		ModelSystemPrompt:     "model rule",
		ModelCapabilitiesJSON: `{"supportsSystemPrompt":true}`,
	}

	got := resolveMessageSystemPromptInjection(config.Config{DefaultSystemPrompt: "global rule"}, route, false)
	if got.Content == "" {
		t.Fatal("expected system prompt content")
	}
	if got.InlineToUser {
		t.Fatal("expected native system prompt")
	}
	for _, want := range []string{"Global instructions", "global rule", "Model instructions", "model rule"} {
		if !strings.Contains(got.Content, want) {
			t.Fatalf("expected content to contain %q, got %q", want, got.Content)
		}
	}
}

func TestResolveMessageSystemPromptInjectionAddsHTMLVisualPrompt(t *testing.T) {
	route := &channel.ResolvedRoute{
		Protocol: llm.AdapterOpenAIResponses,
	}

	got := resolveMessageSystemPromptInjection(config.Config{}, route, true)
	if got.Content == "" {
		t.Fatal("expected request-level system prompt content")
	}
	if got.InlineToUser {
		t.Fatal("expected native system prompt")
	}
	for _, want := range []string{"Response format instructions", "html-visual", "遵循用户语言", "HTML 实时渲染"} {
		if !strings.Contains(got.Content, want) {
			t.Fatalf("expected content to contain %q, got %q", want, got.Content)
		}
	}
	if strings.Contains(got.Content, "使用简体中文") {
		t.Fatalf("expected user-language prompt, got %q", got.Content)
	}
}

func TestResolveMessageSystemPromptInjectionSkipsHTMLVisualPromptWhenDisabled(t *testing.T) {
	route := &channel.ResolvedRoute{
		Protocol: llm.AdapterOpenAIResponses,
	}

	got := resolveMessageSystemPromptInjection(config.Config{}, route, false)
	if got.Content != "" {
		t.Fatalf("expected no system prompt content, got %q", got.Content)
	}
}

func TestResolveMessageSystemPromptInjectionFallsBackWhenCapabilitiesDisableSystemPrompt(t *testing.T) {
	route := &channel.ResolvedRoute{
		Protocol:              llm.AdapterOpenAIResponses,
		ModelCapabilitiesJSON: `{"supportsSystemPrompt":false}`,
	}

	got := resolveMessageSystemPromptInjection(config.Config{DefaultSystemPrompt: "global rule"}, route, false)
	if !got.InlineToUser {
		t.Fatal("expected user prompt fallback")
	}
}

func TestResolveMessageSystemPromptInjectionFallsBackWithSnakeCaseCapabilities(t *testing.T) {
	route := &channel.ResolvedRoute{
		Protocol:              llm.AdapterOpenAIResponses,
		ModelCapabilitiesJSON: `{"supports_system_prompt":false}`,
	}

	got := resolveMessageSystemPromptInjection(config.Config{DefaultSystemPrompt: "global rule"}, route, false)
	if !got.InlineToUser {
		t.Fatal("expected snake_case capability to use user prompt fallback")
	}
}

func TestResolveMessageSystemPromptInjectionFallsBackWhenModeRequestsUserPrompt(t *testing.T) {
	route := &channel.ResolvedRoute{
		Protocol:              llm.AdapterOpenAIResponses,
		ModelCapabilitiesJSON: `{"systemPromptMode":"user"}`,
	}

	got := resolveMessageSystemPromptInjection(config.Config{DefaultSystemPrompt: "global rule"}, route, false)
	if !got.InlineToUser {
		t.Fatal("expected systemPromptMode=user to use user prompt fallback")
	}
}

func TestResolveMessageSystemPromptInjectionFallsBackForGemma(t *testing.T) {
	route := &channel.ResolvedRoute{
		PlatformModelName: "gemma-3-27b",
		Protocol:          llm.AdapterGoogleGenerateContent,
	}

	got := resolveMessageSystemPromptInjection(config.Config{DefaultSystemPrompt: "global rule"}, route, false)
	if !got.InlineToUser {
		t.Fatal("expected Gemma to inline system prompt into user prompt")
	}
}

func TestInlineSystemPromptIntoLatestUserMessage(t *testing.T) {
	messages := []llm.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "second"},
	}

	got := inlineSystemPromptIntoLatestUserMessage(messages, "system rule")
	if got[0].Content != "first" {
		t.Fatalf("expected first user message to stay unchanged, got %q", got[0].Content)
	}
	if !strings.Contains(got[2].Content, "<system_instructions>") || !strings.Contains(got[2].Content, "system rule") || !strings.Contains(got[2].Content, "second") {
		t.Fatalf("expected latest user message to include inline system prompt and original content, got %q", got[2].Content)
	}
}
