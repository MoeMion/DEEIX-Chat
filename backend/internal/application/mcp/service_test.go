package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testMCPEncryptionKey = "test-mcp-data-encryption-key"

var (
	mcpTestSpanRecorder   *tracetest.SpanRecorder
	mcpTestTracerProvider *sdktrace.TracerProvider
)

func TestMain(testMain *testing.M) {
	mcpTestSpanRecorder = tracetest.NewSpanRecorder()
	mcpTestTracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(mcpTestSpanRecorder))
	otel.SetTracerProvider(mcpTestTracerProvider)

	exitCode := testMain.Run()
	if err := mcpTestTracerProvider.Shutdown(context.Background()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "shutdown MCP test tracer provider: %v\n", err)
		if exitCode == 0 {
			exitCode = 1
		}
	}
	os.Exit(exitCode)
}

type captureMCPSessionManager struct {
	calls []inframcp.CallConfig
}

type testMCPListOperation struct {
	list func(context.Context) ([]inframcp.Tool, error)
}

func (o *testMCPListOperation) ListTools(ctx context.Context) ([]inframcp.Tool, error) {
	return o.list(ctx)
}

func (*testMCPListOperation) CallTool(context.Context, inframcp.CallInput) (string, error) {
	return "", nil
}

func (*captureMCPSessionManager) Acquire(context.Context, inframcp.AcquireInput) (inframcp.Operation, error) {
	return nil, nil
}

func (c *captureMCPSessionManager) OpenEphemeral(_ context.Context, cfg inframcp.CallConfig, _ int) (inframcp.Operation, func(context.Context) error, error) {
	c.calls = append(c.calls, cfg)
	return &testMCPListOperation{list: func(context.Context) ([]inframcp.Tool, error) {
		return []inframcp.Tool{{Name: "memory.list", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
	}}, func(context.Context) error { return nil }, nil
}

func (*captureMCPSessionManager) CloseRun(context.Context, string, string) error { return nil }
func (*captureMCPSessionManager) CloseAll(context.Context) error                 { return nil }

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
		ID:             9,
		Name:           "Memory",
		BaseURL:        "https://mcp.example.test/mcp",
		HeadersJSON:    `{"X-Subject":"{{DEEIX_USER_PUBLIC_ID}}","X-Run":"{{DEEIX_RUN_ID}}","X-Request":"{{DEEIX_REQUEST_ID}}"}`,
		Status:         "active",
		ContextJWTMode: "none",
	}}
	lister := &captureMCPSessionManager{}
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
		BaseURL:        " https://mcp.example.test/mcp ",
		AuthTokenEnc:   encrypted,
		HeadersJSON:    `{"X-Subject":"{{DEEIX_USER_PUBLIC_ID}}","X-Warn":"{{UNKNOWN_TOKEN}}"}`,
		ContextJWTMode: "none",
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

func TestContextJWTBuildCallConfigDisabled(t *testing.T) {
	service := NewServiceWithRuntime(config.NewRuntime(config.Config{}), &mcpApplicationRepoStub{}, nil)
	templateContext := inframcp.TemplateContext{
		Mode:         inframcp.ContextModeChat,
		UserPublicID: "user-public",
		RequestID:    "request-public",
	}
	callConfig, _, err := service.BuildCallConfig(t.Context(), domainmcp.Server{
		PublicID:                 "mcp_disabled",
		BaseURL:                  "https://mcp.example.test/mcp",
		HeadersJSON:              `{}`,
		ContextJWTMode:           "none",
		ContextJWTSecretEnc:      "v1:ignored-corrupt-ciphertext",
		ContextJWTKeyID:          "",
		ContextJWTAudience:       "",
		ContextJWTExpiresSeconds: 0,
	}, templateContext, 4321)
	if err != nil {
		t.Fatalf("BuildCallConfig() error = %v", err)
	}
	if callConfig.SignedContext != nil {
		t.Fatal("SignedContext must be nil for mode none")
	}
	if callConfig.Context != templateContext {
		t.Fatalf("Context = %#v, want %#v", callConfig.Context, templateContext)
	}
}

