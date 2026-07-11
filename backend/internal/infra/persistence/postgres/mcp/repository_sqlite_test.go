package mcp

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	model "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/persistence/models"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCreateServerSQLitePersistsSignedContextIdentityUnchanged(t *testing.T) {
	t.Parallel()
	db := openMCPSQLiteTestDB(t)
	repo := NewRepo(db)
	const (
		publicID = "mcp_repository_public_id"
		audience = "urn:repository:audience-must-remain-unchanged"
	)

	created, err := repo.CreateServer(context.Background(), repository.CreateMCPServerInput{
		PublicID: publicID, ContextJWTAudience: audience,
		Name: "Example", BaseURL: "https://example.test/mcp", HeadersJSON: "{}", Status: "active",
	})
	if err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	if created.PublicID != publicID || created.ContextJWTAudience != audience {
		t.Fatalf("created signed-context identity = %#v", created)
	}
	var stored model.MCPServer
	if err = db.First(&stored, created.ID).Error; err != nil {
		t.Fatalf("reload server: %v", err)
	}
	if stored.PublicID != publicID || stored.ContextJWTAudience != audience {
		t.Fatalf("stored public id/audience = %q %q", stored.PublicID, stored.ContextJWTAudience)
	}
}

func TestGetServerSQLiteMapsSignedContextStorageFields(t *testing.T) {
	t.Parallel()
	db := openMCPSQLiteTestDB(t)
	pendingCreatedAt := time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC)
	pendingExpiresAt := pendingCreatedAt.Add(24 * time.Hour)
	stored := model.MCPServer{
		PublicID: "mcp_mapping_fixture", Name: "Example", BaseURL: "https://example.test/mcp",
		HeadersJSON: "{}", Status: "active", ContextJWTMode: "enabled",
		ContextJWTSecretEnc: "current-ciphertext", ContextJWTAudience: "urn:deeix:mcp:mcp_mapping_fixture",
		ContextJWTKeyID: "kid-current", ContextJWTExpiresSeconds: 601,
		ContextJWTIncludeName: true, ContextJWTIncludeEmail: true, ContextJWTIncludeRole: true,
		ContextJWTPendingSecretEnc: "pending-ciphertext", ContextJWTPendingKeyID: "kid-pending",
		ContextJWTPendingCreatedAt: &pendingCreatedAt, ContextJWTPendingExpiresAt: &pendingExpiresAt,
	}
	if err := db.Create(&stored).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}

	got, err := NewRepo(db).GetServer(context.Background(), stored.ID)
	if err != nil {
		t.Fatalf("GetServer() error = %v", err)
	}
	if got.PublicID != stored.PublicID || got.ContextJWTMode != stored.ContextJWTMode ||
		got.ContextJWTSecretEnc != stored.ContextJWTSecretEnc || got.ContextJWTAudience != stored.ContextJWTAudience ||
		got.ContextJWTKeyID != stored.ContextJWTKeyID || got.ContextJWTExpiresSeconds != stored.ContextJWTExpiresSeconds ||
		got.ContextJWTIncludeName != stored.ContextJWTIncludeName || got.ContextJWTIncludeEmail != stored.ContextJWTIncludeEmail ||
		got.ContextJWTIncludeRole != stored.ContextJWTIncludeRole ||
		got.ContextJWTPendingSecretEnc != stored.ContextJWTPendingSecretEnc ||
		got.ContextJWTPendingKeyID != stored.ContextJWTPendingKeyID ||
		got.ContextJWTPendingCreatedAt == nil || !got.ContextJWTPendingCreatedAt.Equal(pendingCreatedAt) ||
		got.ContextJWTPendingExpiresAt == nil || !got.ContextJWTPendingExpiresAt.Equal(pendingExpiresAt) {
		t.Fatalf("mapped signed-context fields = %#v", got)
	}
}

