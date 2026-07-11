package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	systemeventapp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/systemevent"
	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	domainuser "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/user"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	inframcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
	model "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/persistence/models"
	postgresmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/persistence/postgres/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/pkg/secretbox"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testMCPEncryptionKey = "test-mcp-data-encryption-key"

type captureMCPToolLister struct {
	calls []inframcp.CallConfig
}

func (c *captureMCPToolLister) ListTools(_ context.Context, cfg inframcp.CallConfig) ([]inframcp.Tool, error) {
	c.calls = append(c.calls, cfg)
	return []inframcp.Tool{{Name: "memory.list", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}

type mcpApplicationRepoStub struct {
	repository.MCPRepository
	server      domainmcp.Server
	replaced    int
	storedTools []domainmcp.Tool
}

func (r *mcpApplicationRepoStub) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	copy := r.server
	return &copy, nil
}

func (r *mcpApplicationRepoStub) ReplaceServerTools(_ context.Context, _ uint, tools []domainmcp.Tool) error {
	r.replaced++
	r.storedTools = append([]domainmcp.Tool(nil), tools...)
	return nil
}

func (r *mcpApplicationRepoStub) ListTools(context.Context, uint, bool) ([]domainmcp.Tool, error) {
	return append([]domainmcp.Tool(nil), r.storedTools...), nil
}

type userProfileResolverStub struct {
	user domainuser.User
}

func (r userProfileResolverStub) GetByID(context.Context, uint) (*domainuser.User, error) {
	copy := r.user
	return &copy, nil
}

func TestServiceProbeServerAndSyncServerUseAuthoritativeModes(t *testing.T) {
	t.Parallel()
	repo := &mcpApplicationRepoStub{server: domainmcp.Server{
		ID:          9,
		Name:        "Memory",
		BaseURL:     "https://mcp.example.test/mcp",
		HeadersJSON: `{"X-Subject":"{{DEEIX_USER_PUBLIC_ID}}","X-Run":"{{DEEIX_RUN_ID}}","X-Request":"{{DEEIX_REQUEST_ID}}"}`,
		Status:      "active",
	}}
	lister := &captureMCPToolLister{}
	service := NewServiceWithRuntime(
		config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}),
		repo,
		lister,
	)
	service.SetUserProfileResolver(userProfileResolverStub{user: domainuser.User{
		ID: 3, PublicID: "user-admin", Username: "admin", DisplayName: "Admin",
		Email: "admin@example.test", Role: domainuser.RoleAdmin,
	}})

	probe, err := service.ProbeServer(context.Background(), ProbeServerInput{
		ServerID: 9, ActorUserID: 3, RequestID: "request-probe",
	})
	if err != nil {
		t.Fatal(err)
	}
	if probe.ToolCount != 1 || repo.replaced != 0 {
		t.Fatalf("probe = %#v replaced=%d", probe, repo.replaced)
	}
	_, err = service.SyncServerTools(context.Background(), SyncServerToolsInput{
		ServerID: 9, RequestID: "request-sync",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.replaced != 1 || len(lister.calls) != 2 {
		t.Fatalf("replaced=%d calls=%d", repo.replaced, len(lister.calls))
	}

	probeCfg, syncCfg := lister.calls[0], lister.calls[1]
	if probeCfg.Context.Mode != inframcp.ContextModeProbe ||
		probeCfg.Context.UserPublicID != "user-admin" ||
		probeCfg.Context.ConversationPublicID != "" ||
		probeCfg.Context.AssistantMessagePublicID != "" ||
		probeCfg.Context.UserMessagePublicID != "" ||
		probeCfg.Context.RunID != "" ||
		probeCfg.Context.RequestID != "request-probe" ||
		probeCfg.CustomHeaders["X-Subject"] != "user-admin" ||
		probeCfg.CustomHeaders["X-Run"] != "" {
		t.Fatalf("probe config = %#v", probeCfg)
	}
	if syncCfg.Context.Mode != inframcp.ContextModeSync ||
		syncCfg.Context.UserPublicID != "" ||
		syncCfg.Context.UserEmail != "" ||
		syncCfg.Context.ConversationPublicID != "" ||
		syncCfg.Context.AssistantMessagePublicID != "" ||
		syncCfg.Context.UserMessagePublicID != "" ||
		syncCfg.Context.RunID != "" ||
		syncCfg.Context.RequestID != "request-sync" ||
		syncCfg.CustomHeaders["X-Subject"] != "" ||
		syncCfg.CustomHeaders["X-Run"] != "" {
		t.Fatalf("sync config = %#v", syncCfg)
	}
}

func TestServiceBuildCallConfigUsesStrictHeaderTemplateKernel(t *testing.T) {
	t.Parallel()
	runtime := config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"})
	service := NewServiceWithRuntime(runtime, &mcpApplicationRepoStub{}, nil)
	encrypted, err := secretbox.EncryptString("test-data-key", "bearer-secret")
	if err != nil {
		t.Fatal(err)
	}
	templateContext := inframcp.TemplateContext{
		Mode:         inframcp.ContextModeProbe,
		UserPublicID: "user-authoritative",
		RequestID:    "request-probe",
	}
	callConfig, analysis, err := service.BuildCallConfig(context.Background(), domainmcp.Server{
		BaseURL:      " https://mcp.example.test/mcp ",
		AuthTokenEnc: encrypted,
		HeadersJSON:  `{"X-Subject":"{{DEEIX_USER_PUBLIC_ID}}","X-Warn":"{{UNKNOWN_TOKEN}}"}`,
	}, templateContext, 4321)
	if err != nil {
		t.Fatal(err)
	}
	if callConfig.BaseURL != "https://mcp.example.test/mcp" ||
		callConfig.AuthToken != "bearer-secret" ||
		callConfig.TimeoutMS != 4321 ||
		callConfig.Context != templateContext ||
		callConfig.CustomHeaders["X-Subject"] != "user-authoritative" ||
		callConfig.CustomHeaders["X-Warn"] != "{{UNKNOWN_TOKEN}}" {
		t.Fatalf("call config = %#v", callConfig)
	}
	if !reflect.DeepEqual(analysis.Tokens, []string{"{{DEEIX_USER_PUBLIC_ID}}"}) ||
		len(analysis.Warnings) != 1 ||
		analysis.Warnings[0].Code != "unknown_token" ||
		analysis.Warnings[0].HeaderName != "X-Warn" ||
		analysis.Warnings[0].Token != "{{UNKNOWN_TOKEN}}" {
		t.Fatalf("analysis = %#v", analysis)
	}
}

func TestServiceBuildCallConfigRejectsUnsafeTargetAndInvalidTemplate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		server  domainmcp.Server
		wantErr error
	}{
		{
			name:    "query target",
			server:  domainmcp.Server{BaseURL: "https://mcp.example.test/mcp?token=secret", HeadersJSON: "{}"},
			wantErr: ErrUnsafeMCPServerTarget,
		},
		{
			name:    "reserved header",
			server:  domainmcp.Server{BaseURL: "https://mcp.example.test/mcp", HeadersJSON: `{"Authorization":"secret"}`},
			wantErr: ErrInvalidHeaderTemplate,
		},
		{
			name:    "raw control byte",
			server:  domainmcp.Server{BaseURL: "https://mcp.example.test/mcp", HeadersJSON: `{"X-Test":"\rvalue"}`},
			wantErr: ErrInvalidHeaderTemplate,
		},
	}
	service := NewServiceWithRuntime(
		config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}),
		&mcpApplicationRepoStub{},
		nil,
	)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := service.BuildCallConfig(context.Background(), tt.server, inframcp.TemplateContext{}, 1000)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("BuildCallConfig error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestServiceCRUDUsesStrictHeaderTemplateValidation(t *testing.T) {
	t.Parallel()
	invalidTemplates := []string{
		`{" X-Test":"value"}`,
		`{"Authorization":"secret"}`,
		`{"X-Test":"\rvalue"}`,
		`{"X-Test":1}`,
	}
	for _, raw := range invalidTemplates {
		raw := raw
		t.Run("create "+raw, func(t *testing.T) {
			t.Parallel()
			repo := &mcpRepositoryStub{}
			_, err := newTestMCPService(repo).CreateServer(context.Background(), CreateServerInput{
				Name: "Strict", BaseURL: "https://example.com/mcp", HeadersJSON: raw, Status: "active",
			})
			if !errors.Is(err, ErrInvalidServerHeaders) || repo.createCalls != 0 {
				t.Fatalf("CreateServer error=%v writes=%d", err, repo.createCalls)
			}
		})
		t.Run("update "+raw, func(t *testing.T) {
			t.Parallel()
			repo := &mcpRepositoryStub{server: testMCPServer()}
			_, err := newTestMCPService(repo).UpdateServer(context.Background(), repo.server.ID, UpdateServerInput{HeadersJSON: &raw})
			if !errors.Is(err, ErrInvalidServerHeaders) || repo.updateCalls != 0 {
				t.Fatalf("UpdateServer error=%v writes=%d", err, repo.updateCalls)
			}
		})
	}
}

