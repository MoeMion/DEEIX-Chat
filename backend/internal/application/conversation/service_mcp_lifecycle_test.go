package conversation

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	inframcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
)

type recordingSessionManager struct {
	mu         sync.Mutex
	inputs     []inframcp.AcquireInput
	operations map[uint]inframcp.Operation
	returnNil  bool
}

func (m *recordingSessionManager) Acquire(_ context.Context, input inframcp.AcquireInput) (inframcp.Operation, error) {
	if _, err := inframcp.BuildSessionKey(input); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inputs = append(m.inputs, input)
	if m.returnNil {
		return nil, nil
	}
	if m.operations == nil {
		m.operations = make(map[uint]inframcp.Operation)
	}
	if operation := m.operations[input.ServerID]; operation != nil {
		return operation, nil
	}
	operation := &callCountingOperation{}
	m.operations[input.ServerID] = operation
	return operation, nil
}

var selectedRuntimeFixtureUpdatedAt = time.Date(2026, time.July, 12, 9, 0, 0, 0, time.UTC)

func (*recordingSessionManager) OpenEphemeral(context.Context, inframcp.CallConfig, int) (inframcp.Operation, func(context.Context) error, error) {
	return nil, nil, nil
}

func (*recordingSessionManager) CloseRun(context.Context, string, string) error { return nil }

func (*recordingSessionManager) CloseAll(context.Context) error { return nil }

func (m *recordingSessionManager) acquiredInputs() []inframcp.AcquireInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]inframcp.AcquireInput(nil), m.inputs...)
}

type selectedRuntimeRepoStub struct {
	repository.MCPRepository
	tools   []domainmcp.Tool
	servers map[uint]domainmcp.Server
}

func (r selectedRuntimeRepoStub) ListToolsByIDs(context.Context, []uint) ([]domainmcp.Tool, error) {
	return append([]domainmcp.Tool(nil), r.tools...), nil
}

func (r selectedRuntimeRepoStub) GetServer(_ context.Context, serverID uint) (*domainmcp.Server, error) {
	server := r.servers[serverID]
	return &server, nil
}

type completeCallConfigBuilder struct{}

func (completeCallConfigBuilder) BuildCallConfig(
	_ context.Context,
	server domainmcp.Server,
	templateContext inframcp.TemplateContext,
	timeoutMS int,
) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error) {
	return inframcp.CallConfig{
		BaseURL:       server.BaseURL,
		AuthToken:     "auth-" + server.Name,
		TimeoutMS:     timeoutMS,
		CustomHeaders: map[string]string{"X-Server": server.Name},
		Context:       templateContext,
		SignedContext: &inframcp.SignedContextConfig{
			Secret:         "secret-" + server.Name,
			Issuer:         "https://chat.example.test",
			Audience:       "urn:server:" + server.Name,
			KeyID:          "key-" + server.Name,
			ExpiresSeconds: 120,
			IncludeName:    true,
		},
	}, inframcp.HeaderTemplateAnalysis{}, nil
}