func TestReorderServersWithToolsSQLitePersistsToolOrder(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	ctx := context.Background()
	repo := NewRepo(db)

	server := createMCPServer(t, db, "server-a")
	if err := repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{Name: "tool_a", DisplayName: "Tool A", InputSchemaJSON: "{}", Status: "active"},
		{Name: "tool_b", DisplayName: "Tool B", InputSchemaJSON: "{}", Status: "active"},
	}); err != nil {
		t.Fatalf("replace tools: %v", err)
	}
	initial, err := repo.ListTools(ctx, server.ID, false)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	assertToolNames(t, initial, []string{"tool_a", "tool_b"})

	reorderedGroups, err := repo.ReorderServersWithTools(ctx, []repository.ReorderMCPServerInput{
		{ServerID: server.ID, ToolIDs: []uint{initial[1].ID, initial[0].ID}},
	})
	if err != nil {
		t.Fatalf("reorder tools: %v", err)
	}
	reordered := reorderedGroups[0].Tools
	assertToolNames(t, reordered, []string{"tool_b", "tool_a"})
	if reordered[0].SortOrder != 100 || reordered[1].SortOrder != 200 {
		t.Fatalf("expected normalized sort order, got %#v", reordered)
	}

	if err := repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{Name: "tool_a", DisplayName: "Tool A", InputSchemaJSON: `{"type":"object"}`, Status: "active"},
		{Name: "tool_b", DisplayName: "Tool B", InputSchemaJSON: "{}", Status: "active"},
		{Name: "tool_c", DisplayName: "Tool C", InputSchemaJSON: "{}", Status: "active"},
	}); err != nil {
		t.Fatalf("replace tools after reorder: %v", err)
	}
	afterSync, err := repo.ListTools(ctx, server.ID, false)
	if err != nil {
		t.Fatalf("list tools after sync: %v", err)
	}
	assertToolNames(t, afterSync, []string{"tool_b", "tool_a", "tool_c"})
	if afterSync[2].SortOrder <= afterSync[1].SortOrder {
		t.Fatalf("expected newly discovered tool to be appended, got %#v", afterSync)
	}
}

func TestReorderServersWithToolsSQLiteRejectsForeignTool(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	ctx := context.Background()
	repo := NewRepo(db)

	serverA := createMCPServer(t, db, "server-a")
	serverB := createMCPServer(t, db, "server-b")
	if err := repo.ReplaceServerTools(ctx, serverA.ID, []domainmcp.Tool{
		{Name: "tool_a", DisplayName: "Tool A", InputSchemaJSON: "{}", Status: "active"},
	}); err != nil {
		t.Fatalf("replace server a tools: %v", err)
	}
	if err := repo.ReplaceServerTools(ctx, serverB.ID, []domainmcp.Tool{
		{Name: "tool_b", DisplayName: "Tool B", InputSchemaJSON: "{}", Status: "active"},
	}); err != nil {
		t.Fatalf("replace server b tools: %v", err)
	}
	serverBTools, err := repo.ListTools(ctx, serverB.ID, false)
	if err != nil {
		t.Fatalf("list server b tools: %v", err)
	}
	if _, err = repo.ReorderServersWithTools(ctx, []repository.ReorderMCPServerInput{
		{ServerID: serverA.ID, ToolIDs: []uint{serverBTools[0].ID}},
		{ServerID: serverB.ID, ToolIDs: []uint{}},
	}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected foreign tool reorder to fail with record not found, got %v", err)
	}
}

func TestReorderServersWithToolsSQLiteRejectsPartialToolOrder(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	ctx := context.Background()
	repo := NewRepo(db)

	server := createMCPServer(t, db, "server-a")
	if err := repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{Name: "tool_a", DisplayName: "Tool A", InputSchemaJSON: "{}", Status: "active"},
		{Name: "tool_b", DisplayName: "Tool B", InputSchemaJSON: "{}", Status: "active"},
	}); err != nil {
		t.Fatalf("replace tools: %v", err)
	}
	tools, err := repo.ListTools(ctx, server.ID, false)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	if _, err = repo.ReorderServersWithTools(ctx, []repository.ReorderMCPServerInput{
		{ServerID: server.ID, ToolIDs: []uint{tools[0].ID}},
	}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected partial tool order to fail with record not found, got %v", err)
	}
}

