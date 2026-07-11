package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/mcp"
	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/response"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/transport/http/middleware"
	"github.com/gin-gonic/gin"
)

const contextJWTHandlerDataKey = "test-context-jwt-handler-data-key"

type contextJWTHandlerContextKey struct{}

type contextJWTHandlerRepositoryStub struct {
	repository.MCPRepository

	server  *domainmcp.Server
	servers []domainmcp.Server
	tools   map[uint][]domainmcp.Tool

	operations       []string
	requestContexts  []bool
	createInput      repository.CreateMCPServerInput
	updateInput      repository.UpdateMCPServerInput
	policyServerID   uint
	policyInput      repository.UpdateMCPContextJWTPolicyInput
	prepareInput     repository.PrepareMCPContextJWTRotationInput
	activateServerID uint
	activateKeyID    string
	cancelServerID   uint
	cancelKeyID      string
	disableServerID  uint
	clearCalls       int

	listErr     error
	createErr   error
	updateErr   error
	getErr      error
	policyErr   error
	prepareErr  error
	activateErr error
	cancelErr   error
	disableErr  error
	reorderErr  error
	clearErr    error
}

func newContextJWTHandlerService() *appmcp.Service {
	return appmcp.NewServiceWithRuntime(
		config.NewRuntime(config.Config{
			Env:               "dev",
			PublicWebBaseURL:  "https://chat.example.test/",
			DataEncryptionKey: contextJWTHandlerDataKey,
		}),
		&contextJWTHandlerRepositoryStub{},
		nil,
	)
}

func newContextJWTHandlerServer(t *testing.T) (*domainmcp.Server, []string) {
	t.Helper()
	currentSecret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32))
	pendingSecret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x32}, 32))
	currentCiphertext := "v1:ERERERERERERERERJBrqQTNymsWfEN/7SxQ0pUpF2qU+QJCgTSdI80lwz5V/huMxhB3Mzmkc+VWB/M7zkg3kxLBKIn4I1nI="
	pendingCiphertext := "v1:IiIiIiIiIiIiIiIirvNplCzyqP9KgkX1nutnWmqg5QopkvHRS3Ydi3CpNp6S6c7HSFTIZvW9Q5WVoYUwFTbPqZTD/KIbdhg="
	createdAt := time.Now().Add(-time.Hour)
	expiresAt := time.Now().Add(time.Hour)
	server := &domainmcp.Server{
		ID:                         7,
		PublicID:                   "mcp_context_public",
		Name:                       "owner@example.test",
		BaseURL:                    "https://mcp.example.test/mcp",
		AuthTokenEnc:               "v1:auth-token-ciphertext-leak-marker",
		HeadersJSON:                `{"X-API-Key":"header-secret-leak-marker"}`,
		Status:                     "active",
		ContextJWTMode:             "hs256",
		ContextJWTSecretEnc:        currentCiphertext,
		ContextJWTAudience:         "urn:deeix:mcp:mcp_context_public",
		ContextJWTKeyID:            "ctx_current_safe",
		ContextJWTExpiresSeconds:   300,
		ContextJWTIncludeName:      true,
		ContextJWTIncludeEmail:     true,
		ContextJWTIncludeRole:      true,
		ContextJWTPendingSecretEnc: pendingCiphertext,
		ContextJWTPendingKeyID:     "ctx_pending_safe",
		ContextJWTPendingCreatedAt: &createdAt,
		ContextJWTPendingExpiresAt: &expiresAt,
	}
	return server, []string{
		currentSecret,
		pendingSecret,
		currentCiphertext,
		pendingCiphertext,
		"v1:auth-token-ciphertext-leak-marker",
		"header-secret-leak-marker",
	}
}

func cloneContextJWTHandlerServer(server *domainmcp.Server) *domainmcp.Server {
	if server == nil {
		return nil
	}
	item := *server
	if server.ContextJWTPendingCreatedAt != nil {
		value := *server.ContextJWTPendingCreatedAt
		item.ContextJWTPendingCreatedAt = &value
	}
	if server.ContextJWTPendingExpiresAt != nil {
		value := *server.ContextJWTPendingExpiresAt
		item.ContextJWTPendingExpiresAt = &value
	}
	return &item
}

func (r *contextJWTHandlerRepositoryStub) record(ctx context.Context, operation string) {
	r.operations = append(r.operations, operation)
	r.requestContexts = append(r.requestContexts, ctx.Value(contextJWTHandlerContextKey{}) == "request-context-value")
}

func (r *contextJWTHandlerRepositoryStub) CreateServer(
	ctx context.Context,
	input repository.CreateMCPServerInput,
) (*domainmcp.Server, error) {
	r.record(ctx, "create")
	r.createInput = input
	if r.createErr != nil {
		return nil, r.createErr
	}
	r.server = &domainmcp.Server{
		ID:                       7,
		PublicID:                 input.PublicID,
		Name:                     input.Name,
		BaseURL:                  input.BaseURL,
		AuthTokenEnc:             input.AuthTokenEnc,
		HeadersJSON:              input.HeadersJSON,
		Status:                   input.Status,
		ContextJWTMode:           "none",
		ContextJWTAudience:       input.ContextJWTAudience,
		ContextJWTExpiresSeconds: 300,
	}
	return cloneContextJWTHandlerServer(r.server), nil
}

