package conversation

import (
	"context"
	"reflect"
	"strings"
	"testing"

	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/llm"
	inframcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
)

type contextJWTChatContextBuilder struct {
	context inframcp.TemplateContext
}

func (b *contextJWTChatContextBuilder) BuildCallConfig(
	_ context.Context,
	server domainmcp.Server,
	templateContext inframcp.TemplateContext,
	timeoutMS int,
) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error) {
	b.context = templateContext
	return inframcp.CallConfig{
		BaseURL:   server.BaseURL,
		TimeoutMS: timeoutMS,
		Context:   templateContext,
		SignedContext: &inframcp.SignedContextConfig{
			KeyID: "ctx_" + string(templateContext.Mode),
		},
	}, inframcp.HeaderTemplateAnalysis{}, nil
}

func TestInjectMCPToolGuidanceOnlyAddsPolicy(t *testing.T) {
	messages := []llm.Message{{Role: "user", Content: "搜索 DEEIX Chat"}}
	runtime := selectedToolRuntime{
		definitions: []llm.ToolDefinition{{
			Name:        "bing_search",
			Description: "搜索网页",
			InputSchema: []byte(`{"type":"object","properties":{"query":{"type":"string"},"count":{"type":"number"}},"required":["query"]}`),
		}},
	}

	result := injectMCPToolGuidance(messages, runtime, "")
	if len(result) != 2 {
		t.Fatalf("expected guidance message to be injected, got %#v", result)
	}
	guidance := result[0].Content
	for _, want := range []string{"# tool_use", "declared separately via the API schema", "Use the fewest useful calls"} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("expected guidance to contain %q, got %q", want, guidance)
		}
	}
	for _, unwanted := range []string{"# tools", "bing_search", "query:string", "count:number"} {
		if strings.Contains(guidance, unwanted) {
			t.Fatalf("expected guidance not to duplicate tool schema %q, got %q", unwanted, guidance)
		}
	}
}

func TestInjectMCPToolGuidanceUsesCustomPrompt(t *testing.T) {
	messages := []llm.Message{{Role: "user", Content: "搜索 DEEIX Chat"}}
	runtime := selectedToolRuntime{
		definitions: []llm.ToolDefinition{{Name: "bing_search"}},
	}

	result := injectMCPToolGuidance(messages, runtime, "Use MCP tools only after checking user intent.")
	if len(result) != 2 {
		t.Fatalf("expected guidance message to be injected, got %#v", result)
	}
	if result[0].Content != "Use MCP tools only after checking user intent." {
		t.Fatalf("expected custom prompt, got %q", result[0].Content)
	}
}

func TestContextJWTConversationBuilderPreservesCompleteChatContext(t *testing.T) {
	templateContext := inframcp.TemplateContext{
		Mode:                     inframcp.ContextModeChat,
		UserPublicID:             "user-chat",
		ConversationPublicID:     "conversation-public",
		AssistantMessagePublicID: "assistant-public",
		UserMessagePublicID:      "user-message-public",
		RequestID:                "request-chat",
		RunID:                    "run-chat",
	}
	builder := &contextJWTChatContextBuilder{}
	manager := &recordingSessionManager{}
	service := &Service{
		cfg: config.NewRuntime(config.Config{
			MCPEnable:             true,
			MCPToolTimeoutSeconds: 10,
		}),
		mcpRepo:     conversationMCPRepoStub{},
		mcpSessions: manager,
	}
	service.SetMCPCallConfigBuilder(builder)

	runtime, err := service.resolveSelectedToolRuntime(t.Context(), []uint{1}, templateContext)
	if err != nil {
		t.Fatalf("resolveSelectedToolRuntime() error = %v", err)
	}
	if !reflect.DeepEqual(builder.context, templateContext) {
		t.Fatalf("builder context = %#v, want %#v", builder.context, templateContext)
	}
	if runtime.operations["memory_list"] == nil {
		t.Fatal("resolved runtime is missing memory_list operation")
	}
	inputs := manager.acquiredInputs()
	if len(inputs) != 1 {
		t.Fatalf("Acquire calls = %d, want 1", len(inputs))
	}
	callConfig := inputs[0].CallConfig
	if !reflect.DeepEqual(callConfig.Context, templateContext) {
		t.Fatalf("call config context = %#v, want %#v", callConfig.Context, templateContext)
	}
	if callConfig.SignedContext == nil || callConfig.SignedContext.KeyID != "ctx_"+string(templateContext.Mode) {
		t.Fatal("signed context was not preserved")
	}
}
