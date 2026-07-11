package schema

import (
	"strings"
	"testing"
	"time"

	domainchannel "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/channel"
	model "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/persistence/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type legacyMCPServer struct {
	model.ControlPlaneModel
	Name         string     `gorm:"size:128;not null;default:'';comment:MCP服务名称"`
	BaseURL      string     `gorm:"size:512;not null;default:'';comment:MCP服务地址"`
	AuthTokenEnc string     `gorm:"type:text;not null;default:'';comment:加密后的鉴权Token"`
	HeadersJSON  string     `gorm:"type:text;not null;default:'{}';comment:附加请求头JSON"`
	Status       string     `gorm:"size:32;not null;default:'active';index:idx_mcp_servers_status;comment:服务状态(active/inactive)"`
	SortOrder    int        `gorm:"not null;default:0;index:idx_mcp_servers_sort_order;comment:展示顺序"`
	ToolCount    int        `gorm:"not null;default:0;comment:最近发现工具数量"`
	LastSyncedAt *time.Time `gorm:"comment:最近同步工具时间"`
	LastError    string     `gorm:"type:text;not null;default:'';comment:最近同步或调用错误"`
}

func (legacyMCPServer) TableName() string {
	return "mcp_servers"
}

func TestMigrateBackfillsMCPServerPublicIDs(t *testing.T) {
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlite db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	if err = db.AutoMigrate(&legacyMCPServer{}); err != nil {
		t.Fatalf("create legacy mcp schema: %v", err)
	}
	historicalUpdatedAt := []time.Time{
		time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC),
	}
	legacyRows := []legacyMCPServer{
		{
			ControlPlaneModel: model.ControlPlaneModel{CreatedAt: historicalUpdatedAt[0].Add(-time.Hour), UpdatedAt: historicalUpdatedAt[0]},
			Name:              "Legacy A", BaseURL: "https://a.example.test/mcp", HeadersJSON: "{}", Status: "active",
		},
		{
			ControlPlaneModel: model.ControlPlaneModel{CreatedAt: historicalUpdatedAt[1].Add(-time.Hour), UpdatedAt: historicalUpdatedAt[1]},
			Name:              "Legacy B", BaseURL: "https://b.example.test/mcp", HeadersJSON: "{}", Status: "inactive",
		},
	}
	if err = db.Create(&legacyRows).Error; err != nil {
		t.Fatalf("create legacy rows: %v", err)
	}

	if err = Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var rows []model.MCPServer
	if err = db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("list migrated servers: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("migrated rows = %d, want 2", len(rows))
	}
	first, second := rows[0], rows[1]
	if first.PublicID == "" || second.PublicID == "" || first.PublicID == second.PublicID {
		t.Fatalf("invalid public ids: %q %q", first.PublicID, second.PublicID)
	}
	for index, row := range []model.MCPServer{first, second} {
		if !row.UpdatedAt.Equal(historicalUpdatedAt[index]) {
			t.Fatalf("server %d updated_at = %s, want %s", row.ID, row.UpdatedAt, historicalUpdatedAt[index])
		}
		if !strings.HasPrefix(row.PublicID, "mcp_") || len(row.PublicID) != 36 || strings.Contains(row.PublicID, "-") {
			t.Fatalf("unexpected public id %q", row.PublicID)
		}
		if row.ContextJWTAudience != "urn:deeix:mcp:"+row.PublicID {
			t.Fatalf("unexpected audience %q", row.ContextJWTAudience)
		}
		if row.ContextJWTMode != "none" || row.ContextJWTExpiresSeconds != 300 ||
			row.ContextJWTSecretEnc != "" || row.ContextJWTKeyID != "" ||
			row.ContextJWTIncludeName || row.ContextJWTIncludeEmail || row.ContextJWTIncludeRole ||
			row.ContextJWTPendingSecretEnc != "" || row.ContextJWTPendingKeyID != "" ||
			row.ContextJWTPendingCreatedAt != nil || row.ContextJWTPendingExpiresAt != nil {
			t.Fatalf("unexpected signed-context defaults: %#v", row)
		}
	}
	identities := [][2]string{{first.PublicID, first.ContextJWTAudience}, {second.PublicID, second.ContextJWTAudience}}
	if err = Migrate(db); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	var afterSecondMigrate []model.MCPServer
	if err = db.Order("id ASC").Find(&afterSecondMigrate).Error; err != nil {
		t.Fatalf("list servers after second migration: %v", err)
	}
	if len(afterSecondMigrate) != 2 {
		t.Fatalf("rows after second migration = %d, want 2", len(afterSecondMigrate))
	}
	for index, row := range afterSecondMigrate {
		if !row.UpdatedAt.Equal(historicalUpdatedAt[index]) {
			t.Fatalf("server %d second-migrate updated_at = %s, want %s", row.ID, row.UpdatedAt, historicalUpdatedAt[index])
		}
		if row.PublicID != identities[index][0] || row.ContextJWTAudience != identities[index][1] {
			t.Fatalf("server %d identity changed on second migration: %#v", row.ID, row)
		}
	}
	if !db.Migrator().HasIndex(&model.MCPServer{}, "idx_mcp_servers_public_id") {
		t.Fatal("missing partial public-id index")
	}

	duplicate := model.MCPServer{
		PublicID: first.PublicID, Name: "Duplicate", BaseURL: "https://duplicate.example.test/mcp",
		HeadersJSON: "{}", Status: "active", ContextJWTAudience: "urn:deeix:mcp:" + first.PublicID,
	}
	if err = db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate non-empty public id unexpectedly succeeded")
	}

	rollbackRows := []legacyMCPServer{
		{Name: "Rollback A", BaseURL: "https://rollback-a.example.test/mcp", HeadersJSON: "{}", Status: "active"},
		{Name: "Rollback B", BaseURL: "https://rollback-b.example.test/mcp", HeadersJSON: "{}", Status: "active"},
	}
	if err = db.Create(&rollbackRows).Error; err != nil {
		t.Fatalf("older binary blank public-id inserts failed: %v", err)
	}
	var blankCount int64
	if err = db.Model(&model.MCPServer{}).Where("public_id = ?", "").Count(&blankCount).Error; err != nil {
		t.Fatalf("count blank public ids: %v", err)
	}
	if blankCount != 2 {
		t.Fatalf("blank public ids = %d, want 2", blankCount)
	}
}