func (r *contextJWTHandlerRepositoryStub) UpdateServer(
	ctx context.Context,
	_ uint,
	input repository.UpdateMCPServerInput,
) (*domainmcp.Server, error) {
	r.record(ctx, "update")
	r.updateInput = input
	if r.updateErr != nil {
		return nil, r.updateErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	if input.Name != nil {
		r.server.Name = *input.Name
	}
	if input.BaseURL != nil {
		r.server.BaseURL = *input.BaseURL
	}
	if input.AuthTokenEnc != nil {
		r.server.AuthTokenEnc = *input.AuthTokenEnc
	}
	if input.HeadersJSON != nil {
		r.server.HeadersJSON = *input.HeadersJSON
	}
	if input.Status != nil {
		r.server.Status = *input.Status
	}
	return cloneContextJWTHandlerServer(r.server), nil
}

func (r *contextJWTHandlerRepositoryStub) ListServers(ctx context.Context) ([]domainmcp.Server, error) {
	r.record(ctx, "list")
	if r.listErr != nil {
		return nil, r.listErr
	}
	items := r.servers
	if len(items) == 0 && r.server != nil {
		items = []domainmcp.Server{*cloneContextJWTHandlerServer(r.server)}
	}
	result := make([]domainmcp.Server, len(items))
	copy(result, items)
	return result, nil
}

func (r *contextJWTHandlerRepositoryStub) GetServer(ctx context.Context, _ uint) (*domainmcp.Server, error) {
	r.record(ctx, "get")
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	return cloneContextJWTHandlerServer(r.server), nil
}

func (r *contextJWTHandlerRepositoryStub) DeleteServer(ctx context.Context, _ uint) error {
	r.record(ctx, "delete")
	return nil
}

func (r *contextJWTHandlerRepositoryStub) ReplaceServerTools(
	ctx context.Context,
	_ uint,
	_ []domainmcp.Tool,
) error {
	r.record(ctx, "replace tools")
	return nil
}

func (r *contextJWTHandlerRepositoryStub) ListTools(
	ctx context.Context,
	serverID uint,
	_ bool,
) ([]domainmcp.Tool, error) {
	r.record(ctx, "list tools")
	items := r.tools[serverID]
	result := make([]domainmcp.Tool, len(items))
	copy(result, items)
	return result, nil
}

func (r *contextJWTHandlerRepositoryStub) ListToolsByIDs(
	ctx context.Context,
	_ []uint,
) ([]domainmcp.Tool, error) {
	r.record(ctx, "list tools by ids")
	return []domainmcp.Tool{}, nil
}

func (r *contextJWTHandlerRepositoryStub) UpdateTool(
	ctx context.Context,
	_ uint,
	_ repository.UpdateMCPToolInput,
) (*domainmcp.Tool, error) {
	r.record(ctx, "update tool")
	return &domainmcp.Tool{}, nil
}

func (r *contextJWTHandlerRepositoryStub) UpdateServerToolsStatus(
	ctx context.Context,
	_ uint,
	_ []uint,
	_ string,
) ([]domainmcp.Tool, error) {
	r.record(ctx, "update tool status")
	return []domainmcp.Tool{}, nil
}

func (r *contextJWTHandlerRepositoryStub) ReorderServersWithTools(
	ctx context.Context,
	_ []repository.ReorderMCPServerInput,
) ([]domainmcp.ServerWithTools, error) {
	r.record(ctx, "reorder")
	if r.reorderErr != nil {
		return nil, r.reorderErr
	}
	servers := r.servers
	if len(servers) == 0 && r.server != nil {
		servers = []domainmcp.Server{*cloneContextJWTHandlerServer(r.server)}
	}
	result := make([]domainmcp.ServerWithTools, 0, len(servers))
	for _, server := range servers {
		result = append(result, domainmcp.ServerWithTools{
			Server: server,
			Tools:  append([]domainmcp.Tool(nil), r.tools[server.ID]...),
		})
	}
	return result, nil
}

func (r *contextJWTHandlerRepositoryStub) UpdateContextJWTPolicy(
	ctx context.Context,
	serverID uint,
	input repository.UpdateMCPContextJWTPolicyInput,
) (*domainmcp.Server, error) {
	r.record(ctx, "policy")
	r.policyServerID = serverID
	r.policyInput = input
	if r.policyErr != nil {
		return nil, r.policyErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	r.server.ContextJWTExpiresSeconds = input.ExpiresSeconds
	r.server.ContextJWTIncludeName = input.IncludeName
	r.server.ContextJWTIncludeEmail = input.IncludeEmail
	r.server.ContextJWTIncludeRole = input.IncludeRole
	return cloneContextJWTHandlerServer(r.server), nil
}

func (r *contextJWTHandlerRepositoryStub) PrepareContextJWTRotation(
	ctx context.Context,
	input repository.PrepareMCPContextJWTRotationInput,
) (*domainmcp.Server, error) {
	r.record(ctx, "prepare")
	r.prepareInput = input
	if r.prepareErr != nil {
		return nil, r.prepareErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	r.server.ContextJWTPendingSecretEnc = input.PendingSecretEnc
	r.server.ContextJWTPendingKeyID = input.PendingKeyID
	createdAt := input.CreatedAt
	expiresAt := input.ExpiresAt
	r.server.ContextJWTPendingCreatedAt = &createdAt
	r.server.ContextJWTPendingExpiresAt = &expiresAt
	return cloneContextJWTHandlerServer(r.server), nil
}

func (r *contextJWTHandlerRepositoryStub) ActivateContextJWTRotation(
	ctx context.Context,
	serverID uint,
	kid string,
	_ time.Time,
) (*domainmcp.Server, error) {
	r.record(ctx, "activate")
	r.activateServerID = serverID
	r.activateKeyID = kid
	if r.activateErr != nil {
		return nil, r.activateErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	r.server.ContextJWTMode = "hs256"
	r.server.ContextJWTSecretEnc = r.server.ContextJWTPendingSecretEnc
	r.server.ContextJWTKeyID = r.server.ContextJWTPendingKeyID
	clearContextJWTHandlerPending(r.server)
	return cloneContextJWTHandlerServer(r.server), nil
}

func (r *contextJWTHandlerRepositoryStub) CancelContextJWTRotation(
	ctx context.Context,
	serverID uint,
	kid string,
) (*domainmcp.Server, error) {
	r.record(ctx, "cancel")
	r.cancelServerID = serverID
	r.cancelKeyID = kid
	if r.cancelErr != nil {
		return nil, r.cancelErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	clearContextJWTHandlerPending(r.server)
	return cloneContextJWTHandlerServer(r.server), nil
}

func (r *contextJWTHandlerRepositoryStub) DisableContextJWT(
	ctx context.Context,
	serverID uint,
) (*domainmcp.Server, error) {
	r.record(ctx, "disable")
	r.disableServerID = serverID
	if r.disableErr != nil {
		return nil, r.disableErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	r.server.ContextJWTMode = "none"
	r.server.ContextJWTSecretEnc = ""
	r.server.ContextJWTKeyID = ""
	r.server.ContextJWTIncludeName = false
	r.server.ContextJWTIncludeEmail = false
	r.server.ContextJWTIncludeRole = false
	clearContextJWTHandlerPending(r.server)
	return cloneContextJWTHandlerServer(r.server), nil
}

func (r *contextJWTHandlerRepositoryStub) ClearExpiredContextJWTPending(
	ctx context.Context,
	now time.Time,
) error {
	r.record(ctx, "clear")
	r.clearCalls++
	if r.clearErr != nil {
		return r.clearErr
	}
	if r.server != nil && r.server.ContextJWTPendingExpiresAt != nil &&
		!r.server.ContextJWTPendingExpiresAt.After(now) {
		clearContextJWTHandlerPending(r.server)
	}
	for index := range r.servers {
		if r.servers[index].ContextJWTPendingExpiresAt != nil &&
			!r.servers[index].ContextJWTPendingExpiresAt.After(now) {
			clearContextJWTHandlerPending(&r.servers[index])
		}
	}
	return nil
}

func clearContextJWTHandlerPending(server *domainmcp.Server) {
	server.ContextJWTPendingSecretEnc = ""
	server.ContextJWTPendingKeyID = ""
	server.ContextJWTPendingCreatedAt = nil
	server.ContextJWTPendingExpiresAt = nil
}

func newContextJWTHandlerRouter(
	repo *contextJWTHandlerRepositoryStub,
	audit *probeAuditCapture,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	service := appmcp.NewServiceWithRuntime(
		config.NewRuntime(config.Config{
			Env:               "dev",
			PublicWebBaseURL:  "https://chat.example.test/",
			DataEncryptionKey: contextJWTHandlerDataKey,
		}),
		repo,
		nil,
	)
	if audit != nil {
		service.SetAuditWriter(audit)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, uint(42))
		c.Set(middleware.ContextKeyRequestID, "request-context-jwt")
		c.Next()
	})
	NewModule(NewHandler(service)).RegisterAdminRoutes(router.Group("/api/v1/admin"))
	return router
}

func serveContextJWTHandlerRequest(
	router *gin.Engine,
	method string,
	path string,
	body string,
) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request = request.WithContext(context.WithValue(
		request.Context(),
		contextJWTHandlerContextKey{},
		"request-context-value",
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "context-jwt-test-agent")
	request.RemoteAddr = "192.0.2.42:4242"
	router.ServeHTTP(recorder, request)
	return recorder
}

func decodeContextJWTData(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestContextJWTServerResponses(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		body      string
		shape     string
		configure func(*contextJWTHandlerRepositoryStub, *domainmcp.Server)
	}{
		{
			name: "list", method: http.MethodGet, path: "/api/v1/admin/mcp/servers", shape: "list",
			configure: func(repo *contextJWTHandlerRepositoryStub, server *domainmcp.Server) {
				repo.servers = []domainmcp.Server{*server}
			},
		},
		{
			name: "create", method: http.MethodPost, path: "/api/v1/admin/mcp/servers", shape: "server",
			body: `{"name":"Memory","baseURL":"https://mcp.example.test/mcp","headersJSON":"{}","status":"active"}`,
			configure: func(repo *contextJWTHandlerRepositoryStub, _ *domainmcp.Server) {
				repo.server = nil
			},
		},
		{
			name: "update", method: http.MethodPatch, path: "/api/v1/admin/mcp/servers/7", shape: "server",
			body:      `{"status":"inactive"}`,
			configure: func(*contextJWTHandlerRepositoryStub, *domainmcp.Server) {},
		},
		{
			name: "reorder", method: http.MethodPatch, path: "/api/v1/admin/mcp/servers/order", shape: "reorder",
			body: `{"servers":[{"serverID":7,"toolIDs":[]}]}`,
			configure: func(repo *contextJWTHandlerRepositoryStub, server *domainmcp.Server) {
				repo.servers = []domainmcp.Server{*server}
				repo.tools = map[uint][]domainmcp.Tool{7: {}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, leaks := newContextJWTHandlerServer(t)
			repo := &contextJWTHandlerRepositoryStub{server: server}
			test.configure(repo, server)
			router := newContextJWTHandlerRouter(repo, nil)
			recorder := serveContextJWTHandlerRequest(router, test.method, test.path, test.body)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
			}
			serverData := decodeContextJWTServerPayload(t, recorder.Body.Bytes(), test.shape)
			publicID, ok := serverData["publicID"].(string)
			if !ok || !strings.HasPrefix(publicID, "mcp_") {
				t.Fatalf("publicID = %#v; server=%#v", serverData["publicID"], serverData)
			}
			status, ok := serverData["contextJWT"].(map[string]interface{})
			if !ok || status["mode"] == nil || status["configured"] == nil || status["issuer"] == nil {
				t.Fatalf("contextJWT = %#v; server=%#v", serverData["contextJWT"], serverData)
			}
			if test.name != "create" && publicID != "mcp_context_public" {
				t.Fatalf("publicID = %q, want mcp_context_public", publicID)
			}
			for _, leak := range leaks {
				if strings.Contains(recorder.Body.String(), leak) {
					t.Fatalf("sensitive fixture %q leaked: %s", leak, recorder.Body.String())
				}
			}
			lowerBody := strings.ToLower(recorder.Body.String())
			for _, field := range []string{"authtokenenc", "contextjwtsecretenc", "contextjwtpendingsecretenc", "contextjwtpendingcreatedat"} {
				if strings.Contains(lowerBody, field) {
					t.Fatalf("secret field %q leaked: %s", field, recorder.Body.String())
				}
			}
			if test.name == "list" && repo.clearCalls != 1 {
				t.Fatalf("list cleanup calls = %d, want 1", repo.clearCalls)
			}
		})
	}
}

func decodeContextJWTServerPayload(t *testing.T, body []byte, shape string) map[string]interface{} {
	t.Helper()
	var envelope map[string]interface{}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	data, ok := envelope["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data is not an object: %s", body)
	}
	switch shape {
	case "server":
		server, ok := data["server"].(map[string]interface{})
		if !ok {
			t.Fatalf("server payload missing: %s", body)
		}
		return server
	case "list":
		results, ok := data["results"].([]interface{})
		if !ok || len(results) != 1 {
			t.Fatalf("list results = %#v", data["results"])
		}
		server, ok := results[0].(map[string]interface{})
		if !ok {
			t.Fatalf("list server = %#v", results[0])
		}
		return server
	case "reorder":
		results, ok := data["results"].([]interface{})
		if !ok || len(results) != 1 {
			t.Fatalf("reorder results = %#v", data["results"])
		}
		row, ok := results[0].(map[string]interface{})
		if !ok {
			t.Fatalf("reorder row = %#v", results[0])
		}
		server, ok := row["server"].(map[string]interface{})
		if !ok {
			t.Fatalf("reorder server = %#v", row["server"])
		}
		return server
	default:
		t.Fatalf("unknown response shape %q", shape)
		return nil
	}
}

func TestContextJWTRoutes(t *testing.T) {
	routeRepo := &contextJWTHandlerRepositoryStub{}
	router := newContextJWTHandlerRouter(routeRepo, nil)
	routes := make(map[string]struct{})
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}
	for _, want := range []string{
		"PATCH /api/v1/admin/mcp/servers/:id/context-jwt",
		"POST /api/v1/admin/mcp/servers/:id/context-jwt/rotations",
		"POST /api/v1/admin/mcp/servers/:id/context-jwt/rotations/:kid/activate",
		"DELETE /api/v1/admin/mcp/servers/:id/context-jwt/rotations/:kid",
		"DELETE /api/v1/admin/mcp/servers/:id/context-jwt",
	} {
		if _, ok := routes[want]; !ok {
			t.Fatalf("missing route %q; routes=%v", want, routes)
		}
	}

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		assert func(*testing.T, *contextJWTHandlerRepositoryStub)
	}{
		{
			name:   "update policy",
			method: http.MethodPatch,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt",
			body:   `{"expiresSeconds":600,"includeName":true,"includeEmail":false,"includeRole":true}`,
			assert: func(t *testing.T, repo *contextJWTHandlerRepositoryStub) {
				t.Helper()
				want := repository.UpdateMCPContextJWTPolicyInput{
					ExpiresSeconds: 600,
					IncludeName:    true,
					IncludeEmail:   false,
					IncludeRole:    true,
				}
				if repo.policyServerID != 7 || repo.policyInput != want {
					t.Fatalf("policy call = %d/%#v, want 7/%#v", repo.policyServerID, repo.policyInput, want)
				}
			},
		},
		{
			name:   "prepare rotation",
			method: http.MethodPost,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt/rotations",
			assert: func(t *testing.T, repo *contextJWTHandlerRepositoryStub) {
				t.Helper()
				if repo.prepareInput.ServerID != 7 || strings.TrimSpace(repo.prepareInput.PendingKeyID) == "" {
					t.Fatalf("prepare input = %#v", repo.prepareInput)
				}
			},
		},
		{
			name:   "activate rotation",
			method: http.MethodPost,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt/rotations/%20ctx_pending_safe%20/activate",
			assert: func(t *testing.T, repo *contextJWTHandlerRepositoryStub) {
				t.Helper()
				if repo.activateServerID != 7 || repo.activateKeyID != "ctx_pending_safe" {
					t.Fatalf("activate call = %d/%q", repo.activateServerID, repo.activateKeyID)
				}
			},
		},
		{
			name:   "cancel rotation",
			method: http.MethodDelete,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt/rotations/%20ctx_pending_safe%20",
			assert: func(t *testing.T, repo *contextJWTHandlerRepositoryStub) {
				t.Helper()
				if repo.cancelServerID != 7 || repo.cancelKeyID != "ctx_pending_safe" {
					t.Fatalf("cancel call = %d/%q", repo.cancelServerID, repo.cancelKeyID)
				}
			},
		},
		{
			name:   "disable",
			method: http.MethodDelete,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt",
			assert: func(t *testing.T, repo *contextJWTHandlerRepositoryStub) {
				t.Helper()
				if repo.disableServerID != 7 {
					t.Fatalf("disable call id = %d, want 7", repo.disableServerID)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newContextJWTHandlerServer(t)
			repo := &contextJWTHandlerRepositoryStub{server: server}
			router := newContextJWTHandlerRouter(repo, nil)
			recorder := serveContextJWTHandlerRequest(router, test.method, test.path, test.body)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
			}
			test.assert(t, repo)
			for index, ok := range repo.requestContexts {
				if !ok {
					t.Fatalf("repository call %d did not receive request context; operations=%v", index, repo.operations)
				}
			}
			if test.name != "prepare rotation" {
				data := decodeContextJWTData(t, recorder.Body.Bytes())
				if _, ok := data["mode"]; !ok {
					t.Fatalf("status response was not direct envelope data: %s", recorder.Body.String())
				}
				if _, wrapped := data["contextJWT"]; wrapped {
					t.Fatalf("status response was unexpectedly wrapped: %s", recorder.Body.String())
				}
			}
		})
	}
}

func TestContextJWTPrepareNoStore(t *testing.T) {
	server, fixtureLeaks := newContextJWTHandlerServer(t)
	repo := &contextJWTHandlerRepositoryStub{server: server}
	audit := &probeAuditCapture{}
	router := newContextJWTHandlerRouter(repo, audit)
	recorder := serveContextJWTHandlerRequest(
		router,
		http.MethodPost,
		"/api/v1/admin/mcp/servers/7/context-jwt/rotations",
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %q/%q", recorder.Header().Get("Cache-Control"), recorder.Header().Get("Pragma"))
	}
	var envelope struct {
		Data PrepareContextJWTRotationResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Header != "X-DEEIX-Context" || envelope.Data.Algorithm != "HS256" ||
		envelope.Data.Secret == "" || envelope.Data.Issuer != "https://chat.example.test" ||
		envelope.Data.Audience != server.ContextJWTAudience || envelope.Data.KeyID == "" ||
		envelope.Data.ExpiresSeconds != server.ContextJWTExpiresSeconds {
		t.Fatalf("prepare response = %#v", envelope.Data)
	}
	data := decodeContextJWTData(t, recorder.Body.Bytes())
	wantKeys := []string{"header", "algorithm", "secret", "issuer", "audience", "keyID", "expiresSeconds"}
	if len(data) != len(wantKeys) {
		t.Fatalf("prepare data keys = %v", mapKeys(data))
	}
	for _, key := range wantKeys {
		if _, ok := data[key]; !ok {
			t.Fatalf("prepare data missing %q: %s", key, recorder.Body.String())
		}
	}
	if strings.Count(recorder.Body.String(), envelope.Data.Secret) != 1 {
		t.Fatalf("one-time secret count != 1: %s", recorder.Body.String())
	}
	if repo.prepareInput.PendingSecretEnc == "" ||
		repo.prepareInput.PendingSecretEnc == envelope.Data.Secret ||
		strings.Contains(recorder.Body.String(), repo.prepareInput.PendingSecretEnc) {
		t.Fatalf("prepare ciphertext boundary failed: input=%#v body=%s", repo.prepareInput, recorder.Body.String())
	}
	for _, leak := range fixtureLeaks {
		if strings.Contains(recorder.Body.String(), leak) {
			t.Fatalf("fixture leak %q in response: %s", leak, recorder.Body.String())
		}
	}
	if len(audit.records) != 1 {
		t.Fatalf("audit records = %#v", audit.records)
	}
	auditJSON, err := json.Marshal(audit.records[0].detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range append(fixtureLeaks, envelope.Data.Secret, repo.prepareInput.PendingSecretEnc) {
		if strings.Contains(string(auditJSON), leak) {
			t.Fatalf("secret/ciphertext %q leaked in audit: %s", leak, auditJSON)
		}
	}
}

func TestContextJWTErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid policy", err: appmcp.ErrMCPContextJWTInvalidPolicy, status: http.StatusBadRequest, code: "mcp.context_jwt.invalid_policy"},
		{name: "unavailable", err: appmcp.ErrMCPContextJWTUnavailable, status: http.StatusServiceUnavailable, code: "mcp.context_jwt.unavailable"},
		{name: "pending exists", err: appmcp.ErrMCPContextJWTPendingExists, status: http.StatusConflict, code: "mcp.context_jwt.pending_exists"},
		{name: "rotation conflict", err: appmcp.ErrMCPContextJWTRotationConflict, status: http.StatusConflict, code: "mcp.context_jwt.rotation_conflict"},
		{name: "rotation expired", err: appmcp.ErrMCPContextJWTRotationExpired, status: http.StatusConflict, code: "mcp.context_jwt.rotation_expired"},
		{name: "invalid storage", err: appmcp.ErrMCPContextJWTInvalidStorage, status: http.StatusInternalServerError, code: "mcp.context_jwt.invalid_storage"},
		{name: "server not found", err: appmcp.ErrMCPServerNotFound, status: http.StatusNotFound, code: "mcp.server.not_found"},
		{name: "unknown", err: errors.New("raw-error-leak-marker"), status: http.StatusInternalServerError, code: response.CodeInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set(middleware.ContextKeyRequestID, "request-context-jwt-error")

			if got := writeContextJWTServiceError(ctx, test.err); got != test.code {
				t.Fatalf("writeContextJWTServiceError() = %q, want %q", got, test.code)
			}
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			var envelope response.Envelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.ErrorCode != test.code || envelope.RequestID != "request-context-jwt-error" || envelope.Data != nil {
				t.Fatalf("envelope = %#v", envelope)
			}
			if strings.Contains(recorder.Body.String(), "raw-error-leak-marker") {
				t.Fatalf("raw error leaked in response: %s", recorder.Body.String())
			}
		})
	}

	for _, test := range tests {
		t.Run("route "+test.name, func(t *testing.T) {
			server, _ := newContextJWTHandlerServer(t)
			repo := &contextJWTHandlerRepositoryStub{server: server, policyErr: test.err}
			router := newContextJWTHandlerRouter(repo, nil)
			recorder := serveContextJWTHandlerRequest(
				router,
				http.MethodPatch,
				"/api/v1/admin/mcp/servers/7/context-jwt",
				`{"expiresSeconds":300,"includeName":true,"includeEmail":false,"includeRole":true}`,
			)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			var envelope response.Envelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.ErrorCode != test.code || envelope.RequestID != "request-context-jwt" || envelope.Data != nil {
				t.Fatalf("route envelope = %#v", envelope)
			}
			if strings.Contains(recorder.Body.String(), "raw-error-leak-marker") {
				t.Fatalf("route leaked raw error: %s", recorder.Body.String())
			}
			if len(repo.requestContexts) != 1 || !repo.requestContexts[0] {
				t.Fatalf("route did not pass request context: %v/%v", repo.operations, repo.requestContexts)
			}
		})
	}
}

