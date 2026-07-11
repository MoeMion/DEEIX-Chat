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

type contextJWTModePreservingBuilder struct {
	context inframcp.TemplateContext
}

func (b *contextJWTModePreservingBuilder) BuildCallConfig(
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

func TestContextJWTConversationBuilderPreservesModes(t *testing.T) {
	tests := []struct {
		name    string
		context inframcp.TemplateContext
	}{
		{
			name: "chat",
			context: inframcp.TemplateContext{
				Mode:                     inframcp.ContextModeChat,
				UserPublicID:             "user-chat",
				ConversationPublicID:     "conversation-public",
				AssistantMessagePublicID: "assistant-public",
				UserMessagePublicID:      "user-message-public",
				RequestID:                "request-chat",
				RunID:                    "run-chat",
			},
		},
		{
			name: "probe",
			context: inframcp.TemplateContext{
				Mode:         inframcp.ContextModeProbe,
				UserPublicID: "user-probe",
				RequestID:    "request-probe",
			},
		},
		{
			name: "sync",
			context: inframcp.TemplateContext{
				Mode:      inframcp.ContextModeSync,
				RequestID: "request-sync",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			builder := &contextJWTModePreservingBuilder{}
			service := &Service{
				cfg: config.NewRuntime(config.Config{
					MCPEnable:             true,
					MCPToolTimeoutSeconds: 10,
				}),
				mcpRepo: conversationMCPRepoStub{},
			}
			service.SetMCPCallConfigBuilder(builder)

			runtime, err := service.resolveSelectedToolRuntime(t.Context(), []uint{1}, test.context)
			if err != nil {
				t.Fatalf("resolveSelectedToolRuntime() error = %v", err)
			}
			if !reflect.DeepEqual(builder.context, test.context) {
				t.Fatalf("builder context = %#v, want %#v", builder.context, test.context)
			}
			callConfig, ok := runtime.mcpConfigs["memory_list"]
			if !ok {
				t.Fatal("resolved runtime is missing memory_list config")
			}
			if !reflect.DeepEqual(callConfig.Context, test.context) {
				t.Fatalf("call config context = %#v, want %#v", callConfig.Context, test.context)
			}
			if callConfig.SignedContext == nil || callConfig.SignedContext.KeyID != "ctx_"+string(test.context.Mode) {
				t.Fatal("signed context was not preserved")
			}
		})
	}
}