func TestSeedBillingCatalogBindsDefaultPermissionGroup(t *testing.T) {
	db := openSchemaTestDB(t)
	if err := SeedPermissionGroups(db); err != nil {
		t.Fatalf("SeedPermissionGroups() error = %v", err)
	}
	if err := SeedBillingCatalog(db); err != nil {
		t.Fatalf("SeedBillingCatalog() error = %v", err)
	}

	var plans []model.BillingPlan
	if err := db.Order("code ASC").Find(&plans).Error; err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if len(plans) == 0 {
		t.Fatal("expected seeded billing plans")
	}
	for _, plan := range plans {
		if plan.PermissionGroupID == nil {
			t.Fatalf("plan %q PermissionGroupID is nil", plan.Code)
		}
	}
}

func TestSeedBillingCatalogBackfillsExistingPlans(t *testing.T) {
	db := openSchemaTestDB(t)
	if err := SeedPermissionGroups(db); err != nil {
		t.Fatalf("SeedPermissionGroups() error = %v", err)
	}
	if err := db.Create(&model.BillingPlan{
		Code:     "pro",
		Name:     "Pro",
		IsActive: true,
	}).Error; err != nil {
		t.Fatalf("seed plan: %v", err)
	}

	if err := SeedBillingCatalog(db); err != nil {
		t.Fatalf("SeedBillingCatalog() error = %v", err)
	}

	var plan model.BillingPlan
	if err := db.Where("code = ?", "pro").First(&plan).Error; err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if plan.PermissionGroupID == nil {
		t.Fatal("expected existing plan to be bound to default permission group")
	}
}