func TestContextJWTAuditRedaction(t *testing.T) {
	validPublicID := "mcp_" + strings.Repeat("a", 124)
	validKeyID := "ctx_" + strings.Repeat("b", 60)
	policy := &UpdateContextJWTRequest{
		ExpiresSeconds: 300,
		IncludeName:    true,
		IncludeEmail:   false,
		IncludeRole:    true,
	}
	detail := buildContextJWTAuditDetail(contextJWTAuditDetailInput{
		Outcome:        "error",
		ErrorCode:      "mcp.context_jwt.rotation_conflict",
		ServerPublicID: validPublicID,
		KeyID:          validKeyID,
		Policy:         policy,
	})
	want := map[string]interface{}{
		"outcome":          "error",
		"error_code":       "mcp.context_jwt.rotation_conflict",
		"server_public_id": validPublicID,
		"kid":              validKeyID,
		"expires_seconds":  300,
		"include_name":     true,
		"include_email":    false,
		"include_role":     true,
	}
	gotJSON, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("detail = %s, want %s", gotJSON, wantJSON)
	}

	unsafe := buildContextJWTAuditDetail(contextJWTAuditDetailInput{
		Outcome:        "error",
		ServerPublicID: "mcp_" + strings.Repeat("x", 125),
		KeyID:          "ctx_" + strings.Repeat("y", 61),
	})
	if _, ok := unsafe["server_public_id"]; ok {
		t.Fatalf("oversized public id was audited: %#v", unsafe)
	}
	if _, ok := unsafe["kid"]; ok {
		t.Fatalf("oversized kid was audited: %#v", unsafe)
	}
	unsafe = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
		Outcome:        "error",
		ServerPublicID: "mcp_owner@example.test",
		KeyID:          "ctx_bad/path",
	})
	if _, ok := unsafe["server_public_id"]; ok {
		t.Fatalf("invalid public id was audited: %#v", unsafe)
	}
	if _, ok := unsafe["kid"]; ok {
		t.Fatalf("invalid kid was audited: %#v", unsafe)
	}

	successes := []struct {
		name       string
		method     string
		path       string
		body       string
		action     string
		wantPolicy bool
		wantKid    string
	}{
		{
			name: "update", method: http.MethodPatch,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt",
			body:   `{"expiresSeconds":600,"includeName":true,"includeEmail":false,"includeRole":true}`,
			action: "mcp.context_jwt.policy_update", wantPolicy: true, wantKid: "ctx_current_safe",
		},
		{
			name: "prepare", method: http.MethodPost,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt/rotations",
			action: "mcp.context_jwt.rotation_prepare", wantKid: "generated",
		},
		{
			name: "activate", method: http.MethodPost,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt/rotations/ctx_pending_safe/activate",
			action: "mcp.context_jwt.rotation_activate", wantKid: "ctx_pending_safe",
		},
		{
			name: "cancel", method: http.MethodDelete,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt/rotations/ctx_pending_safe",
			action: "mcp.context_jwt.rotation_cancel", wantKid: "ctx_pending_safe",
		},
		{
			name: "disable", method: http.MethodDelete,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt",
			action: "mcp.context_jwt.disable",
		},
	}
	for _, test := range successes {
		t.Run("success "+test.name, func(t *testing.T) {
			server, leaks := newContextJWTHandlerServer(t)
			repo := &contextJWTHandlerRepositoryStub{server: server}
			audit := &probeAuditCapture{}
			router := newContextJWTHandlerRouter(repo, audit)
			recorder := serveContextJWTHandlerRequest(router, test.method, test.path, test.body)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
			}
			detail := requireContextJWTAuditRecord(t, audit, test.action)
			if detail["outcome"] != "success" || detail["server_public_id"] != "mcp_context_public" {
				t.Fatalf("success detail = %#v", detail)
			}
			if _, ok := detail["error_code"]; ok {
				t.Fatalf("success detail contains error code: %#v", detail)
			}
			if test.wantPolicy {
				if detail["expires_seconds"] != 600 || detail["include_name"] != true ||
					detail["include_email"] != false || detail["include_role"] != true {
					t.Fatalf("policy detail = %#v", detail)
				}
			} else {
				for _, key := range []string{"expires_seconds", "include_name", "include_email", "include_role"} {
					if _, ok := detail[key]; ok {
						t.Fatalf("non-update detail contains %q: %#v", key, detail)
					}
				}
			}
			if test.wantKid == "generated" {
				kid, ok := detail["kid"].(string)
				if !ok || !strings.HasPrefix(kid, "ctx_") {
					t.Fatalf("generated kid detail = %#v", detail)
				}
			} else if test.wantKid == "" {
				if _, ok := detail["kid"]; ok {
					t.Fatalf("detail unexpectedly contains kid: %#v", detail)
				}
			} else if detail["kid"] != test.wantKid {
				t.Fatalf("kid = %#v, want %q", detail["kid"], test.wantKid)
			}
			assertContextJWTAuditOmits(t, detail, append(leaks,
				"owner@example.test",
				"raw-error-leak-marker",
				"jwt-header.jwt-payload.jwt-signature",
			)...)
		})
	}

	errorsTable := []struct {
		name      string
		method    string
		path      string
		body      string
		action    string
		status    int
		code      string
		configure func(*contextJWTHandlerRepositoryStub)
		policy    bool
	}{
		{
			name: "update", method: http.MethodPatch,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt",
			body:   `{"expiresSeconds":600,"includeName":true,"includeEmail":false,"includeRole":true}`,
			action: "mcp.context_jwt.policy_update", status: http.StatusInternalServerError,
			code: response.CodeInternal, policy: true,
			configure: func(repo *contextJWTHandlerRepositoryStub) {
				repo.policyErr = errors.New("raw-error-leak-marker")
			},
		},
		{
			name: "prepare", method: http.MethodPost,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt/rotations",
			action: "mcp.context_jwt.rotation_prepare", status: http.StatusConflict,
			code: "mcp.context_jwt.pending_exists",
			configure: func(repo *contextJWTHandlerRepositoryStub) {
				repo.prepareErr = repository.ErrMCPContextJWTPendingExists
			},
		},
		{
			name: "activate", method: http.MethodPost,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt/rotations/ctx_pending_safe/activate",
			action: "mcp.context_jwt.rotation_activate", status: http.StatusConflict,
			code: "mcp.context_jwt.rotation_expired",
			configure: func(repo *contextJWTHandlerRepositoryStub) {
				repo.activateErr = repository.ErrMCPContextJWTPendingExpired
			},
		},
		{
			name: "cancel", method: http.MethodDelete,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt/rotations/ctx_pending_safe",
			action: "mcp.context_jwt.rotation_cancel", status: http.StatusConflict,
			code: "mcp.context_jwt.rotation_conflict",
			configure: func(repo *contextJWTHandlerRepositoryStub) {
				repo.cancelErr = repository.ErrMCPContextJWTRotationConflict
			},
		},
		{
			name: "disable", method: http.MethodDelete,
			path:   "/api/v1/admin/mcp/servers/7/context-jwt",
			action: "mcp.context_jwt.disable", status: http.StatusNotFound,
			code: "mcp.server.not_found",
			configure: func(repo *contextJWTHandlerRepositoryStub) {
				repo.disableErr = repository.ErrNotFound
			},
		},
	}
	for _, test := range errorsTable {
		t.Run("error "+test.name, func(t *testing.T) {
			server, leaks := newContextJWTHandlerServer(t)
			repo := &contextJWTHandlerRepositoryStub{server: server}
			test.configure(repo)
			audit := &probeAuditCapture{}
			router := newContextJWTHandlerRouter(repo, audit)
			recorder := serveContextJWTHandlerRequest(router, test.method, test.path, test.body)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			var envelope response.Envelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.ErrorCode != test.code || envelope.RequestID != "request-context-jwt" || envelope.Data != nil {
				t.Fatalf("error envelope = %#v", envelope)
			}
			detail := requireContextJWTAuditRecord(t, audit, test.action)
			if detail["outcome"] != "error" || detail["error_code"] != test.code {
				t.Fatalf("error detail = %#v", detail)
			}
			for _, key := range []string{"server_public_id", "kid"} {
				if _, ok := detail[key]; ok {
					t.Fatalf("error detail contains %q: %#v", key, detail)
				}
			}
			_, hasPolicy := detail["expires_seconds"]
			if hasPolicy != test.policy {
				t.Fatalf("error policy presence = %v, want %v; detail=%#v", hasPolicy, test.policy, detail)
			}
			assertContextJWTAuditOmits(t, detail, append(leaks,
				"owner@example.test",
				"raw-error-leak-marker",
				"jwt-header.jwt-payload.jwt-signature",
			)...)
			if strings.Contains(recorder.Body.String(), "raw-error-leak-marker") {
				t.Fatalf("raw error leaked in response: %s", recorder.Body.String())
			}
		})
	}

	t.Run("binding error omits policy", func(t *testing.T) {
		server, leaks := newContextJWTHandlerServer(t)
		repo := &contextJWTHandlerRepositoryStub{server: server}
		audit := &probeAuditCapture{}
		router := newContextJWTHandlerRouter(repo, audit)
		recorder := serveContextJWTHandlerRequest(
			router,
			http.MethodPatch,
			"/api/v1/admin/mcp/servers/7/context-jwt",
			`{"includeName":true}`,
		)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
		}
		detail := requireContextJWTAuditRecord(t, audit, "mcp.context_jwt.policy_update")
		if detail["outcome"] != "error" || detail["error_code"] != response.CodeRequestInvalidBody {
			t.Fatalf("binding error detail = %#v", detail)
		}
		for _, key := range []string{
			"server_public_id", "kid", "expires_seconds", "include_name", "include_email", "include_role",
		} {
			if _, ok := detail[key]; ok {
				t.Fatalf("binding error detail contains %q: %#v", key, detail)
			}
		}
		assertContextJWTAuditOmits(t, detail, append(leaks, "owner@example.test")...)
	})
}

