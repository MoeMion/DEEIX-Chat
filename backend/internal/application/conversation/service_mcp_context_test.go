package conversation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	domainconversation "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/conversation"
	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	domainuser "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/user"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	inframcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/pkg/traceid"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestNewMCPTemplateContextUsesOnlyAuthoritativePublicValues(t *testing.T) {
	t.Parallel()
	ctx := traceid.WithTraceID(context.Background(), "4bf92f3577b34da6a3ce929d0e0e4736")
	got := newMCPTemplateContext(
		ctx,
		domainuser.User{
			ID: 901234, PublicID: "user-public", Username: "fallback-name",
			DisplayName: "", Email: "user@example.test", Role: domainuser.RoleUser,
		},
		domainconversation.Conversation{ID: 801234, PublicID: "conversation-public"},
		domainconversation.Message{ID: 701234, PublicID: "user-message-public"},
		domainconversation.Message{ID: 601234, PublicID: "assistant-message-public"},
		"request-public",
		"run-public",
	)
	want := inframcp.TemplateContext{
		Mode:                     inframcp.ContextModeChat,
		UserPublicID:             "user-public",
		UserDisplayName:          "fallback-name",
		UserEmail:                "user@example.test",
		UserRole:                 domainuser.RoleUser,
		ConversationPublicID:     "conversation-public",
		AssistantMessagePublicID: "assistant-message-public",
		UserMessagePublicID:      "user-message-public",
		RequestID:                "request-public",
		RunID:                    "run-public",
		TraceID:                  "4bf92f3577b34da6a3ce929d0e0e4736",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("context = %#v, want %#v", got, want)
	}
	serialized := fmt.Sprintf("%#v", got)
	for _, internalID := range []string{"901234", "801234", "701234", "601234"} {
		if strings.Contains(serialized, internalID) {
			t.Fatalf("internal ID %s leaked into %#v", internalID, got)
		}
	}
}

func TestNewMCPTemplateContextPrefersOpenTelemetryTraceID(t *testing.T) {
	t.Parallel()
	otelTraceID, err := oteltrace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := oteltrace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatal(err)
	}
	spanContext := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: otelTraceID,
		SpanID:  spanID,
	})
	ctx := traceid.WithTraceID(context.Background(), "fallback-trace")
	ctx = oteltrace.ContextWithSpanContext(ctx, spanContext)

	got := newMCPTemplateContext(
		ctx,
		domainuser.User{},
		domainconversation.Conversation{},
		domainconversation.Message{},
		domainconversation.Message{},
		"",
		"",
	)
	if got.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace ID = %q", got.TraceID)
	}
}

var errMCPCallConfigFixture = errors.New("call config fixture failed")

type conversationMCPRepoStub struct {
	repository.MCPRepository
}

func (conversationMCPRepoStub) ListToolsByIDs(context.Context, []uint) ([]domainmcp.Tool, error) {
	return []domainmcp.Tool{{
		ID: 1, ServerID: 9, Name: "memory.list", DisplayName: "Memory",
		InputSchemaJSON: `{"type":"object"}`, Status: "active",
	}}, nil
}

func (conversationMCPRepoStub) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	return &domainmcp.Server{
		ID: 9, Name: "Memory", BaseURL: "https://mcp.example.test/mcp", Status: "active",
		UpdatedAt: selectedRuntimeFixtureUpdatedAt,
	}, nil
}

type failingMCPCallConfigBuilder struct{}

func (failingMCPCallConfigBuilder) BuildCallConfig(
	context.Context,
	domainmcp.Server,
	inframcp.TemplateContext,
	int,
) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error) {
	return inframcp.CallConfig{}, inframcp.HeaderTemplateAnalysis{}, errMCPCallConfigFixture
}

func TestResolveSelectedToolRuntimeFailsClosedOnCallConfigError(t *testing.T) {
	t.Parallel()
	service := &Service{
		cfg:         config.NewRuntime(config.Config{MCPEnable: true}),
		mcpRepo:     conversationMCPRepoStub{},
		mcpSessions: &recordingSessionManager{},
	}
	service.SetMCPCallConfigBuilder(failingMCPCallConfigBuilder{})
	_, err := service.resolveSelectedToolRuntime(
		context.Background(),
		[]uint{1},
		inframcp.TemplateContext{
			Mode: inframcp.ContextModeChat, UserPublicID: "user-public", RunID: "run-public",
		},
	)
	if !errors.Is(err, errMCPCallConfigFixture) {
		t.Fatalf("error = %v", err)
	}
}

type multiToolMCPRepoStub struct {
	conversationMCPRepoStub
}

func (multiToolMCPRepoStub) ListToolsByIDs(context.Context, []uint) ([]domainmcp.Tool, error) {
	return []domainmcp.Tool{
		{ID: 1, ServerID: 9, Name: "memory.list", InputSchemaJSON: `{"type":"object"}`, Status: "active"},
		{ID: 2, ServerID: 9, Name: "memory.get", InputSchemaJSON: `{"type":"object"}`, Status: "active"},
	}, nil
}