type panicUserProfileResolver struct{}

func (panicUserProfileResolver) GetByID(context.Context, uint) (*domainuser.User, error) {
	panic("preview loaded a real user")
}

func TestServicePreviewHeaderTemplateUsesFixedContextsAndRedaction(t *testing.T) {
	t.Parallel()
	service := NewServiceWithRuntime(
		config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}),
		&mcpApplicationRepoStub{},
		nil,
	)
	service.SetUserProfileResolver(panicUserProfileResolver{})
	raw := `{"X-A":"{{DEEIX_USER_PUBLIC_ID}}|{{DEEIX_CONVERSATION_PUBLIC_ID}}|{{DEEIX_RUN_ID}}|{{DEEIX_REQUEST_ID}}","X-API-Key":"synthetic-{{DEEIX_USER_EMAIL}}","X-Z":"{{UNKNOWN_TOKEN}}"}`
	tests := []struct {
		mode  inframcp.ContextMode
		value string
	}{
		{mode: inframcp.ContextModeChat, value: "user_example|conversation_example|run_example|request_example"},
		{mode: inframcp.ContextModeProbe, value: "user_example|||request_example"},
		{mode: inframcp.ContextModeSync, value: "|||request_example"},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			t.Parallel()
			result, err := service.PreviewHeaderTemplate(context.Background(), raw, tt.mode)
			if err != nil {
				t.Fatal(err)
			}
			if result.Mode != tt.mode || !reflect.DeepEqual(result.SupportedTokens, inframcp.SupportedHeaderTemplateTokens()) {
				t.Fatalf("preview metadata = %#v", result)
			}
			if len(result.Headers) != 3 ||
				result.Headers[0] != (HeaderPreviewItem{Name: "X-A", Value: tt.value}) ||
				result.Headers[1] != (HeaderPreviewItem{Name: "X-API-Key", Value: "********", Sensitive: true}) ||
				result.Headers[2] != (HeaderPreviewItem{Name: "X-Z", Value: "{{UNKNOWN_TOKEN}}"}) {
				t.Fatalf("preview headers = %#v", result.Headers)
			}
			if len(result.Warnings) != 1 || result.Warnings[0].Code != "unknown_token" || result.Warnings[0].Token != "{{UNKNOWN_TOKEN}}" {
				t.Fatalf("preview warnings = %#v", result.Warnings)
			}
		})
	}
}