func TestContextJWTBuildCallConfigConfigured(t *testing.T) {
	const dataKey = "context-jwt-build-data-key"
	secret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	ciphertext := encryptContextJWTBuildSecret(t, dataKey, secret)
	service := NewServiceWithRuntime(config.NewRuntime(config.Config{
		Env:               "dev",
		PublicWebBaseURL:  " https://chat.example.test/// ",
		DataEncryptionKey: dataKey,
	}), &mcpApplicationRepoStub{}, nil)
	templateContext := inframcp.TemplateContext{
		Mode:                     inframcp.ContextModeChat,
		UserPublicID:             "user-public",
		ConversationPublicID:     "conversation-public",
		AssistantMessagePublicID: "assistant-public",
		UserMessagePublicID:      "user-message-public",
		RequestID:                "request-public",
		RunID:                    "run-public",
	}
	server := contextJWTBuildServer(ciphertext)
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	t.Cleanup(func() {
		if err := tracerProvider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown tracer provider: %v", err)
		}
	})
	ctx, span := tracerProvider.Tracer("context-jwt-build-test").Start(t.Context(), "build-call-config")

	callConfig, _, err := service.BuildCallConfig(ctx, server, templateContext, 4321)
	span.End()
	if err != nil {
		t.Fatalf("BuildCallConfig() error = %v", err)
	}
	want := &inframcp.SignedContextConfig{
		Secret:         secret,
		Issuer:         "https://chat.example.test",
		Audience:       server.ContextJWTAudience,
		KeyID:          server.ContextJWTKeyID,
		ExpiresSeconds: server.ContextJWTExpiresSeconds,
		IncludeName:    true,
		IncludeEmail:   true,
		IncludeRole:    true,
	}
	if !reflect.DeepEqual(callConfig.SignedContext, want) {
		t.Fatal("SignedContext does not match configured policy")
	}
	if callConfig.Context != templateContext {
		t.Fatalf("Context = %#v, want %#v", callConfig.Context, templateContext)
	}
	ended := spanRecorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	var signedContext bool
	var keyID string
	for _, item := range ended[0].Attributes() {
		switch string(item.Key) {
		case "signed_context":
			signedContext = item.Value.AsBool()
		case "kid":
			keyID = item.Value.AsString()
		}
		if strings.Contains(item.Value.Emit(), secret) {
			t.Fatal("trace attribute leaked signed context secret")
		}
	}
	if !signedContext || keyID != server.ContextJWTKeyID {
		t.Fatalf("trace signed_context/kid = %v/%q", signedContext, keyID)
	}
}

