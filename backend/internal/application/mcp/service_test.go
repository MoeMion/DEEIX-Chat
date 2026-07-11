package mcp

import (
	"context"
	"errors"
	"testing"

	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	model "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/persistence/models"
	postgresmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/persistence/postgres/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/pkg/secretbox"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testMCPEncryptionKey = "test-mcp-data-encryption-key"

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