func TestServicePreviewHeaderTemplateRejectsUnsupportedMode(t *testing.T) {
	t.Parallel()
	service := NewServiceWithRuntime(config.NewRuntime(config.Config{}), &mcpApplicationRepoStub{}, nil)
	_, err := service.PreviewHeaderTemplate(context.Background(), `{}`, inframcp.ContextMode("browser"))
	if !errors.Is(err, ErrInvalidHeaderTemplateMode) {
		t.Fatalf("PreviewHeaderTemplate error = %v", err)
	}
}

type failingMCPToolLister struct {
	err   error
	calls int
}

func (l *failingMCPToolLister) ListTools(context.Context, inframcp.CallConfig) ([]inframcp.Tool, error) {
	l.calls++
	return nil, l.err
}

type safeFailureMCPRepoStub struct {
	repository.MCPRepository
	server   domainmcp.Server
	updates  []repository.UpdateMCPServerInput
	replaced int
}

func (r *safeFailureMCPRepoStub) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	copy := r.server
	return &copy, nil
}

func (r *safeFailureMCPRepoStub) UpdateServer(_ context.Context, _ uint, input repository.UpdateMCPServerInput) (*domainmcp.Server, error) {
	r.updates = append(r.updates, input)
	copy := r.server
	return &copy, nil
}

