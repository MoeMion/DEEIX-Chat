package schema

import (
	"strings"
	"testing"
	"time"

	model "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/persistence/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type legacyMCPTool struct {
	ID              uint `gorm:"primaryKey"`
	ServerID        uint
	Name            string
	DisplayName     string
	Description     string
	InputSchemaJSON string
	Status          string
	SortOrder       int
	UpdatedAt       time.Time
}

func (legacyMCPTool) TableName() string { return "mcp_tools" }

func TestMigrateLeavesLegacyMCPToolMetadataPendingConfirmation(t *testing.T) {
	db := openMergedSchemaTestDB(t)
	if err := db.AutoMigrate(&legacyMCPTool{}); err != nil {
		t.Fatalf("migrate legacy MCP tool: %v", err)
	}
	updatedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	legacy := legacyMCPTool{
		ServerID: 1, Name: "tool_a", DisplayName: "Existing title",
		Description: "Existing description", InputSchemaJSON: "{}", Status: "active", UpdatedAt: updatedAt,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy MCP tool: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	var migrated model.MCPTool
	if err := db.First(&migrated, legacy.ID).Error; err != nil {
		t.Fatalf("load migrated MCP tool: %v", err)
	}
	if migrated.MetadataCustomized != nil {
		t.Fatalf("legacy metadata state = %v, want pending confirmation", *migrated.MetadataCustomized)
	}
	if !migrated.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("legacy updated_at = %s, want %s", migrated.UpdatedAt, updatedAt)
	}
}

func TestBackfillContextArtifactMessageIDsUsesAssistantRunOwner(t *testing.T) {
	db := openMergedSchemaTestDB(t)
	if err := db.AutoMigrate(&model.Message{}, &model.ChatContextRecord{}); err != nil {
		t.Fatalf("migrate context artifacts: %v", err)
	}
	userMessage := model.Message{
		ConversationID: 7, UserID: 11, PublicID: "msg_user", RunID: "run_tool", Role: "user", Status: "success",
	}
	if err := db.Create(&userMessage).Error; err != nil {
		t.Fatalf("create user message: %v", err)
	}
	assistantMessage := model.Message{
		ConversationID: 7, UserID: 11, PublicID: "msg_assistant", ParentMessageID: &userMessage.ID,
		RunID: "run_tool", Role: "assistant", Status: "success",
	}
	if err := db.Create(&assistantMessage).Error; err != nil {
		t.Fatalf("create assistant message: %v", err)
	}
	artifact := model.ChatContextRecord{
		RecordType: "artifact", ConversationID: 7, MessageID: userMessage.ID, UserID: 11,
		RunID: "run_tool", Kind: "tool_result", SourceType: "tool_call", SourceID: "call_1", Content: "tool output",
	}
	if err := db.Create(&artifact).Error; err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if err := backfillContextArtifactMessageIDs(db); err != nil {
		t.Fatalf("backfillContextArtifactMessageIDs() error = %v", err)
	}
	if err := db.First(&artifact, artifact.ID).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	if artifact.MessageID != assistantMessage.ID {
		t.Fatalf("artifact message id = %d, want %d", artifact.MessageID, assistantMessage.ID)
	}
}

func openMergedSchemaTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}
