package user

import (
	"context"
	"testing"
	"time"

	domainuser "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/user"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/persistence/models"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/persistence/schema"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestIdentityProviderTLSInsecureSkipVerifyRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:identity_provider_tls_policy?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = db.AutoMigrate(&model.AuthIdentityProvider{}); err != nil {
		t.Fatalf("migrate identity providers: %v", err)
	}
	repo := NewRepo(db)
	created, err := repo.CreateIdentityProvider(context.Background(), &domainuser.IdentityProvider{
		PublicID:              "provider_tls",
		Type:                  domainuser.IdentityProviderTypeOIDC,
		Name:                  "TLS Provider",
		Slug:                  "tls-provider",
		TLSInsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("CreateIdentityProvider() error = %v", err)
	}
	if !created.TLSInsecureSkipVerify {
		t.Fatal("created provider lost TLS override")
	}
	loaded, err := repo.GetIdentityProviderBySlug(context.Background(), "tls-provider")
	if err != nil {
		t.Fatalf("GetIdentityProviderBySlug() error = %v", err)
	}
	if !loaded.TLSInsecureSkipVerify {
		t.Fatal("loaded provider lost TLS override")
	}
	secure := false
	updated, err := repo.UpdateIdentityProvider(context.Background(), created.PublicID, repository.UpdateIdentityProviderInput{
		TLSInsecureSkipVerify: &secure,
	})
	if err != nil {
		t.Fatalf("UpdateIdentityProvider() error = %v", err)
	}
	if updated.TLSInsecureSkipVerify {
		t.Fatal("updated provider did not restore TLS verification")
	}
}

func TestListUsersSearchesBeforePagination(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:list_users_search?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}

	users := []model.User{
		{PublicID: "u_first", Username: "first", DisplayName: "First", Email: "first@example.com", Role: "user", Status: "active"},
		{PublicID: "u_second", Username: "second", DisplayName: "Second", Email: "second@example.com", Role: "user", Status: "active"},
		{PublicID: "u_target", Username: "target", DisplayName: "Target", Email: "target@example.com", Role: "user", Status: "active"},
	}
	if err = db.Create(&users).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}

	items, total, err := NewRepo(db).ListUsers(context.Background(), 0, 1, repository.UserListFilter{Query: "target@example.com"})
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if len(items) != 1 || items[0].Email != "target@example.com" {
		t.Fatalf("items = %+v, want target user", items)
	}
}

func TestListUsersFiltersByIdentityProviderAndSubscriptionStatus(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:list_users_filters?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = db.AutoMigrate(
		&model.User{},
		&model.AuthIdentityProvider{},
		&model.UserIdentity{},
		&model.BillingPlan{},
		&model.Subscription{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	users := []model.User{
		{PublicID: "u_paid", Username: "paid", DisplayName: "Paid", Email: "paid@example.com", Role: "user", Status: "active"},
		{PublicID: "u_free", Username: "free", DisplayName: "Free", Email: "free@example.com", Role: "user", Status: "active"},
	}
	if err = db.Create(&users).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}

	providers := []model.AuthIdentityProvider{
		{PublicID: "p_github", Name: "GitHub", Slug: "github", Type: "oauth2"},
		{PublicID: "p_oidc", Name: "OIDC", Slug: "oidc", Type: "oidc"},
	}
	if err = db.Create(&providers).Error; err != nil {
		t.Fatalf("seed providers: %v", err)
	}
	if err = db.Create(&[]model.UserIdentity{
		{UserID: users[0].ID, ProviderID: providers[0].ID, ProviderType: "oauth2", ProviderSubject: "paid", LinkedAt: time.Now()},
		{UserID: users[1].ID, ProviderID: providers[1].ID, ProviderType: "oidc", ProviderSubject: "free", LinkedAt: time.Now()},
	}).Error; err != nil {
		t.Fatalf("seed identities: %v", err)
	}

	plans := []model.BillingPlan{
		{Code: "free", Name: "Free", IsActive: true},
		{Code: "pro", Name: "Pro", IsActive: true},
	}
	if err = db.Create(&plans).Error; err != nil {
		t.Fatalf("seed plans: %v", err)
	}
	now := time.Now()
	if err = db.Create(&model.Subscription{
		UserID:               users[0].ID,
		PlanID:               plans[1].ID,
		PriceID:              1,
		Status:               "active",
		StartAt:              now.Add(-time.Hour),
		CurrentPeriodStartAt: now.Add(-time.Hour),
		CurrentPeriodEndAt:   ptrTime(now.Add(time.Hour)),
	}).Error; err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	repo := NewRepo(db)
	identityItems, total, err := repo.ListUsers(context.Background(), 0, 10, repository.UserListFilter{IdentityProvider: "github"})
	if err != nil {
		t.Fatalf("ListUsers(identity) error = %v", err)
	}
	if total != 1 || len(identityItems) != 1 || identityItems[0].Username != "paid" {
		t.Fatalf("identity filter total=%d items=%+v, want paid", total, identityItems)
	}

	activeItems, total, err := repo.ListUsers(context.Background(), 0, 10, repository.UserListFilter{SubscriptionStatus: "active"})
	if err != nil {
		t.Fatalf("ListUsers(active) error = %v", err)
	}
	if total != 1 || len(activeItems) != 1 || activeItems[0].Username != "paid" {
		t.Fatalf("active filter total=%d items=%+v, want paid", total, activeItems)
	}

	freeItems, total, err := repo.ListUsers(context.Background(), 0, 10, repository.UserListFilter{SubscriptionStatus: "free"})
	if err != nil {
		t.Fatalf("ListUsers(free) error = %v", err)
	}
	if total != 1 || len(freeItems) != 1 || freeItems[0].Username != "free" {
		t.Fatalf("free filter total=%d items=%+v, want free", total, freeItems)
	}
}

func TestDeleteAccountHardRemovesPermissionGroupUserAccess(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:delete_user_permission_groups?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = db.AutoMigrate(schema.Models()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	user := model.User{
		PublicID:    "u_permission_group",
		Username:    "permission-group-user",
		DisplayName: "Permission Group User",
		Email:       "permission-group-user@example.com",
		Role:        "user",
		Status:      "active",
	}
	if err = db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	group := model.PermissionGroup{
		Name:                  "Pro",
		RateMultiplierPercent: 80,
	}
	if err = db.Create(&group).Error; err != nil {
		t.Fatalf("seed permission group: %v", err)
	}
	if err = db.Create(&model.PermissionGroupUserAccess{
		GroupID: group.ID,
		UserID:  user.ID,
	}).Error; err != nil {
		t.Fatalf("seed permission group user access: %v", err)
	}

	if err = NewRepo(db).DeleteAccountHard(context.Background(), user.ID); err != nil {
		t.Fatalf("DeleteAccountHard() error = %v", err)
	}

	var count int64
	if err = db.Model(&model.PermissionGroupUserAccess{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
		t.Fatalf("count permission group user access: %v", err)
	}
	if count != 0 {
		t.Fatalf("permission group user access count = %d, want 0", count)
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