func (r *safeFailureMCPRepoStub) ReplaceServerTools(context.Context, uint, []domainmcp.Tool) error {
	r.replaced++
	return nil
}

type captureSystemEventWriter struct {
	inputs []systemeventapp.WriteInput
}

func (w *captureSystemEventWriter) Write(_ context.Context, input systemeventapp.WriteInput) {
	w.inputs = append(w.inputs, input)
}

func TestServiceProbeAndSyncFailuresAreStableAndSafe(t *testing.T) {
	t.Parallel()
	const secret = "remote-bearer-secret"
	remoteErr := errors.New("remote body " + secret + " admin@example.test https://mcp.example.test/mcp?token=" + secret)

	t.Run("probe", func(t *testing.T) {
		t.Parallel()
		repo := &safeFailureMCPRepoStub{server: domainmcp.Server{ID: 9, BaseURL: "https://mcp.example.test/mcp", HeadersJSON: "{}"}}
		lister := &failingMCPToolLister{err: remoteErr}
		service := NewServiceWithRuntime(config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}), repo, lister)
		service.SetUserProfileResolver(userProfileResolverStub{user: domainuser.User{PublicID: "actor", Email: "admin@example.test"}})
		_, err := service.ProbeServer(context.Background(), ProbeServerInput{ServerID: 9, ActorUserID: 3, RequestID: "probe"})
		if !errors.Is(err, ErrMCPServerProbeFailed) || err.Error() != ErrMCPServerProbeFailed.Error() {
			t.Fatalf("ProbeServer error = %v", err)
		}
		if lister.calls != 1 || repo.replaced != 0 || len(repo.updates) != 0 {
			t.Fatalf("calls=%d replaced=%d updates=%d", lister.calls, repo.replaced, len(repo.updates))
		}
		assertTextOmits(t, err.Error(), secret, "admin@example.test", "token=")
	})

	t.Run("sync", func(t *testing.T) {
		t.Parallel()
		repo := &safeFailureMCPRepoStub{server: domainmcp.Server{ID: 9, BaseURL: "https://mcp.example.test/mcp", HeadersJSON: "{}"}}
		lister := &failingMCPToolLister{err: remoteErr}
		writer := &captureSystemEventWriter{}
		service := NewServiceWithRuntime(config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}), repo, lister)
		service.SetSystemEventWriter(writer)
		_, err := service.SyncServerTools(context.Background(), SyncServerToolsInput{ServerID: 9, RequestID: "sync"})
		if !errors.Is(err, ErrMCPServerSyncFailed) || err.Error() != ErrMCPServerSyncFailed.Error() {
			t.Fatalf("SyncServerTools error = %v", err)
		}
		if lister.calls != 1 || repo.replaced != 0 || len(repo.updates) != 1 || repo.updates[0].LastError == nil {
			t.Fatalf("calls=%d replaced=%d updates=%#v", lister.calls, repo.replaced, repo.updates)
		}
		wantSummary := inframcp.SafeErrorSummary(remoteErr)
		if *repo.updates[0].LastError != wantSummary {
			t.Fatalf("last error = %q, want %q", *repo.updates[0].LastError, wantSummary)
		}
		if len(writer.inputs) != 1 {
			t.Fatalf("system events = %#v", writer.inputs)
		}
		detail, marshalErr := json.Marshal(writer.inputs[0].Detail)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if !strings.Contains(string(detail), wantSummary) {
			t.Fatalf("system event detail = %s", detail)
		}
		assertTextOmits(t, err.Error()+string(detail)+*repo.updates[0].LastError, secret, "admin@example.test", "token=")
	})
}