func TestContextJWTBuildCallConfigRejectsInvalidStorage(t *testing.T) {
	const dataKey = "context-jwt-build-data-key"
	validSecret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	validCiphertext := encryptContextJWTBuildSecret(t, dataKey, validSecret)
	invalid31 := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 31))
	nonCanonical := contextJWTBuildNonCanonicalSecret(t, validSecret)
	tests := []struct {
		name   string
		mutate func(*testing.T, *domainmcp.Server, *config.Config)
	}{
		{
			name: "unknown mode",
			mutate: func(_ *testing.T, server *domainmcp.Server, _ *config.Config) {
				server.ContextJWTMode = "future"
			},
		},
		{
			name: "blank current ciphertext",
			mutate: func(_ *testing.T, server *domainmcp.Server, _ *config.Config) {
				server.ContextJWTSecretEnc = " \t"
			},
		},
		{
			name: "blank current key id",
			mutate: func(_ *testing.T, server *domainmcp.Server, _ *config.Config) {
				server.ContextJWTKeyID = " \t"
			},
		},
		{
			name: "blank audience",
			mutate: func(_ *testing.T, server *domainmcp.Server, _ *config.Config) {
				server.ContextJWTAudience = "\n"
			},
		},
		{
			name: "ttl below minimum",
			mutate: func(_ *testing.T, server *domainmcp.Server, _ *config.Config) {
				server.ContextJWTExpiresSeconds = 59
			},
		},
		{
			name: "ttl above maximum",
			mutate: func(_ *testing.T, server *domainmcp.Server, _ *config.Config) {
				server.ContextJWTExpiresSeconds = 901
			},
		},
		{
			name: "data encryption key mismatch",
			mutate: func(_ *testing.T, _ *domainmcp.Server, cfg *config.Config) {
				cfg.DataEncryptionKey = "mismatched-data-key"
			},
		},
		{
			name: "padded decrypted secret",
			mutate: func(t *testing.T, server *domainmcp.Server, _ *config.Config) {
				server.ContextJWTSecretEnc = encryptContextJWTBuildSecret(t, dataKey, validSecret+"=")
			},
		},
		{
			name: "malformed decrypted secret",
			mutate: func(t *testing.T, server *domainmcp.Server, _ *config.Config) {
				server.ContextJWTSecretEnc = encryptContextJWTBuildSecret(t, dataKey, "not/base64")
			},
		},
		{
			name: "31 byte decrypted secret",
			mutate: func(t *testing.T, server *domainmcp.Server, _ *config.Config) {
				server.ContextJWTSecretEnc = encryptContextJWTBuildSecret(t, dataKey, invalid31)
			},
		},
		{
			name: "non canonical decrypted secret",
			mutate: func(t *testing.T, server *domainmcp.Server, _ *config.Config) {
				server.ContextJWTSecretEnc = encryptContextJWTBuildSecret(t, dataKey, nonCanonical)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := contextJWTBuildServer(validCiphertext)
			runtimeConfig := config.Config{
				Env:               "dev",
				PublicWebBaseURL:  "https://chat.example.test",
				DataEncryptionKey: dataKey,
			}
			test.mutate(t, &server, &runtimeConfig)
			lister := &captureMCPSessionManager{}
			service := NewServiceWithRuntime(config.NewRuntime(runtimeConfig), &mcpApplicationRepoStub{}, lister)

			callConfig, _, err := service.BuildCallConfig(t.Context(), server, inframcp.TemplateContext{
				Mode:         inframcp.ContextModeChat,
				UserPublicID: "user-public",
			}, 1000)
			if err != ErrMCPContextJWTInvalidStorage {
				t.Fatalf("BuildCallConfig() error = %v, want exact ErrMCPContextJWTInvalidStorage", err)
			}
			if callConfig.SignedContext != nil {
				t.Fatal("SignedContext must be nil on invalid storage")
			}
			if len(lister.calls) != 0 {
				t.Fatalf("outbound lister calls = %d, want 0", len(lister.calls))
			}
		})
	}
}

func TestContextJWTBuildCallConfigRequiresIssuer(t *testing.T) {
	const dataKey = "context-jwt-build-data-key"
	secret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	ciphertext := encryptContextJWTBuildSecret(t, dataKey, secret)
	tests := []struct {
		name           string
		issuer         string
		env            string
		expiresSeconds int
	}{
		{name: "missing issuer", issuer: " \t", env: "dev"},
		{name: "missing issuer precedes invalid ttl", issuer: " \t", env: "dev", expiresSeconds: 59},
		{name: "invalid issuer", issuer: "issuer://invalid?marker", env: "dev"},
		{name: "insecure production issuer", issuer: "http://chat.example.test", env: "prod"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lister := &captureMCPSessionManager{}
			service := NewServiceWithRuntime(config.NewRuntime(config.Config{
				Env:               test.env,
				PublicWebBaseURL:  test.issuer,
				DataEncryptionKey: dataKey,
			}), &mcpApplicationRepoStub{}, lister)

			server := contextJWTBuildServer(ciphertext)
			if test.expiresSeconds != 0 {
				server.ContextJWTExpiresSeconds = test.expiresSeconds
			}
			callConfig, _, err := service.BuildCallConfig(t.Context(), server, inframcp.TemplateContext{
				Mode:         inframcp.ContextModeProbe,
				UserPublicID: "user-public",
			}, 1000)
			if err != ErrMCPContextJWTUnavailable {
				t.Fatalf("BuildCallConfig() error = %v, want exact ErrMCPContextJWTUnavailable", err)
			}
			if callConfig.SignedContext != nil {
				t.Fatal("SignedContext must be nil without valid issuer")
			}
			if len(lister.calls) != 0 {
				t.Fatalf("outbound lister calls = %d, want 0", len(lister.calls))
			}
		})
	}
}

