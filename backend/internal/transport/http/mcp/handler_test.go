package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/mcp"
	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"github.com/gin-gonic/gin"
)

func TestUpdateServerMapsPartialRequestWithoutSynthesizingFields(t *testing.T) {
	repo := &handlerMCPRepositoryStub{server: handlerTestMCPServer()}
	router := newMCPHandlerTestRouter(repo)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/servers/7", strings.NewReader(`{"status":"inactive"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if repo.updateCalls != 1 {
		t.Fatalf("expected one update, got %d", repo.updateCalls)
	}
	if repo.updatedInput.Name != nil || repo.updatedInput.BaseURL != nil || repo.updatedInput.AuthTokenEnc != nil || repo.updatedInput.HeadersJSON != nil {
		t.Fatalf("partial request synthesized absent fields: %#v", repo.updatedInput)
	}
	if repo.updatedInput.Status == nil || *repo.updatedInput.Status != "inactive" {
		t.Fatalf("expected inactive status pointer, got %#v", repo.updatedInput.Status)
	}
	if !strings.Contains(recorder.Body.String(), `"authTokenConfigured":true`) {
		t.Fatalf("expected configured state in response: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "real-secret") {
		t.Fatalf("response exposed sensitive header: %s", recorder.Body.String())
	}
}

func TestUpdateServerReturnsNotFound(t *testing.T) {
	repo := &handlerMCPRepositoryStub{getErr: repository.ErrNotFound}
	router := newMCPHandlerTestRouter(repo)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/servers/404", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"data":null`) {
		t.Fatalf("expected standard error envelope, got %s", recorder.Body.String())
	}
}

func TestDeleteServerReturnsNotFound(t *testing.T) {
	repo := &handlerMCPRepositoryStub{deleteErr: repository.ErrNotFound}
	router := newMCPHandlerTestRouter(repo)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/servers/404", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"data":null`) {
		t.Fatalf("expected standard error envelope, got %s", recorder.Body.String())
	}
}

func TestUpdateServerRejectsInvalidAuthTokenPatch(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty token", body: `{"authToken":"   "}`},
		{name: "clear and token", body: `{"authToken":"replacement","clearAuthToken":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &handlerMCPRepositoryStub{server: handlerTestMCPServer()}
			router := newMCPHandlerTestRouter(repo)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPatch, "/servers/7", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
			}
			if repo.updateCalls != 0 {
				t.Fatalf("invalid auth patch wrote %d updates", repo.updateCalls)
			}
		})
	}
}

func TestToServerResponseReportsAuthTokenConfigured(t *testing.T) {
	tests := []struct {
		name       string
		ciphertext string
		want       bool
	}{
		{name: "configured", ciphertext: "v1:ciphertext", want: true},
		{name: "empty", ciphertext: "", want: false},
		{name: "whitespace", ciphertext: "   ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toServerResponse(domainmcp.Server{AuthTokenEnc: tt.ciphertext})
			if got.AuthTokenConfigured != tt.want {
				t.Fatalf("got AuthTokenConfigured=%v want %v", got.AuthTokenConfigured, tt.want)
			}
		})
	}
}

func newMCPHandlerTestRouter(repo repository.MCPRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	service := appmcp.NewServiceWithRuntime(config.NewRuntime(config.Config{
		Env:               "dev",
		DataEncryptionKey: "test-mcp-handler-data-encryption-key",
	}), repo, nil)
	handler := NewHandler(service)
	router := gin.New()
	router.PATCH("/servers/:id", handler.UpdateServer)
	router.DELETE("/servers/:id", handler.DeleteServer)
	return router
}

func handlerTestMCPServer() *domainmcp.Server {
	return &domainmcp.Server{
		ID:           7,
		Name:         "Example",
		BaseURL:      "https://example.com/mcp",
		AuthTokenEnc: "existing-ciphertext",
		HeadersJSON:  `{"X-API-Key":"real-secret","X-Tenant":"old"}`,
		Status:       "active",
	}
}

type handlerMCPRepositoryStub struct {
	server       *domainmcp.Server
	getErr       error
	updateErr    error
	deleteErr    error
	updateCalls  int
	updatedInput repository.UpdateMCPServerInput
}

func (*handlerMCPRepositoryStub) CreateServer(context.Context, repository.CreateMCPServerInput) (*domainmcp.Server, error) {
	return nil, nil
}

func (r *handlerMCPRepositoryStub) UpdateServer(_ context.Context, _ uint, input repository.UpdateMCPServerInput) (*domainmcp.Server, error) {
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

func (*handlerMCPRepositoryStub) ListServers(context.Context) ([]domainmcp.Server, error) {
	return nil, nil
}

func (r *handlerMCPRepositoryStub) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	return &item, nil
}

func (r *handlerMCPRepositoryStub) DeleteServer(context.Context, uint) error {
	return r.deleteErr
}

func (*handlerMCPRepositoryStub) ReplaceServerTools(context.Context, uint, []domainmcp.Tool) error {
	return nil
}

func (*handlerMCPRepositoryStub) ListTools(context.Context, uint, bool) ([]domainmcp.Tool, error) {
	return nil, nil
}

func (*handlerMCPRepositoryStub) ListToolsByIDs(context.Context, []uint) ([]domainmcp.Tool, error) {
	return nil, nil
}

func (*handlerMCPRepositoryStub) UpdateTool(context.Context, uint, repository.UpdateMCPToolInput) (*domainmcp.Tool, error) {
	return nil, nil
}

func (*handlerMCPRepositoryStub) UpdateServerToolsStatus(context.Context, uint, []uint, string) ([]domainmcp.Tool, error) {
	return nil, nil
}

func (*handlerMCPRepositoryStub) ReorderServersWithTools(context.Context, []repository.ReorderMCPServerInput) ([]domainmcp.ServerWithTools, error) {
	return nil, nil
}