type capturedAuditCall struct {
	requestID   string
	actorUserID uint
	action      string
	resource    string
	resourceID  string
	ip          string
	userAgent   string
	detail      interface{}
}

type captureAuditWriter struct {
	calls []capturedAuditCall
}

func (w *captureAuditWriter) Write(
	_ context.Context,
	requestID string,
	actorUserID uint,
	action string,
	resource string,
	resourceID string,
	ip string,
	userAgent string,
	detail interface{},
) {
	w.calls = append(w.calls, capturedAuditCall{
		requestID: requestID, actorUserID: actorUserID, action: action, resource: resource,
		resourceID: resourceID, ip: ip, userAgent: userAgent, detail: detail,
	})
}

func TestServiceRecordAuditUsesMCPServerResourceShape(t *testing.T) {
	t.Parallel()
	service := NewServiceWithRuntime(config.NewRuntime(config.Config{}), &mcpApplicationRepoStub{}, nil)
	writer := &captureAuditWriter{}
	service.SetAuditWriter(writer)
	detail := map[string]interface{}{"outcome": "success", "tool_count": 3}
	service.RecordAudit(context.Background(), AuditInput{
		UserID: 7, RequestID: " request-7 ", Action: " mcp.server.probe ", ResourceID: " 9 ",
		ClientIP: " 127.0.0.1 ", UserAgent: " test-agent ", Detail: detail,
	})
	if len(writer.calls) != 1 {
		t.Fatalf("audit calls = %#v", writer.calls)
	}
	want := capturedAuditCall{
		requestID: "request-7", actorUserID: 7, action: "mcp.server.probe", resource: "mcp_servers",
		resourceID: "9", ip: "127.0.0.1", userAgent: "test-agent", detail: detail,
	}
	if !reflect.DeepEqual(writer.calls[0], want) {
		t.Fatalf("audit call = %#v, want %#v", writer.calls[0], want)
	}
}

func assertTextOmits(t *testing.T, value string, forbidden ...string) {
	t.Helper()
	for _, item := range forbidden {
		if strings.Contains(value, item) {
			t.Fatalf("sensitive text %q leaked through %q", item, value)
		}
	}
}

func TestServiceMapsRepositoryNotFound(t *testing.T) {
	t.Parallel()
	status := "inactive"
	tests := []struct {
		name string
		repo *mcpRepositoryStub
		call func(*Service) error
	}{
		{
			name: "get",
			repo: &mcpRepositoryStub{getErr: repository.ErrNotFound},
			call: func(service *Service) error {
				_, err := service.GetServer(context.Background(), 404)
				return err
			},
		},
		{
			name: "update read",
			repo: &mcpRepositoryStub{getErr: repository.ErrNotFound},
			call: func(service *Service) error {
				_, err := service.UpdateServer(context.Background(), 404, UpdateServerInput{Status: &status})
				return err
			},
		},
		{
			name: "update write",
			repo: &mcpRepositoryStub{
				server:    testMCPServer(),
				updateErr: repository.ErrNotFound,
			},
			call: func(service *Service) error {
				_, err := service.UpdateServer(context.Background(), 404, UpdateServerInput{Status: &status})
				return err
			},
		},
		{
			name: "delete",
			repo: &mcpRepositoryStub{deleteErr: repository.ErrNotFound},
			call: func(service *Service) error {
				return service.DeleteServer(context.Background(), 404)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.call(newTestMCPService(tt.repo))
			if !errors.Is(err, ErrMCPServerNotFound) {
				t.Fatalf("expected ErrMCPServerNotFound, got %v", err)
			}
		})
	}
}