func contextJWTBuildServer(ciphertext string) domainmcp.Server {
	return domainmcp.Server{
		ID:                       9,
		PublicID:                 "mcp_public",
		BaseURL:                  "https://mcp.example.test/mcp",
		HeadersJSON:              `{}`,
		ContextJWTMode:           "hs256",
		ContextJWTSecretEnc:      ciphertext,
		ContextJWTAudience:       "urn:deeix:mcp:mcp_public",
		ContextJWTKeyID:          "ctx_current",
		ContextJWTExpiresSeconds: 300,
		ContextJWTIncludeName:    true,
		ContextJWTIncludeEmail:   true,
		ContextJWTIncludeRole:    true,
	}
}

func encryptContextJWTBuildSecret(t *testing.T, dataKey string, secret string) string {
	t.Helper()
	ciphertext, err := secretbox.EncryptString(dataKey, secret)
	if err != nil {
		t.Fatalf("EncryptString() fixture error = %v", err)
	}
	return ciphertext
}

func contextJWTBuildNonCanonicalSecret(t *testing.T, canonical string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, canonical[len(canonical)-1])
	if last < 0 || last%4 != 0 {
		t.Fatalf("canonical fixture has unexpected terminal base64 index %d", last)
	}
	nonCanonical := canonical[:len(canonical)-1] + string(alphabet[last+1])
	decoded, err := base64.RawURLEncoding.DecodeString(nonCanonical)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) == nonCanonical {
		t.Fatalf("non-canonical fixture is invalid: len=%d err=%v", len(decoded), err)
	}
	return nonCanonical
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
			name:    "empty query target",
			server:  domainmcp.Server{BaseURL: "https://example.test/mcp?", HeadersJSON: "{}"},
			wantErr: ErrUnsafeMCPServerTarget,
		},
		{
			name:    "empty fragment target",
			server:  domainmcp.Server{BaseURL: "https://example.test/mcp#", HeadersJSON: "{}"},
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

type failingMCPSessionManager struct {
	err   error
	calls int
}

func (*failingMCPSessionManager) Acquire(context.Context, inframcp.AcquireInput) (inframcp.Operation, error) {
	return nil, nil
}

func (l *failingMCPSessionManager) OpenEphemeral(context.Context, inframcp.CallConfig, int) (inframcp.Operation, func(context.Context) error, error) {
	return &testMCPListOperation{list: func(context.Context) ([]inframcp.Tool, error) {
		l.calls++
		return nil, l.err
	}}, func(context.Context) error { return nil }, nil
}

func (*failingMCPSessionManager) CloseRun(context.Context, string, string) error { return nil }
func (*failingMCPSessionManager) CloseAll(context.Context) error                 { return nil }

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
		repo := &safeFailureMCPRepoStub{server: domainmcp.Server{
			ID: 9, BaseURL: "https://mcp.example.test/mcp", HeadersJSON: "{}", ContextJWTMode: "none",
		}}
		lister := &failingMCPSessionManager{err: remoteErr}
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
		repo := &safeFailureMCPRepoStub{server: domainmcp.Server{
			ID: 9, BaseURL: "https://mcp.example.test/mcp", HeadersJSON: "{}", ContextJWTMode: "none",
		}}
		lister := &failingMCPSessionManager{err: remoteErr}
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

func TestServiceRealTransportSecretsStayOutOfErrorsTracesEventsAndLastError(t *testing.T) {
	const (
		querySecret   = "endpoint-query-secret"
		jsonRPCSecret = "json-rpc-message-secret"
		sseDataSecret = "sse-data-secret"
	)
	forbidden := []string{querySecret, jsonRPCSecret, sseDataSecret, "token="}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			t.Errorf("decode MCP request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch envelope.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-11-25"}}`, envelope.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			switch request.URL.Path {
			case "/json":
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":%q}}`, envelope.ID, jsonRPCSecret)
			case "/sse":
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\n", sseDataSecret)
			default:
				http.Error(w, "unexpected path", http.StatusNotFound)
			}
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	ctx, rootSpan := mcpTestTracerProvider.Tracer("mcp-secret-sinks-test").Start(t.Context(), "mcp-secret-sinks")
	traceID := rootSpan.SpanContext().TraceID()

	client := inframcp.NewClient()
	_, queryErr := client.ListTools(ctx, inframcp.CallConfig{
		BaseURL: server.URL + "/query?token=" + querySecret,
	})
	var queryClientErr *inframcp.ClientError
	if !errors.As(queryErr, &queryClientErr) || queryClientErr.Kind != inframcp.ClientErrorProtocol {
		t.Fatalf("query error = %#v", queryErr)
	}

	_, jsonErr := client.ListTools(ctx, inframcp.CallConfig{BaseURL: server.URL + "/json"})
	var jsonClientErr *inframcp.ClientError
	if !errors.As(jsonErr, &jsonClientErr) || jsonClientErr.Kind != inframcp.ClientErrorJSONRPC {
		t.Fatalf("JSON-RPC error = %#v", jsonErr)
	}

	_, sseErr := client.ListTools(ctx, inframcp.CallConfig{BaseURL: server.URL + "/sse"})
	var sseRequestErr *inframcp.RequestError
	if !errors.As(sseErr, &sseRequestErr) || sseRequestErr.Class != inframcp.ClientErrorProtocol {
		t.Fatalf("SSE error = %#v", sseErr)
	}

	transportErrors := []struct {
		name string
		err  error
	}{
		{name: "query", err: queryErr},
		{name: "json rpc", err: jsonErr},
		{name: "sse data", err: sseErr},
	}
	for _, test := range transportErrors {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil {
				t.Fatal("expected real transport error")
			}
			assertTextOmits(t, test.err.Error()+inframcp.SafeErrorSummary(test.err), forbidden...)

			repo := &safeFailureMCPRepoStub{server: domainmcp.Server{
				ID: 9, BaseURL: "https://mcp.example.test/mcp", HeadersJSON: "{}", ContextJWTMode: "none",
			}}
			lister := &failingMCPSessionManager{err: test.err}
			writer := &captureSystemEventWriter{}
			service := NewServiceWithRuntime(
				config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}),
				repo,
				lister,
			)
			service.SetSystemEventWriter(writer)
			_, err := service.SyncServerTools(ctx, SyncServerToolsInput{ServerID: 9, RequestID: "sync-real-transport"})
			if !errors.Is(err, ErrMCPServerSyncFailed) || err.Error() != ErrMCPServerSyncFailed.Error() {
				t.Fatalf("SyncServerTools error = %v", err)
			}
			if lister.calls != 1 || len(repo.updates) != 1 || repo.updates[0].LastError == nil || len(writer.inputs) != 1 {
				t.Fatalf("calls=%d updates=%#v events=%#v", lister.calls, repo.updates, writer.inputs)
			}
			detail, marshalErr := json.Marshal(writer.inputs[0].Detail)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			sinkText := err.Error() + string(detail) + *repo.updates[0].LastError
			assertTextOmits(t, sinkText, forbidden...)
		})
	}
	rootSpan.End()

	ended := make([]sdktrace.ReadOnlySpan, 0, 7)
	for _, span := range mcpTestSpanRecorder.Ended() {
		if span.SpanContext().TraceID() == traceID {
			ended = append(ended, span)
		}
	}
	if len(ended) != 7 {
		t.Fatalf("trace-local ended spans = %d, want root plus six real outbound spans", len(ended))
	}
	var traceText strings.Builder
	for _, span := range ended {
		_, _ = fmt.Fprintf(&traceText, "name=%s status=%s attrs=%v", span.Name(), span.Status().Description, span.Attributes())
		for _, event := range span.Events() {
			_, _ = fmt.Fprintf(&traceText, " event=%s attrs=%v", event.Name, event.Attributes)
		}
		for _, link := range span.Links() {
			_, _ = fmt.Fprintf(&traceText, " link_attrs=%v", link.Attributes)
		}
	}
	assertTextOmits(t, traceText.String(), forbidden...)
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