func requireContextJWTAuditRecord(
	t *testing.T,
	audit *probeAuditCapture,
	action string,
) map[string]interface{} {
	t.Helper()
	if len(audit.records) != 1 {
		t.Fatalf("audit records = %#v", audit.records)
	}
	record := audit.records[0]
	if record.requestID != "request-context-jwt" || record.userID != 42 ||
		record.action != action || record.resource != "mcp_servers" || record.resourceID != "7" ||
		record.ip != "192.0.2.42" || record.userAgent != "context-jwt-test-agent" {
		t.Fatalf("audit record = %#v", record)
	}
	detail, ok := record.detail.(map[string]interface{})
	if !ok {
		t.Fatalf("audit detail type = %T", record.detail)
	}
	return detail
}

func assertContextJWTAuditOmits(t *testing.T, detail map[string]interface{}, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(string(encoded), value) {
			t.Fatalf("audit leaked %q: %s", value, encoded)
		}
	}
	for _, forbiddenKey := range []string{
		"secret", "jwt", "email", "ciphertext", "issuer", "audience", "error", "raw_error",
	} {
		if _, ok := detail[forbiddenKey]; ok {
			t.Fatalf("audit contains forbidden key %q: %s", forbiddenKey, encoded)
		}
	}
}

// These methods close the Task 3 transport-test repository interface debt in
// the Task 6-owned test file without changing the legacy handler tests.
func (r *handlerMCPRepositoryStub) UpdateContextJWTPolicy(
	context.Context,
	uint,
	repository.UpdateMCPContextJWTPolicyInput,
) (*domainmcp.Server, error) {
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	return &item, nil
}

func (r *handlerMCPRepositoryStub) PrepareContextJWTRotation(
	context.Context,
	repository.PrepareMCPContextJWTRotationInput,
) (*domainmcp.Server, error) {
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	return &item, nil
}

func (r *handlerMCPRepositoryStub) ActivateContextJWTRotation(
	context.Context,
	uint,
	string,
	time.Time,
) (*domainmcp.Server, error) {
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	return &item, nil
}

func (r *handlerMCPRepositoryStub) CancelContextJWTRotation(
	context.Context,
	uint,
	string,
) (*domainmcp.Server, error) {
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	return &item, nil
}

func (r *handlerMCPRepositoryStub) DisableContextJWT(context.Context, uint) (*domainmcp.Server, error) {
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	return &item, nil
}

func (*handlerMCPRepositoryStub) ClearExpiredContextJWTPending(context.Context, time.Time) error {
	return nil
}

func (*controlPlaneRepoStub) ClearExpiredContextJWTPending(context.Context, time.Time) error {
	return nil
}