func TestCreateServerRejectsURLCredentialsQueryAndFragment(t *testing.T) {
	t.Parallel()
	for _, rawURL := range invalidPersistedMCPURLs() {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			repo := &mcpRepositoryStub{}
			_, err := newTestMCPService(repo).CreateServer(context.Background(), CreateServerInput{
				Name:        "Example",
				BaseURL:     rawURL,
				HeadersJSON: "{}",
				Status:      "active",
			})
			if !errors.Is(err, ErrInvalidServerBaseURL) {
				t.Fatalf("expected ErrInvalidServerBaseURL, got %v", err)
			}
			if repo.createCalls != 0 {
				t.Fatalf("invalid URL wrote %d servers", repo.createCalls)
			}
		})
	}
}

func TestUpdateServerRejectsURLCredentialsQueryAndFragment(t *testing.T) {
	t.Parallel()
	for _, rawURL := range invalidPersistedMCPURLs() {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			repo := &mcpRepositoryStub{server: testMCPServer()}
			_, err := newTestMCPService(repo).UpdateServer(context.Background(), repo.server.ID, UpdateServerInput{
				BaseURL: &rawURL,
			})
			if !errors.Is(err, ErrInvalidServerBaseURL) {
				t.Fatalf("expected ErrInvalidServerBaseURL, got %v", err)
			}
			if repo.updateCalls != 0 {
				t.Fatalf("invalid URL wrote %d updates", repo.updateCalls)
			}
		})
	}
}