func TestResolveSelectedToolRuntimeAcquiresOncePerServer(t *testing.T) {
	updatedA := time.Date(2026, time.July, 12, 10, 0, 0, 0, time.UTC)
	updatedB := updatedA.Add(time.Minute)
	repo := selectedRuntimeRepoStub{
		tools: []domainmcp.Tool{
			{ID: 1, ServerID: 9, Name: "memory.list", InputSchemaJSON: `{}`, Status: "active"},
			{ID: 2, ServerID: 9, Name: "memory.get", InputSchemaJSON: `{}`, Status: "active"},
			{ID: 3, ServerID: 10, Name: "web.search", InputSchemaJSON: `{}`, Status: "active"},
		},
		servers: map[uint]domainmcp.Server{
			9:  {ID: 9, Name: "memory", BaseURL: "https://same.example.test/mcp", Status: "active", UpdatedAt: updatedA},
			10: {ID: 10, Name: "web", BaseURL: "https://same.example.test/mcp", Status: "active", UpdatedAt: updatedB},
		},
	}
	manager := &recordingSessionManager{}
	templateContext := inframcp.TemplateContext{
		Mode:         inframcp.ContextModeChat,
		UserPublicID: "user-public",
		RequestID:    "request-public",
		RunID:        "run-public",
	}
	service := &Service{
		cfg: config.NewRuntime(config.Config{
			MCPEnable:             true,
			MCPToolTimeoutSeconds: 7,
			MCPToolRetryCount:     5,
		}),
		mcpRepo:          repo,
		mcpConfigBuilder: completeCallConfigBuilder{},
		mcpSessions:      manager,
	}

	runtime, err := service.resolveSelectedToolRuntime(t.Context(), []uint{1, 2, 3}, templateContext)
	if err != nil {
		t.Fatal(err)
	}
	inputs := manager.acquiredInputs()
	if len(inputs) != 2 {
		t.Fatalf("Acquire calls = %d, want 2", len(inputs))
	}
	if inputs[0].ServerID != 9 || inputs[0].ServerUpdatedAt != updatedA || inputs[0].RetryCount != 5 {
		t.Fatalf("first Acquire input = %#v", inputs[0])
	}
	if inputs[1].ServerID != 10 || inputs[1].ServerUpdatedAt != updatedB || inputs[1].RetryCount != 5 {
		t.Fatalf("second Acquire input = %#v", inputs[1])
	}
	for index, input := range inputs {
		if input.CallConfig.Context.UserPublicID != "user-public" || input.CallConfig.Context.RunID != "run-public" {
			t.Fatalf("Acquire %d authoritative identity = user %q run %q", index, input.CallConfig.Context.UserPublicID, input.CallConfig.Context.RunID)
		}
	}
	if inputs[0].CallConfig.AuthToken == inputs[1].CallConfig.AuthToken ||
		inputs[0].CallConfig.CustomHeaders["X-Server"] != "memory" ||
		inputs[1].CallConfig.CustomHeaders["X-Server"] != "web" ||
		inputs[0].CallConfig.SignedContext == nil || inputs[1].CallConfig.SignedContext == nil ||
		inputs[0].CallConfig.SignedContext.Audience != "urn:server:memory" ||
		inputs[1].CallConfig.SignedContext.Audience != "urn:server:web" {
		t.Fatalf("per-Server acquisition context crossed boundaries: first=%#v second=%#v", inputs[0].CallConfig, inputs[1].CallConfig)
	}
	wantFirstConfig, _, err := (completeCallConfigBuilder{}).BuildCallConfig(
		t.Context(), repo.servers[9], templateContext, 7000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inputs[0].CallConfig, wantFirstConfig) {
		t.Fatalf("first CallConfig = %#v, want %#v", inputs[0].CallConfig, wantFirstConfig)
	}
	memoryList := runtime.operations["memory_list"]
	if memoryList == nil || memoryList != runtime.operations["memory_get"] {
		t.Fatal("tools from one Server did not share one operation")
	}
	if memoryList == runtime.operations["web_search"] {
		t.Fatal("different Server IDs sharing a URL reused one operation")
	}
	if runtime.operationsByServer[9] != memoryList || runtime.operationsByServer[10] != runtime.operations["web_search"] {
		t.Fatalf("private per-Server operation cache = %#v", runtime.operationsByServer)
	}
}

func TestResolveSelectedToolRuntimeFailsClosedWithoutSessionManager(t *testing.T) {
	service := &Service{
		cfg:              config.NewRuntime(config.Config{MCPEnable: true}),
		mcpRepo:          conversationMCPRepoStub{},
		mcpConfigBuilder: echoMCPCallConfigBuilder{},
	}
	_, err := service.resolveSelectedToolRuntime(t.Context(), []uint{1}, inframcp.TemplateContext{
		Mode: inframcp.ContextModeChat, UserPublicID: "user-public", RunID: "run-public",
	})
	if err != ErrSelectedToolUnavailable {
		t.Fatalf("error = %v, want ErrSelectedToolUnavailable", err)
	}
}

func TestResolveSelectedToolRuntimeFailsClosedWhenAcquireReturnsNil(t *testing.T) {
	manager := &recordingSessionManager{returnNil: true}
	service := &Service{
		cfg:              config.NewRuntime(config.Config{MCPEnable: true, MCPToolTimeoutSeconds: 10}),
		mcpRepo:          conversationMCPRepoStub{},
		mcpConfigBuilder: echoMCPCallConfigBuilder{},
		mcpSessions:      manager,
	}
	_, err := service.resolveSelectedToolRuntime(t.Context(), []uint{1}, inframcp.TemplateContext{
		Mode: inframcp.ContextModeChat, UserPublicID: "user-public", RunID: "run-public",
	})
	if err != ErrSelectedToolUnavailable {
		t.Fatalf("error = %v, want ErrSelectedToolUnavailable", err)
	}
}