func TestReorderServersWithToolsSQLitePersistsServerOrder(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	ctx := context.Background()
	repo := NewRepo(db)

	serverA := createMCPServer(t, db, "server-a")
	serverB := createMCPServer(t, db, "server-b")
	if err := repo.ReplaceServerTools(ctx, serverA.ID, []domainmcp.Tool{
		{Name: "tool_a", DisplayName: "Tool A", InputSchemaJSON: "{}", Status: "active"},
	}); err != nil {
		t.Fatalf("replace server a tools: %v", err)
	}
	if err := repo.ReplaceServerTools(ctx, serverB.ID, []domainmcp.Tool{
		{Name: "tool_b", DisplayName: "Tool B", InputSchemaJSON: "{}", Status: "active"},
	}); err != nil {
		t.Fatalf("replace server b tools: %v", err)
	}
	serverATools, err := repo.ListTools(ctx, serverA.ID, false)
	if err != nil {
		t.Fatalf("list server a tools: %v", err)
	}
	serverBTools, err := repo.ListTools(ctx, serverB.ID, false)
	if err != nil {
		t.Fatalf("list server b tools: %v", err)
	}

	reordered, err := repo.ReorderServersWithTools(ctx, []repository.ReorderMCPServerInput{
		{ServerID: serverB.ID, ToolIDs: []uint{serverBTools[0].ID}},
		{ServerID: serverA.ID, ToolIDs: []uint{serverATools[0].ID}},
	})
	if err != nil {
		t.Fatalf("reorder servers with tools: %v", err)
	}
	if reordered[0].Server.ID != serverB.ID || reordered[1].Server.ID != serverA.ID {
		t.Fatalf("expected server b before server a, got %#v", reordered)
	}
	if reordered[0].Server.SortOrder != 100 || reordered[1].Server.SortOrder != 200 {
		t.Fatalf("expected normalized server sort order, got %#v", reordered)
	}
}

func TestGetServerSQLiteReturnsRepositoryNotFound(t *testing.T) {
	t.Parallel()
	repo := NewRepo(openMCPSQLiteTestDB(t))

	_, err := repo.GetServer(context.Background(), 404)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected repository.ErrNotFound, got %v", err)
	}
}

func TestUpdateServerSQLiteReturnsRepositoryNotFound(t *testing.T) {
	t.Parallel()
	repo := NewRepo(openMCPSQLiteTestDB(t))
	status := "inactive"

	_, err := repo.UpdateServer(context.Background(), 404, repository.UpdateMCPServerInput{Status: &status})
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected repository.ErrNotFound, got %v", err)
	}
}

func TestDeleteServerSQLiteReturnsRepositoryNotFound(t *testing.T) {
	t.Parallel()
	repo := NewRepo(openMCPSQLiteTestDB(t))

	err := repo.DeleteServer(context.Background(), 404)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected repository.ErrNotFound, got %v", err)
	}
}

func openMCPSQLiteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.MCPServer{}, &model.MCPTool{}); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return db
}

func createMCPServer(t *testing.T, db *gorm.DB, name string) model.MCPServer {
	t.Helper()
	server := model.MCPServer{Name: name, BaseURL: "https://example.com/mcp", HeadersJSON: "{}", Status: "active"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create mcp server: %v", err)
	}
	return server
}

func assertToolNames(t *testing.T, tools []domainmcp.Tool, want []string) {
	t.Helper()
	got := make([]string, 0, len(tools))
	for _, tool := range tools {
		got = append(got, tool.Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected tool order %v, got %v", want, got)
	}
}