func TestSeedPermissionGroupsClearsDefaultGroupUserAccess(t *testing.T) {
	db := openSchemaTestDB(t)
	defaultGroup := model.PermissionGroup{Name: "Default", IsDefault: true}
	manualGroup := model.PermissionGroup{Name: "Manual"}
	if err := db.Create(&[]model.PermissionGroup{defaultGroup, manualGroup}).Error; err != nil {
		t.Fatalf("create groups: %v", err)
	}
	var groups []model.PermissionGroup
	if err := db.Order("id ASC").Find(&groups).Error; err != nil {
		t.Fatalf("list groups: %v", err)
	}
	if err := db.Create(&[]model.PermissionGroupUserAccess{
		{GroupID: groups[0].ID, UserID: 1},
		{GroupID: groups[1].ID, UserID: 1},
	}).Error; err != nil {
		t.Fatalf("create group users: %v", err)
	}

	if err := SeedPermissionGroups(db); err != nil {
		t.Fatalf("SeedPermissionGroups() error = %v", err)
	}

	var defaultRows int64
	if err := db.Model(&model.PermissionGroupUserAccess{}).
		Where("group_id = ?", groups[0].ID).
		Count(&defaultRows).Error; err != nil {
		t.Fatalf("count default rows: %v", err)
	}
	if defaultRows != 0 {
		t.Fatalf("expected default group user access to be cleared, got %d", defaultRows)
	}
	var manualRows int64
	if err := db.Model(&model.PermissionGroupUserAccess{}).
		Where("group_id = ?", groups[1].ID).
		Count(&manualRows).Error; err != nil {
		t.Fatalf("count manual rows: %v", err)
	}
	if manualRows != 1 {
		t.Fatalf("expected manual group user access to remain, got %d", manualRows)
	}
}

func TestSeedPermissionGroupsInitializesDefaultAllModelsRule(t *testing.T) {
	db := openSchemaTestDB(t)
	if err := SeedPermissionGroups(db); err != nil {
		t.Fatalf("SeedPermissionGroups() error = %v", err)
	}

	var defaultGroup model.PermissionGroup
	if err := db.Where("is_default = ?", true).First(&defaultGroup).Error; err != nil {
		t.Fatalf("get default group: %v", err)
	}
	var rule model.PermissionGroupModelRule
	if err := db.Where("group_id = ? AND rule_type = ?", defaultGroup.ID, domainchannel.PermissionGroupModelRuleAll).
		First(&rule).Error; err != nil {
		t.Fatalf("expected default all-model rule: %v", err)
	}
}

func TestSeedPermissionGroupsDoesNotRecreateDefaultAllRuleAfterAccessConfigured(t *testing.T) {
	db := openSchemaTestDB(t)
	defaultGroup := model.PermissionGroup{Name: "Default", IsDefault: true}
	if err := db.Create(&defaultGroup).Error; err != nil {
		t.Fatalf("create default group: %v", err)
	}
	manualGroup := model.PermissionGroup{Name: "Manual"}
	if err := db.Create(&manualGroup).Error; err != nil {
		t.Fatalf("create manual group: %v", err)
	}
	if err := db.Create(&model.PermissionGroupModelRule{
		GroupID:  manualGroup.ID,
		RuleType: domainchannel.PermissionGroupModelRuleVendor,
		Value:    "openai",
	}).Error; err != nil {
		t.Fatalf("create existing rule: %v", err)
	}

	if err := SeedPermissionGroups(db); err != nil {
		t.Fatalf("SeedPermissionGroups() error = %v", err)
	}

	var count int64
	if err := db.Model(&model.PermissionGroupModelRule{}).
		Where("group_id = ? AND rule_type = ?", defaultGroup.ID, domainchannel.PermissionGroupModelRuleAll).
		Count(&count).Error; err != nil {
		t.Fatalf("count default all rule: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no default all rule to be recreated after access was configured, got %d", count)
	}
}

func openSchemaTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = db.AutoMigrate(
		&model.PermissionGroup{},
		&model.PermissionGroupUserAccess{},
		&model.PermissionGroupModelAccess{},
		&model.PermissionGroupModelRule{},
		&model.BillingPlan{},
		&model.BillingPrice{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}