func TestCreateServerRejectsRedactedAndDuplicateHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		headers string
	}{
		{name: "redacted sentinel", headers: `{"X-API-Key":"********"}`},
		{name: "duplicate key", headers: `{"X-Tenant":"one","X-Tenant":"two"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &mcpRepositoryStub{}
			_, err := newTestMCPService(repo).CreateServer(context.Background(), CreateServerInput{
				Name:        "Example",
				BaseURL:     "https://example.com/mcp",
				HeadersJSON: tt.headers,
				Status:      "active",
			})
			if !errors.Is(err, ErrInvalidServerHeaders) {
				t.Fatalf("expected ErrInvalidServerHeaders, got %v", err)
			}
			if repo.createCalls != 0 {
				t.Fatalf("invalid headers wrote %d servers", repo.createCalls)
			}
		})
	}
}

func TestUpdateServerBearerPatchSemantics(t *testing.T) {
	t.Parallel()
	t.Run("replace", func(t *testing.T) {
		t.Parallel()
		repo := &mcpRepositoryStub{server: testMCPServer()}
		token := " replacement-token "
		if _, err := newTestMCPService(repo).UpdateServer(context.Background(), repo.server.ID, UpdateServerInput{AuthToken: &token}); err != nil {
			t.Fatal(err)
		}
		if repo.updatedInput.AuthTokenEnc == nil {
			t.Fatal("expected explicit encrypted token update")
		}
		got, err := secretbox.DecryptString(testMCPEncryptionKey, *repo.updatedInput.AuthTokenEnc)
		if err != nil {
			t.Fatal(err)
		}
		if got != "replacement-token" {
			t.Fatalf("got decrypted token %q", got)
		}
	})

	t.Run("clear", func(t *testing.T) {
		t.Parallel()
		repo := &mcpRepositoryStub{server: testMCPServer()}
		if _, err := newTestMCPService(repo).UpdateServer(context.Background(), repo.server.ID, UpdateServerInput{ClearAuthToken: true}); err != nil {
			t.Fatal(err)
		}
		if repo.updatedInput.AuthTokenEnc == nil || *repo.updatedInput.AuthTokenEnc != "" {
			t.Fatalf("expected explicit empty ciphertext, got %#v", repo.updatedInput.AuthTokenEnc)
		}
	})

	t.Run("preserve", func(t *testing.T) {
		t.Parallel()
		repo := &mcpRepositoryStub{server: testMCPServer()}
		status := "inactive"
		if _, err := newTestMCPService(repo).UpdateServer(context.Background(), repo.server.ID, UpdateServerInput{Status: &status}); err != nil {
			t.Fatal(err)
		}
		if repo.updatedInput.AuthTokenEnc != nil {
			t.Fatalf("expected ciphertext preservation, got %#v", repo.updatedInput.AuthTokenEnc)
		}
	})
}

func TestUpdateServerRejectsInvalidBearerPatch(t *testing.T) {
	t.Parallel()
	empty := ""
	space := "   "
	token := "replacement"
	tests := []struct {
		name  string
		input UpdateServerInput
	}{
		{name: "empty token", input: UpdateServerInput{AuthToken: &empty}},
		{name: "whitespace token", input: UpdateServerInput{AuthToken: &space}},
		{name: "clear and token", input: UpdateServerInput{AuthToken: &token, ClearAuthToken: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &mcpRepositoryStub{server: testMCPServer()}
			_, err := newTestMCPService(repo).UpdateServer(context.Background(), repo.server.ID, tt.input)
			if !errors.Is(err, ErrInvalidAuthTokenUpdate) {
				t.Fatalf("expected ErrInvalidAuthTokenUpdate, got %v", err)
			}
			if repo.updateCalls != 0 {
				t.Fatalf("invalid auth update wrote %d updates", repo.updateCalls)
			}
		})
	}
}

func TestUpdateServerSQLitePreservesAndMergesHeaders(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlite db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err = db.AutoMigrate(&model.MCPServer{}, &model.MCPTool{}); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	stored := model.MCPServer{
		Name:         "Example",
		BaseURL:      "https://example.com/mcp",
		AuthTokenEnc: "existing-ciphertext",
		HeadersJSON:  `{"X-API-Key":"real-secret","X-Tenant":"old"}`,
		Status:       "active",
	}
	if err = db.Create(&stored).Error; err != nil {
		t.Fatalf("insert server: %v", err)
	}

	service := newTestMCPService(postgresmcp.NewRepo(db))
	status := "inactive"
	if _, err = service.UpdateServer(context.Background(), stored.ID, UpdateServerInput{Status: &status}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	var afterStatus model.MCPServer
	if err = db.First(&afterStatus, stored.ID).Error; err != nil {
		t.Fatalf("reload status update: %v", err)
	}
	if afterStatus.HeadersJSON != stored.HeadersJSON {
		t.Fatalf("status update changed headers: got %s want %s", afterStatus.HeadersJSON, stored.HeadersJSON)
	}
	if afterStatus.AuthTokenEnc != stored.AuthTokenEnc {
		t.Fatalf("status update changed ciphertext: got %q want %q", afterStatus.AuthTokenEnc, stored.AuthTokenEnc)
	}

	incoming := `{"X-API-Key":"********","X-Tenant":"new"}`
	if _, err = service.UpdateServer(context.Background(), stored.ID, UpdateServerInput{HeadersJSON: &incoming}); err != nil {
		t.Fatalf("merge headers: %v", err)
	}
	var afterHeaders model.MCPServer
	if err = db.First(&afterHeaders, stored.ID).Error; err != nil {
		t.Fatalf("reload header update: %v", err)
	}
	wantHeaders := `{"X-API-Key":"real-secret","X-Tenant":"new"}`
	if afterHeaders.HeadersJSON != wantHeaders {
		t.Fatalf("got headers %s want %s", afterHeaders.HeadersJSON, wantHeaders)
	}
}

func invalidPersistedMCPURLs() []string {
	return []string{
		"https://user:pass@example.com/mcp",
		"https://example.com/mcp?token=x",
		"https://example.com/mcp#fragment",
	}
}

func newTestMCPService(repo repository.MCPRepository) *Service {
	return NewServiceWithRuntime(config.NewRuntime(config.Config{
		Env:               "dev",
		DataEncryptionKey: testMCPEncryptionKey,
	}), repo, nil)
}

func testMCPServer() *domainmcp.Server {
	return &domainmcp.Server{
		ID:           7,
		Name:         "Example",
		BaseURL:      "https://example.com/mcp",
		AuthTokenEnc: "existing-ciphertext",
		HeadersJSON:  `{"X-API-Key":"real-secret","X-Tenant":"old"}`,
		Status:       "active",
	}
}

type mcpRepositoryStub struct {
	server       *domainmcp.Server
	getErr       error
	createErr    error
	updateErr    error
	deleteErr    error
	createCalls  int
	updateCalls  int
	createdInput repository.CreateMCPServerInput
	updatedInput repository.UpdateMCPServerInput
}

func (r *mcpRepositoryStub) CreateServer(_ context.Context, input repository.CreateMCPServerInput) (*domainmcp.Server, error) {
	r.createCalls++
	r.createdInput = input
	if r.createErr != nil {
		return nil, r.createErr
	}
	return &domainmcp.Server{
		ID:           1,
		Name:         input.Name,
		BaseURL:      input.BaseURL,
		AuthTokenEnc: input.AuthTokenEnc,
		HeadersJSON:  input.HeadersJSON,
		Status:       input.Status,
	}, nil
}

func (r *mcpRepositoryStub) UpdateServer(_ context.Context, _ uint, input repository.UpdateMCPServerInput) (*domainmcp.Server, error) {
	r.updateCalls++
	r.updatedInput = input
	if r.updateErr != nil {
		return nil, r.updateErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	if input.Name != nil {
		item.Name = *input.Name
	}
	if input.BaseURL != nil {
		item.BaseURL = *input.BaseURL
	}
	if input.AuthTokenEnc != nil {
		item.AuthTokenEnc = *input.AuthTokenEnc
	}
	if input.HeadersJSON != nil {
		item.HeadersJSON = *input.HeadersJSON
	}
	if input.Status != nil {
		item.Status = *input.Status
	}
	return &item, nil
}

func (r *mcpRepositoryStub) ListServers(context.Context) ([]domainmcp.Server, error) {
	return nil, nil
}

func (r *mcpRepositoryStub) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	return &item, nil
}

func (r *mcpRepositoryStub) DeleteServer(context.Context, uint) error {
	return r.deleteErr
}

func (r *mcpRepositoryStub) ReplaceServerTools(context.Context, uint, []domainmcp.Tool) error {
	return nil
}

func (r *mcpRepositoryStub) ListTools(context.Context, uint, bool) ([]domainmcp.Tool, error) {
	return nil, nil
}

func (r *mcpRepositoryStub) ListToolsByIDs(context.Context, []uint) ([]domainmcp.Tool, error) {
	return nil, nil
}

func (r *mcpRepositoryStub) UpdateTool(context.Context, uint, repository.UpdateMCPToolInput) (*domainmcp.Tool, error) {
	return nil, nil
}

func (r *mcpRepositoryStub) UpdateServerToolsStatus(context.Context, uint, []uint, string) ([]domainmcp.Tool, error) {
	return nil, nil
}

func (r *mcpRepositoryStub) ReorderServersWithTools(context.Context, []repository.ReorderMCPServerInput) ([]domainmcp.ServerWithTools, error) {
	return nil, nil
}