type countingMCPCallConfigBuilder struct {
	calls int
}

func (b *countingMCPCallConfigBuilder) BuildCallConfig(
	ctx context.Context,
	server domainmcp.Server,
	templateContext inframcp.TemplateContext,
	timeoutMS int,
) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error) {
	b.calls++
	return echoMCPCallConfigBuilder{}.BuildCallConfig(ctx, server, templateContext, timeoutMS)
}

func TestResolveSelectedToolRuntimeBuildsOneCallConfigPerServer(t *testing.T) {
	t.Parallel()
	builder := &countingMCPCallConfigBuilder{}
	manager := &recordingSessionManager{}
	service := &Service{
		cfg:         config.NewRuntime(config.Config{MCPEnable: true, MCPToolTimeoutSeconds: 10}),
		mcpRepo:     multiToolMCPRepoStub{},
		mcpSessions: manager,
	}
	service.SetMCPCallConfigBuilder(builder)

	runtime, err := service.resolveSelectedToolRuntime(
		context.Background(),
		[]uint{1, 2},
		inframcp.TemplateContext{
			Mode:         inframcp.ContextModeChat,
			UserPublicID: "user-public",
			RequestID:    "request-public",
			RunID:        "run-public",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if builder.calls != 1 {
		t.Fatalf("builder calls = %d, want 1", builder.calls)
	}
	if len(runtime.operations) != 2 {
		t.Fatalf("tool operations = %d, want 2", len(runtime.operations))
	}
	if runtime.operations["memory_list"] != runtime.operations["memory_get"] {
		t.Fatal("tools from one Server did not share an operation")
	}
	inputs := manager.acquiredInputs()
	if len(inputs) != 1 {
		t.Fatalf("Acquire calls = %d, want 1", len(inputs))
	}
	if inputs[0].ServerUpdatedAt != selectedRuntimeFixtureUpdatedAt {
		t.Fatalf("ServerUpdatedAt = %v, want %v", inputs[0].ServerUpdatedAt, selectedRuntimeFixtureUpdatedAt)
	}
	if got := inputs[0].CallConfig.CustomHeaders["X-Request"]; got != "request-public" {
		t.Fatalf("Acquire request Header = %q", got)
	}
}

type echoMCPCallConfigBuilder struct{}

func (echoMCPCallConfigBuilder) BuildCallConfig(
	_ context.Context,
	server domainmcp.Server,
	templateContext inframcp.TemplateContext,
	timeoutMS int,
) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error) {
	return inframcp.CallConfig{
		BaseURL:   server.BaseURL,
		TimeoutMS: timeoutMS,
		Context:   templateContext,
		CustomHeaders: map[string]string{
			"X-Request": templateContext.RequestID,
			"X-User":    templateContext.UserPublicID,
		},
	}, inframcp.HeaderTemplateAnalysis{}, nil
}

func TestResolveSelectedToolRuntimeDoesNotCrossConcurrentContexts(t *testing.T) {
	manager := &recordingSessionManager{}
	service := &Service{
		cfg:         config.NewRuntime(config.Config{MCPEnable: true, MCPToolTimeoutSeconds: 10}),
		mcpRepo:     conversationMCPRepoStub{},
		mcpSessions: manager,
	}
	service.SetMCPCallConfigBuilder(echoMCPCallConfigBuilder{})

	type result struct {
		requestID string
		userID    string
		err       error
	}
	results := make(chan result, 2)
	for _, item := range []struct{ requestID, userID string }{
		{requestID: "request-a", userID: "user-a"},
		{requestID: "request-b", userID: "user-b"},
	} {
		item := item
		go func() {
			_, err := service.resolveSelectedToolRuntime(
				context.Background(), []uint{1}, inframcp.TemplateContext{
					Mode: inframcp.ContextModeChat, RequestID: item.requestID,
					UserPublicID: item.userID, RunID: "run-" + item.requestID,
				},
			)
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{requestID: item.requestID, userID: item.userID}
		}()
	}
	seen := map[string]string{}
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		seen[got.requestID] = got.userID
	}
	if !reflect.DeepEqual(seen, map[string]string{
		"request-a": "user-a",
		"request-b": "user-b",
	}) {
		t.Fatalf("crossed contexts: %#v", seen)
	}
	inputs := manager.acquiredInputs()
	if len(inputs) != 2 {
		t.Fatalf("Acquire calls = %d, want 2", len(inputs))
	}
	acquired := map[string]string{}
	for _, input := range inputs {
		acquired[input.CallConfig.CustomHeaders["X-Request"]] = input.CallConfig.CustomHeaders["X-User"]
	}
	if !reflect.DeepEqual(acquired, seen) {
		t.Fatalf("crossed Acquire contexts: %#v", acquired)
	}
}