func TestServiceURLValidationAllowsPercentEncodedHashPath(t *testing.T) {
	t.Parallel()
	const baseURL = "https://example.test/mcp%23tenant"

	createRepo := &mcpRepositoryStub{}
	created, err := newTestMCPService(createRepo).CreateServer(context.Background(), CreateServerInput{
		Name: "Example", BaseURL: baseURL, HeadersJSON: "{}", Status: "active",
	})
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	if createRepo.createCalls != 1 || created.BaseURL != baseURL {
		t.Fatalf("create writes=%d server=%#v", createRepo.createCalls, created)
	}

	updateRepo := &mcpRepositoryStub{server: testMCPServer()}
	updated, err := newTestMCPService(updateRepo).UpdateServer(context.Background(), updateRepo.server.ID, UpdateServerInput{BaseURL: ptrString(baseURL)})
	if err != nil {
		t.Fatalf("UpdateServer: %v", err)
	}
	if updateRepo.updateCalls != 1 || updated.BaseURL != baseURL {
		t.Fatalf("update writes=%d server=%#v", updateRepo.updateCalls, updated)
	}

	service := NewServiceWithRuntime(config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}), &mcpApplicationRepoStub{}, nil)
	callConfig, _, err := service.BuildCallConfig(context.Background(), domainmcp.Server{
		BaseURL: baseURL, HeadersJSON: "{}", ContextJWTMode: "none",
	}, inframcp.TemplateContext{}, 1000)
	if err != nil {
		t.Fatalf("BuildCallConfig: %v", err)
	}
	if callConfig.BaseURL != baseURL {
		t.Fatalf("BuildCallConfig BaseURL = %q", callConfig.BaseURL)
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

func TestCreateServerGeneratesSignedContextIdentity(t *testing.T) {
	t.Parallel()
	repo := &mcpRepositoryStub{}
	created, err := newTestMCPService(repo).CreateServer(context.Background(), CreateServerInput{
		Name: "Example", BaseURL: "https://example.test/mcp", HeadersJSON: "{}", Status: "active",
	})
	if err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	publicID := repo.createdInput.PublicID
	if !strings.HasPrefix(publicID, "mcp_") || len(publicID) != 36 || strings.Contains(publicID, "-") {
		t.Fatalf("generated public id = %q", publicID)
	}
	wantAudience := "urn:deeix:mcp:" + publicID
	if repo.createdInput.ContextJWTAudience != wantAudience {
		t.Fatalf("repository audience = %q, want %q", repo.createdInput.ContextJWTAudience, wantAudience)
	}
	if created.PublicID != publicID || created.ContextJWTAudience != wantAudience {
		t.Fatalf("created server identity = %#v", created)
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
		"https://example.test/mcp?",
		"https://example.test/mcp#",
	}
}

func ptrString(value string) *string {
	return &value
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
		ID:                 1,
		PublicID:           input.PublicID,
		Name:               input.Name,
		BaseURL:            input.BaseURL,
		AuthTokenEnc:       input.AuthTokenEnc,
		HeadersJSON:        input.HeadersJSON,
		Status:             input.Status,
		ContextJWTAudience: input.ContextJWTAudience,
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
