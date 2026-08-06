package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
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

func TestMCPServerHeadersEnabledPersistence(t *testing.T) {
	t.Parallel()
	db := openMCPSQLiteTestDB(t)
	repo := NewRepo(db)
	ctx := context.Background()

	created, err := repo.CreateServer(ctx, repository.CreateMCPServerInput{
		Name: "Headers Disabled", BaseURL: "https://headers.example.test/mcp", HeadersJSON: `{"X-Test":"value"}`,
		HeadersEnabled: false, Status: "active",
	})
	if err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	if created.HeadersEnabled {
		t.Fatal("created headers_enabled = true, want false")
	}
	var stored model.MCPServer
	if err = db.First(&stored, created.ID).Error; err != nil {
		t.Fatalf("get stored server: %v", err)
	}
	if stored.HeadersEnabled {
		t.Fatal("stored headers_enabled = true, want false")
	}

	got, err := repo.GetServer(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetServer() error = %v", err)
	}
	if got.HeadersEnabled {
		t.Fatal("get headers_enabled = true, want false")
	}
	listed, err := repo.ListServers(ctx)
	if err != nil {
		t.Fatalf("ListServers() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListServers() count = %d, want 1", len(listed))
	}
	if listed[0].HeadersEnabled {
		t.Fatal("listed headers_enabled = true, want false")
	}

	headersEnabled := true
	updated, err := repo.UpdateServer(ctx, created.ID, repository.UpdateMCPServerInput{
		HeadersEnabled: &headersEnabled,
	})
	if err != nil {
		t.Fatalf("UpdateServer(true) error = %v", err)
	}
	if !updated.HeadersEnabled {
		t.Fatal("updated headers_enabled = false, want true")
	}

	unchanged, err := repo.UpdateServer(ctx, created.ID, repository.UpdateMCPServerInput{
		HeadersEnabled: nil,
	})
	if err != nil {
		t.Fatalf("UpdateServer(nil) error = %v", err)
	}
	if !unchanged.HeadersEnabled {
		t.Fatal("nil update changed headers_enabled to false")
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

func TestContextJWTPolicyUpdatePersistsFalseValues(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	server := model.MCPServer{
		PublicID: "mcp_policy", Name: "policy", BaseURL: "https://example.test/mcp",
		HeadersJSON: "{}", Status: "active", ContextJWTAudience: "urn:deeix:mcp:mcp_policy",
		ContextJWTExpiresSeconds: 600, ContextJWTIncludeName: true,
		ContextJWTIncludeEmail: true, ContextJWTIncludeRole: true,
	}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}

	got, err := NewRepo(db).UpdateContextJWTPolicy(context.Background(), server.ID, repository.UpdateMCPContextJWTPolicyInput{
		ExpiresSeconds: 120,
		IncludeName:    false,
		IncludeEmail:   false,
		IncludeRole:    false,
	})
	if err != nil {
		t.Fatalf("UpdateContextJWTPolicy() error = %v", err)
	}
	if got.ContextJWTExpiresSeconds != 120 || got.ContextJWTIncludeName || got.ContextJWTIncludeEmail || got.ContextJWTIncludeRole {
		t.Fatalf("updated policy = %#v", got)
	}
}

func TestContextJWTMutationsReturnNotFoundForMissingServer(t *testing.T) {
	now := time.Date(2026, 7, 11, 1, 30, 0, 0, time.UTC)
	tests := []struct {
		name string
		call func(*Repo) error
	}{
		{
			name: "update policy",
			call: func(repo *Repo) error {
				_, err := repo.UpdateContextJWTPolicy(context.Background(), 404, repository.UpdateMCPContextJWTPolicyInput{
					ExpiresSeconds: 300,
				})
				return err
			},
		},
		{
			name: "prepare rotation",
			call: func(repo *Repo) error {
				_, err := repo.PrepareContextJWTRotation(context.Background(), repository.PrepareMCPContextJWTRotationInput{
					ServerID: 404, PendingSecretEnc: "pending-secret", PendingKeyID: "pending-kid",
					CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
				})
				return err
			},
		},
		{
			name: "activate rotation",
			call: func(repo *Repo) error {
				_, err := repo.ActivateContextJWTRotation(context.Background(), 404, "pending-kid", now)
				return err
			},
		},
		{
			name: "cancel rotation",
			call: func(repo *Repo) error {
				_, err := repo.CancelContextJWTRotation(context.Background(), 404, "pending-kid")
				return err
			},
		},
		{
			name: "disable context jwt",
			call: func(repo *Repo) error {
				_, err := repo.DisableContextJWT(context.Background(), 404)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(NewRepo(openMCPSQLiteTestDB(t)))
			if !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("error = %v, want repository.ErrNotFound", err)
			}
		})
	}
}

func TestContextJWTPrepareStateTransitions(t *testing.T) {
	now := time.Date(2026, 7, 11, 2, 0, 0, 0, time.UTC)

	t.Run("empty pending state", func(t *testing.T) {
		db := openMCPSQLiteTestDB(t)
		server := model.MCPServer{
			PublicID: "mcp_prepare_empty", Name: "prepare-empty", BaseURL: "https://example.test/mcp",
			HeadersJSON: "{}", Status: "active", ContextJWTMode: "none",
			ContextJWTSecretEnc: "current-secret", ContextJWTKeyID: "current-kid",
		}
		if err := db.Create(&server).Error; err != nil {
			t.Fatalf("create server: %v", err)
		}

		got, err := NewRepo(db).PrepareContextJWTRotation(context.Background(), repository.PrepareMCPContextJWTRotationInput{
			ServerID: server.ID, PendingSecretEnc: "pending-secret", PendingKeyID: "pending-kid",
			CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		})
		if err != nil {
			t.Fatalf("PrepareContextJWTRotation() error = %v", err)
		}
		if got.ContextJWTMode != "none" || got.ContextJWTSecretEnc != "current-secret" || got.ContextJWTKeyID != "current-kid" {
			t.Fatalf("prepare changed current state = %#v", got)
		}
		assertContextJWTPending(t, got, "pending-secret", "pending-kid", now, now.Add(24*time.Hour))
	})

	t.Run("unexpired pending conflicts", func(t *testing.T) {
		db := openMCPSQLiteTestDB(t)
		createdAt := now.Add(-time.Hour)
		expiresAt := now.Add(time.Hour)
		server := model.MCPServer{
			Name: "prepare-conflict", BaseURL: "https://example.test/mcp", HeadersJSON: "{}", Status: "active",
			ContextJWTPendingSecretEnc: "existing-secret", ContextJWTPendingKeyID: "existing-kid",
			ContextJWTPendingCreatedAt: &createdAt, ContextJWTPendingExpiresAt: &expiresAt,
		}
		if err := db.Create(&server).Error; err != nil {
			t.Fatalf("create server: %v", err)
		}

		_, err := NewRepo(db).PrepareContextJWTRotation(context.Background(), repository.PrepareMCPContextJWTRotationInput{
			ServerID: server.ID, PendingSecretEnc: "replacement-secret", PendingKeyID: "replacement-kid",
			CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		})
		if !errors.Is(err, repository.ErrMCPContextJWTPendingExists) {
			t.Fatalf("expected pending conflict, got %v", err)
		}
		got, err := NewRepo(db).GetServer(context.Background(), server.ID)
		if err != nil {
			t.Fatalf("GetServer() error = %v", err)
		}
		assertContextJWTPending(t, got, "existing-secret", "existing-kid", createdAt, expiresAt)
	})

	t.Run("expired pending is replaced", func(t *testing.T) {
		db := openMCPSQLiteTestDB(t)
		oldCreatedAt := now.Add(-2 * time.Hour)
		oldExpiresAt := now.Add(-time.Hour)
		newExpiresAt := now.Add(24 * time.Hour)
		server := model.MCPServer{
			Name: "prepare-expired", BaseURL: "https://example.test/mcp", HeadersJSON: "{}", Status: "active",
			ContextJWTPendingSecretEnc: "expired-secret", ContextJWTPendingKeyID: "expired-kid",
			ContextJWTPendingCreatedAt: &oldCreatedAt, ContextJWTPendingExpiresAt: &oldExpiresAt,
		}
		if err := db.Create(&server).Error; err != nil {
			t.Fatalf("create server: %v", err)
		}

		got, err := NewRepo(db).PrepareContextJWTRotation(context.Background(), repository.PrepareMCPContextJWTRotationInput{
			ServerID: server.ID, PendingSecretEnc: "replacement-secret", PendingKeyID: "replacement-kid",
			CreatedAt: now, ExpiresAt: newExpiresAt,
		})
		if err != nil {
			t.Fatalf("PrepareContextJWTRotation() error = %v", err)
		}
		assertContextJWTPending(t, got, "replacement-secret", "replacement-kid", now, newExpiresAt)
	})
}

func TestContextJWTActivateStateTransitions(t *testing.T) {
	now := time.Date(2026, 7, 11, 3, 0, 0, 0, time.UTC)

	t.Run("wrong kid conflicts", func(t *testing.T) {
		db := openMCPSQLiteTestDB(t)
		createdAt := now.Add(-time.Hour)
		expiresAt := now.Add(time.Hour)
		server := createContextJWTPendingServer(t, db, "activate-wrong-kid", "pending-secret", "pending-kid", createdAt, expiresAt)

		_, err := NewRepo(db).ActivateContextJWTRotation(context.Background(), server.ID, "wrong-kid", now)
		if !errors.Is(err, repository.ErrMCPContextJWTRotationConflict) {
			t.Fatalf("expected rotation conflict, got %v", err)
		}
	})

	t.Run("expired pending is cleared before sentinel", func(t *testing.T) {
		db := openMCPSQLiteTestDB(t)
		createdAt := now.Add(-2 * time.Hour)
		expiresAt := now.Add(-time.Hour)
		server := createContextJWTPendingServer(t, db, "activate-expired", "expired-secret", "expired-kid", createdAt, expiresAt)

		_, err := NewRepo(db).ActivateContextJWTRotation(context.Background(), server.ID, "expired-kid", now)
		if !errors.Is(err, repository.ErrMCPContextJWTPendingExpired) {
			t.Fatalf("expected pending expired, got %v", err)
		}
		got, err := NewRepo(db).GetServer(context.Background(), server.ID)
		if err != nil {
			t.Fatalf("GetServer() error = %v", err)
		}
		assertContextJWTPendingEmpty(t, got)
	})

	t.Run("pending is promoted atomically", func(t *testing.T) {
		db := openMCPSQLiteTestDB(t)
		createdAt := now.Add(-time.Hour)
		expiresAt := now.Add(time.Hour)
		server := createContextJWTPendingServer(t, db, "activate-success", "pending-secret", "pending-kid", createdAt, expiresAt)
		if err := db.Model(&model.MCPServer{}).Where("id = ?", server.ID).Updates(map[string]interface{}{
			"context_jwt_mode":       "none",
			"context_jwt_secret_enc": "old-secret",
			"context_jwt_key_id":     "old-kid",
		}).Error; err != nil {
			t.Fatalf("seed current state: %v", err)
		}

		got, err := NewRepo(db).ActivateContextJWTRotation(context.Background(), server.ID, "pending-kid", now)
		if err != nil {
			t.Fatalf("ActivateContextJWTRotation() error = %v", err)
		}
		if got.ContextJWTMode != "hs256" || got.ContextJWTSecretEnc != "pending-secret" || got.ContextJWTKeyID != "pending-kid" {
			t.Fatalf("activated current state = %#v", got)
		}
		assertContextJWTPendingEmpty(t, got)
	})
}

func TestContextJWTCancelClearsPendingOnly(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	now := time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)
	createdAt := now.Add(-time.Hour)
	expiresAt := now.Add(time.Hour)
	server := createContextJWTPendingServer(t, db, "cancel", "pending-secret", "pending-kid", createdAt, expiresAt)
	if err := db.Model(&model.MCPServer{}).Where("id = ?", server.ID).Updates(map[string]interface{}{
		"context_jwt_mode":       "hs256",
		"context_jwt_secret_enc": "current-secret",
		"context_jwt_key_id":     "current-kid",
	}).Error; err != nil {
		t.Fatalf("seed current state: %v", err)
	}

	got, err := NewRepo(db).CancelContextJWTRotation(context.Background(), server.ID, "pending-kid")
	if err != nil {
		t.Fatalf("CancelContextJWTRotation() error = %v", err)
	}
	if got.ContextJWTMode != "hs256" || got.ContextJWTSecretEnc != "current-secret" || got.ContextJWTKeyID != "current-kid" {
		t.Fatalf("cancel changed current state = %#v", got)
	}
	assertContextJWTPendingEmpty(t, got)
}

func TestContextJWTCancelWrongOrStaleKIDPreservesPending(t *testing.T) {
	now := time.Date(2026, 7, 11, 4, 30, 0, 0, time.UTC)

	t.Run("wrong kid", func(t *testing.T) {
		db := openMCPSQLiteTestDB(t)
		createdAt := now.Add(-time.Hour)
		expiresAt := now.Add(time.Hour)
		server := createContextJWTPendingServer(t, db, "cancel-wrong", "pending-secret", "pending-kid", createdAt, expiresAt)
		repo := NewRepo(db)

		_, err := repo.CancelContextJWTRotation(context.Background(), server.ID, "wrong-kid")
		if !errors.Is(err, repository.ErrMCPContextJWTRotationConflict) {
			t.Fatalf("error = %v, want rotation conflict", err)
		}
		got, err := repo.GetServer(context.Background(), server.ID)
		if err != nil {
			t.Fatalf("GetServer() error = %v", err)
		}
		assertContextJWTPending(t, got, "pending-secret", "pending-kid", createdAt, expiresAt)
	})

	t.Run("stale kid after replacement", func(t *testing.T) {
		db := openMCPSQLiteTestDB(t)
		oldCreatedAt := now.Add(-2 * time.Hour)
		oldExpiresAt := now.Add(-time.Hour)
		newExpiresAt := now.Add(24 * time.Hour)
		server := createContextJWTPendingServer(t, db, "cancel-stale", "old-secret", "old-kid", oldCreatedAt, oldExpiresAt)
		repo := NewRepo(db)
		if _, err := repo.PrepareContextJWTRotation(context.Background(), repository.PrepareMCPContextJWTRotationInput{
			ServerID: server.ID, PendingSecretEnc: "replacement-secret", PendingKeyID: "replacement-kid",
			CreatedAt: now, ExpiresAt: newExpiresAt,
		}); err != nil {
			t.Fatalf("PrepareContextJWTRotation() error = %v", err)
		}

		_, err := repo.CancelContextJWTRotation(context.Background(), server.ID, "old-kid")
		if !errors.Is(err, repository.ErrMCPContextJWTRotationConflict) {
			t.Fatalf("error = %v, want rotation conflict", err)
		}
		got, err := repo.GetServer(context.Background(), server.ID)
		if err != nil {
			t.Fatalf("GetServer() error = %v", err)
		}
		assertContextJWTPending(t, got, "replacement-secret", "replacement-kid", now, newExpiresAt)
	})
}

func TestContextJWTDisableClearsSecretsAndOptInsPreservingIdentity(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	createdAt := time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(time.Hour)
	server := model.MCPServer{
		PublicID: "mcp_disable", Name: "disable", BaseURL: "https://example.test/mcp",
		HeadersJSON: "{}", Status: "active", ContextJWTMode: "hs256",
		ContextJWTSecretEnc: "current-secret", ContextJWTAudience: "urn:deeix:mcp:mcp_disable",
		ContextJWTKeyID: "current-kid", ContextJWTIncludeName: true,
		ContextJWTIncludeEmail: true, ContextJWTIncludeRole: true,
		ContextJWTPendingSecretEnc: "pending-secret", ContextJWTPendingKeyID: "pending-kid",
		ContextJWTPendingCreatedAt: &createdAt, ContextJWTPendingExpiresAt: &expiresAt,
	}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}

	got, err := NewRepo(db).DisableContextJWT(context.Background(), server.ID)
	if err != nil {
		t.Fatalf("DisableContextJWT() error = %v", err)
	}
	if got.ContextJWTMode != "none" || got.ContextJWTSecretEnc != "" || got.ContextJWTKeyID != "" {
		t.Fatalf("disabled current state = %#v", got)
	}
	if got.ContextJWTIncludeName || got.ContextJWTIncludeEmail || got.ContextJWTIncludeRole {
		t.Fatalf("disable left opt-ins enabled = %#v", got)
	}
	if got.PublicID != "mcp_disable" || got.ContextJWTAudience != "urn:deeix:mcp:mcp_disable" {
		t.Fatalf("disable changed identity = %#v", got)
	}
	assertContextJWTPendingEmpty(t, got)
}

func TestContextJWTClearExpiredPendingOnly(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	expiredCreatedAt := now.Add(-2 * time.Hour)
	expiredAt := now.Add(-time.Hour)
	futureCreatedAt := now.Add(-time.Hour)
	futureExpiresAt := now.Add(time.Hour)
	nullExpiryCreatedAt := now.Add(-3 * time.Hour)
	expired := createContextJWTPendingServer(t, db, "bulk-expired", "expired-secret", "expired-kid", expiredCreatedAt, expiredAt)
	future := createContextJWTPendingServer(t, db, "bulk-future", "future-secret", "future-kid", futureCreatedAt, futureExpiresAt)
	nullExpiry := model.MCPServer{
		Name: "bulk-null", BaseURL: "https://example.test/mcp", HeadersJSON: "{}", Status: "active",
		ContextJWTPendingSecretEnc: "null-secret", ContextJWTPendingKeyID: "null-kid",
		ContextJWTPendingCreatedAt: &nullExpiryCreatedAt,
	}
	if err := db.Create(&nullExpiry).Error; err != nil {
		t.Fatalf("create null-expiry server: %v", err)
	}

	if err := NewRepo(db).ClearExpiredContextJWTPending(context.Background(), now); err != nil {
		t.Fatalf("ClearExpiredContextJWTPending() error = %v", err)
	}
	expiredGot, err := NewRepo(db).GetServer(context.Background(), expired.ID)
	if err != nil {
		t.Fatalf("GetServer(expired) error = %v", err)
	}
	assertContextJWTPendingEmpty(t, expiredGot)
	futureGot, err := NewRepo(db).GetServer(context.Background(), future.ID)
	if err != nil {
		t.Fatalf("GetServer(future) error = %v", err)
	}
	assertContextJWTPending(t, futureGot, "future-secret", "future-kid", futureCreatedAt, futureExpiresAt)
	nullGot, err := NewRepo(db).GetServer(context.Background(), nullExpiry.ID)
	if err != nil {
		t.Fatalf("GetServer(null expiry) error = %v", err)
	}
	if nullGot.ContextJWTPendingSecretEnc != "null-secret" || nullGot.ContextJWTPendingKeyID != "null-kid" ||
		nullGot.ContextJWTPendingCreatedAt == nil || !nullGot.ContextJWTPendingCreatedAt.Equal(nullExpiryCreatedAt) ||
		nullGot.ContextJWTPendingExpiresAt != nil {
		t.Fatalf("null-expiry pending rotation = %#v", nullGot)
	}
}

func TestContextJWTConcurrentPrepareHasExactlyOneWinner(t *testing.T) {
	db, firstRepo, secondRepo := openSharedMCPSQLiteTestRepos(t)
	now := time.Date(2026, 7, 11, 6, 0, 0, 0, time.UTC)
	server := createMCPServer(t, db, "concurrent-prepare")
	firstInput := repository.PrepareMCPContextJWTRotationInput{
		ServerID: server.ID, PendingSecretEnc: "first-secret", PendingKeyID: "first-kid",
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	secondInput := repository.PrepareMCPContextJWTRotationInput{
		ServerID: server.ID, PendingSecretEnc: "second-secret", PendingKeyID: "second-kid",
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}

	results := runContextJWTBarrierCalls(t,
		contextJWTBarrierCall{
			name: "first",
			call: func() (*domainmcp.Server, error) {
				return firstRepo.PrepareContextJWTRotation(context.Background(), firstInput)
			},
		},
		contextJWTBarrierCall{
			name: "second",
			call: func() (*domainmcp.Server, error) {
				return secondRepo.PrepareContextJWTRotation(context.Background(), secondInput)
			},
		},
	)

	winners := 0
	conflicts := 0
	var winner *domainmcp.Server
	for _, result := range results {
		switch {
		case result.err == nil:
			winners++
			winner = result.server
		case errors.Is(result.err, repository.ErrMCPContextJWTPendingExists):
			conflicts++
		default:
			t.Fatalf("%s prepare error = %v", result.name, result.err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("prepare outcomes: winners=%d conflicts=%d results=%#v", winners, conflicts, results)
	}
	stored, err := firstRepo.GetServer(context.Background(), server.ID)
	if err != nil {
		t.Fatalf("GetServer() error = %v", err)
	}
	if winner == nil || stored.ContextJWTPendingKeyID != winner.ContextJWTPendingKeyID ||
		stored.ContextJWTPendingSecretEnc != winner.ContextJWTPendingSecretEnc {
		t.Fatalf("stored winner = %#v, returned winner = %#v", stored, winner)
	}
}

func TestContextJWTConcurrentActivateHasExactlyOneWinner(t *testing.T) {
	db, firstRepo, secondRepo := openSharedMCPSQLiteTestRepos(t)
	now := time.Date(2026, 7, 11, 7, 0, 0, 0, time.UTC)
	createdAt := now.Add(-time.Hour)
	expiresAt := now.Add(time.Hour)
	server := createContextJWTPendingServer(t, db, "concurrent-activate", "pending-secret", "pending-kid", createdAt, expiresAt)

	results := runContextJWTBarrierCalls(t,
		contextJWTBarrierCall{
			name: "first",
			call: func() (*domainmcp.Server, error) {
				return firstRepo.ActivateContextJWTRotation(context.Background(), server.ID, "pending-kid", now)
			},
		},
		contextJWTBarrierCall{
			name: "second",
			call: func() (*domainmcp.Server, error) {
				return secondRepo.ActivateContextJWTRotation(context.Background(), server.ID, "pending-kid", now)
			},
		},
	)

	winners := 0
	conflicts := 0
	for _, result := range results {
		switch {
		case result.err == nil:
			winners++
		case errors.Is(result.err, repository.ErrMCPContextJWTRotationConflict):
			conflicts++
		default:
			t.Fatalf("%s activate error = %v", result.name, result.err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("activate outcomes: winners=%d conflicts=%d results=%#v", winners, conflicts, results)
	}
	stored, err := firstRepo.GetServer(context.Background(), server.ID)
	if err != nil {
		t.Fatalf("GetServer() error = %v", err)
	}
	if stored.ContextJWTMode != "hs256" || stored.ContextJWTSecretEnc != "pending-secret" || stored.ContextJWTKeyID != "pending-kid" {
		t.Fatalf("stored activated state = %#v", stored)
	}
	assertContextJWTPendingEmpty(t, stored)
}

func TestContextJWTExpiredActivateRacingPreparePreservesReplacement(t *testing.T) {
	db, activateRepo, prepareRepo := openSharedMCPSQLiteTestRepos(t)
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	oldCreatedAt := now.Add(-2 * time.Hour)
	oldExpiresAt := now.Add(-time.Hour)
	newExpiresAt := now.Add(24 * time.Hour)
	server := createContextJWTPendingServer(t, db, "activate-prepare-race", "expired-secret", "expired-kid", oldCreatedAt, oldExpiresAt)
	prepareInput := repository.PrepareMCPContextJWTRotationInput{
		ServerID: server.ID, PendingSecretEnc: "replacement-secret", PendingKeyID: "replacement-kid",
		CreatedAt: now, ExpiresAt: newExpiresAt,
	}
	if err := prepareRepo.db.Exec("PRAGMA busy_timeout = 250").Error; err != nil {
		t.Fatalf("set prepare busy timeout: %v", err)
	}

	observedOldSnapshot := make(chan struct{})
	releaseActivate := make(chan struct{})
	var observeOnce sync.Once
	const callbackName = "test:context-jwt-activate-observed-expired-snapshot"
	if err := activateRepo.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*model.MCPServer)
		if !ok || row.ID != server.ID || row.ContextJWTPendingKeyID != "expired-kid" ||
			row.ContextJWTPendingExpiresAt == nil || !row.ContextJWTPendingExpiresAt.Equal(oldExpiresAt) {
			return
		}
		observeOnce.Do(func() {
			close(observedOldSnapshot)
			<-releaseActivate
		})
	}); err != nil {
		t.Fatalf("register activate query barrier: %v", err)
	}

	activateResultCh := make(chan contextJWTBarrierResult, 1)
	go func() {
		activated, err := activateRepo.ActivateContextJWTRotation(context.Background(), server.ID, "expired-kid", now)
		activateResultCh <- contextJWTBarrierResult{name: "activate", server: activated, err: err}
	}()
	select {
	case <-observedOldSnapshot:
	case <-time.After(5 * time.Second):
		close(releaseActivate)
		t.Fatal("activate did not observe the expired pending snapshot")
	}

	prepareResultCh := make(chan contextJWTBarrierResult, 1)
	go func() {
		prepared, err := prepareRepo.PrepareContextJWTRotation(context.Background(), prepareInput)
		prepareResultCh <- contextJWTBarrierResult{name: "prepare", server: prepared, err: err}
	}()
	var prepareResult contextJWTBarrierResult
	select {
	case prepareResult = <-prepareResultCh:
	case <-time.After(5 * time.Second):
		close(releaseActivate)
		t.Fatal("replacement prepare did not finish while activate was paused")
	}
	close(releaseActivate)
	var activateResult contextJWTBarrierResult
	select {
	case activateResult = <-activateResultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("activate did not finish after the barrier was released")
	}

	if prepareResult.err != nil {
		t.Fatalf("prepare error = %v", prepareResult.err)
	}
	if !errors.Is(activateResult.err, repository.ErrMCPContextJWTRotationConflict) {
		t.Fatalf("activate error = %v, want rotation conflict", activateResult.err)
	}
	stored, err := activateRepo.GetServer(context.Background(), server.ID)
	if err != nil {
		t.Fatalf("GetServer() error = %v", err)
	}
	assertContextJWTPending(t, stored, "replacement-secret", "replacement-kid", now, newExpiresAt)
}

func TestReorderServersWithToolsSQLitePersistsToolOrder(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	ctx := context.Background()
	repo := NewRepo(db)

	server := createMCPServer(t, db, "server-a")
	if err := repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{Name: "tool_a", DisplayName: "Tool A", InputSchemaJSON: "{}", Status: "active"},
		{Name: "tool_b", DisplayName: "Tool B", InputSchemaJSON: "{}", Status: "active"},
	}, false); err != nil {
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
	}, false); err != nil {
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

func TestReplaceServerToolsRefreshesRemoteMetadataAndPreservesCustomizedMetadata(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	ctx := context.Background()
	repo := NewRepo(db)
	server := createMCPServer(t, db, "server-metadata")

	if err := repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{
			Name:            "tool_a",
			DisplayName:     "Old title",
			Description:     "Old description",
			InputSchemaJSON: `{"type":"object","required":["old"]}`,
			Status:          "active",
		},
	}, false); err != nil {
		t.Fatalf("replace initial tools: %v", err)
	}
	initial, err := repo.ListTools(ctx, server.ID, false)
	if err != nil {
		t.Fatalf("list initial tools: %v", err)
	}
	if len(initial) != 1 {
		t.Fatalf("initial tools = %#v, want one tool", initial)
	}
	initialTool := initial[0]
	if initialTool.AttachmentInputMode != domainmcp.AttachmentInputModeNone ||
		initialTool.AttachmentArgument != "" ||
		initialTool.AttachmentEncoding != "" ||
		initialTool.AttachmentPromptArgument != "" {
		t.Fatalf("new tool has unexpected attachment processor defaults: %#v", initialTool)
	}
	inactive := "inactive"
	attachmentMode := domainmcp.AttachmentInputModeImage
	attachmentArgument := "image"
	attachmentEncoding := domainmcp.AttachmentEncodingDataURL
	promptArgument := "prompt"
	if _, err = repo.UpdateTool(ctx, initialTool.ID, repository.UpdateMCPToolInput{
		Status:                   &inactive,
		AttachmentInputMode:      &attachmentMode,
		AttachmentArgument:       &attachmentArgument,
		AttachmentEncoding:       &attachmentEncoding,
		AttachmentPromptArgument: &promptArgument,
	}); err != nil {
		t.Fatalf("disable tool: %v", err)
	}

	if err = repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{
			Name:                     "tool_a",
			DisplayName:              "New title",
			Description:              "New description",
			InputSchemaJSON:          `{"type":"object","properties":{"image":{"type":"string"},"prompt":{"type":"string"}},"required":["image"]}`,
			AttachmentInputMode:      attachmentMode,
			AttachmentArgument:       attachmentArgument,
			AttachmentEncoding:       attachmentEncoding,
			AttachmentPromptArgument: promptArgument,
			Status:                   "active",
		},
	}, false); err != nil {
		t.Fatalf("replace updated tools: %v", err)
	}

	updated, err := repo.ListTools(ctx, server.ID, false)
	if err != nil {
		t.Fatalf("list updated tools: %v", err)
	}
	if len(updated) != 1 {
		t.Fatalf("updated tools = %#v, want one tool", updated)
	}
	updatedTool := updated[0]
	if updatedTool.ID != initialTool.ID {
		t.Fatalf("tool id = %d, want preserved id %d", updatedTool.ID, initialTool.ID)
	}
	if updatedTool.DisplayName != "New title" || updatedTool.Description != "New description" {
		t.Fatalf("tool metadata = %q/%q, want refreshed values", updatedTool.DisplayName, updatedTool.Description)
	}
	if updatedTool.InputSchemaJSON != `{"type":"object","properties":{"image":{"type":"string"},"prompt":{"type":"string"}},"required":["image"]}` {
		t.Fatalf("tool schema = %s, want refreshed schema", updatedTool.InputSchemaJSON)
	}
	if updatedTool.Status != "inactive" || updatedTool.SortOrder != initialTool.SortOrder {
		t.Fatalf("local controls = %s/%d, want inactive/%d", updatedTool.Status, updatedTool.SortOrder, initialTool.SortOrder)
	}
	if updatedTool.AttachmentInputMode != attachmentMode ||
		updatedTool.AttachmentArgument != attachmentArgument ||
		updatedTool.AttachmentEncoding != attachmentEncoding ||
		updatedTool.AttachmentPromptArgument != promptArgument {
		t.Fatalf("attachment processor configuration was not preserved: %#v", updatedTool)
	}
	storedTool := loadStoredMCPTool(t, db, updatedTool.ID)
	if storedTool.MetadataCustomized == nil || *storedTool.MetadataCustomized {
		t.Fatal("remote metadata refresh unexpectedly marked tool as customized")
	}
	unchangedTitle := updatedTool.DisplayName
	unchangedDescription := updatedTool.Description
	if _, err = repo.UpdateTool(ctx, updatedTool.ID, repository.UpdateMCPToolInput{
		DisplayName: &unchangedTitle,
		Description: &unchangedDescription,
	}); err != nil {
		t.Fatalf("save unchanged metadata: %v", err)
	}
	storedTool = loadStoredMCPTool(t, db, updatedTool.ID)
	if storedTool.MetadataCustomized == nil || *storedTool.MetadataCustomized {
		t.Fatal("saving unchanged metadata marked tool as customized")
	}

	customTitle := "Custom title"
	customDescription := "Custom description"
	if _, err = repo.UpdateTool(ctx, initialTool.ID, repository.UpdateMCPToolInput{
		DisplayName: &customTitle,
		Description: &customDescription,
	}); err != nil {
		t.Fatalf("customize tool metadata: %v", err)
	}
	if err = repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{
			Name:            "tool_a",
			DisplayName:     "Latest remote title",
			Description:     "Latest remote description",
			InputSchemaJSON: `{"type":"object","required":["latest"]}`,
			Status:          "active",
		},
	}, false); err != nil {
		t.Fatalf("replace tools after customization: %v", err)
	}

	afterCustomization, err := repo.ListTools(ctx, server.ID, false)
	if err != nil {
		t.Fatalf("list tools after customization: %v", err)
	}
	if len(afterCustomization) != 1 {
		t.Fatalf("tools after customization = %#v, want one tool", afterCustomization)
	}
	customizedTool := afterCustomization[0]
	if customizedTool.DisplayName != customTitle || customizedTool.Description != customDescription {
		t.Fatalf("effective metadata = %q/%q, want custom values", customizedTool.DisplayName, customizedTool.Description)
	}
	if customizedTool.InputSchemaJSON != `{"type":"object","required":["latest"]}` {
		t.Fatalf("tool schema = %s, want latest remote schema", customizedTool.InputSchemaJSON)
	}
	if customizedTool.Status != "inactive" || customizedTool.SortOrder != initialTool.SortOrder {
		t.Fatalf("local controls after customization = %s/%d, want inactive/%d", customizedTool.Status, customizedTool.SortOrder, initialTool.SortOrder)
	}
	if customizedTool.AttachmentInputMode != domainmcp.AttachmentInputModeNone ||
		customizedTool.AttachmentArgument != "" ||
		customizedTool.AttachmentEncoding != "" ||
		customizedTool.AttachmentPromptArgument != "" {
		t.Fatalf("disabled attachment processor configuration was not cleared: %#v", customizedTool)
	}
	storedTool = loadStoredMCPTool(t, db, customizedTool.ID)
	if storedTool.MetadataCustomized == nil || !*storedTool.MetadataCustomized {
		t.Fatal("administrator metadata update was not marked as customized")
	}
	servers, err := repo.ListServers(ctx)
	if err != nil {
		t.Fatalf("list servers after customization: %v", err)
	}
	if len(servers) != 1 || !servers[0].RequiresToolMetadataSyncConfirmation {
		t.Fatalf("server customization flag = %#v, want true", servers)
	}
	serverAfterCustomization, err := repo.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatalf("get server after customization: %v", err)
	}
	if !serverAfterCustomization.RequiresToolMetadataSyncConfirmation {
		t.Fatal("single server response did not require metadata sync confirmation")
	}

	if err = repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{
			Name:            "tool_a",
			DisplayName:     "Latest remote title",
			Description:     "Latest remote description",
			InputSchemaJSON: `{"type":"object","required":["overwritten"]}`,
			Status:          "active",
		},
	}, true); err != nil {
		t.Fatalf("replace tools with overwrite: %v", err)
	}
	afterOverwrite, err := repo.ListTools(ctx, server.ID, false)
	if err != nil {
		t.Fatalf("list tools after overwrite: %v", err)
	}
	if len(afterOverwrite) != 1 {
		t.Fatalf("tools after overwrite = %#v, want one tool", afterOverwrite)
	}
	overwrittenTool := afterOverwrite[0]
	if overwrittenTool.DisplayName != "Latest remote title" || overwrittenTool.Description != "Latest remote description" {
		t.Fatalf("overwritten metadata = %q/%q, want latest remote values", overwrittenTool.DisplayName, overwrittenTool.Description)
	}
	if overwrittenTool.InputSchemaJSON != `{"type":"object","required":["overwritten"]}` {
		t.Fatalf("tool schema after overwrite = %s", overwrittenTool.InputSchemaJSON)
	}
	storedTool = loadStoredMCPTool(t, db, overwrittenTool.ID)
	if storedTool.MetadataCustomized == nil || *storedTool.MetadataCustomized {
		t.Fatal("overwritten remote metadata remained marked as customized")
	}
	if overwrittenTool.Status != "inactive" || overwrittenTool.SortOrder != initialTool.SortOrder {
		t.Fatalf("local controls after overwrite = %s/%d, want inactive/%d", overwrittenTool.Status, overwrittenTool.SortOrder, initialTool.SortOrder)
	}
	servers, err = repo.ListServers(ctx)
	if err != nil {
		t.Fatalf("list servers after overwrite: %v", err)
	}
	if len(servers) != 1 || servers[0].RequiresToolMetadataSyncConfirmation {
		t.Fatalf("server customization flag = %#v, want false", servers)
	}
	serverAfterOverwrite, err := repo.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatalf("get server after overwrite: %v", err)
	}
	if serverAfterOverwrite.RequiresToolMetadataSyncConfirmation {
		t.Fatal("single server response still required metadata sync confirmation after overwrite")
	}
}

func TestReplaceServerToolsPreservesLegacyMetadataAfterConfirmation(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	ctx := context.Background()
	repo := NewRepo(db)
	server := createMCPServer(t, db, "server-legacy-metadata")
	if err := repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{
			Name:            "tool_a",
			DisplayName:     "Remote title",
			Description:     "Remote description",
			InputSchemaJSON: "{}",
			Status:          "active",
		},
		{
			Name:            "tool_b",
			DisplayName:     "Stable remote title",
			Description:     "Stable remote description",
			InputSchemaJSON: "{}",
			Status:          "active",
		},
	}, false); err != nil {
		t.Fatalf("replace initial tools: %v", err)
	}
	tools, err := repo.ListTools(ctx, server.ID, false)
	if err != nil || len(tools) != 2 {
		t.Fatalf("list initial tools = %#v, error = %v", tools, err)
	}
	if err = db.Model(&model.MCPTool{}).
		Where("server_id = ?", server.ID).
		UpdateColumn("metadata_customized", nil).Error; err != nil {
		t.Fatalf("simulate legacy metadata state: %v", err)
	}
	if err = db.Model(&model.MCPTool{}).
		Where("server_id = ? AND name = ?", server.ID, "tool_a").
		UpdateColumns(map[string]interface{}{
			"display_name": "Existing title",
			"description":  "Existing description",
		}).Error; err != nil {
		t.Fatalf("simulate legacy metadata: %v", err)
	}
	servers, err := repo.ListServers(ctx)
	if err != nil || len(servers) != 1 || !servers[0].RequiresToolMetadataSyncConfirmation {
		t.Fatalf("legacy confirmation state = %#v, error = %v", servers, err)
	}

	if err = repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{
			Name:            "tool_a",
			DisplayName:     "Latest remote title",
			Description:     "Latest remote description",
			InputSchemaJSON: `{"type":"object"}`,
			Status:          "active",
		},
		{
			Name:            "tool_b",
			DisplayName:     "Stable remote title",
			Description:     "Stable remote description",
			InputSchemaJSON: `{"type":"object"}`,
			Status:          "active",
		},
	}, false); err != nil {
		t.Fatalf("preserve legacy metadata: %v", err)
	}
	preserved, err := repo.ListTools(ctx, server.ID, false)
	if err != nil || len(preserved) != 2 {
		t.Fatalf("list preserved tools = %#v, error = %v", preserved, err)
	}
	toolsByName := map[string]domainmcp.Tool{}
	for _, tool := range preserved {
		toolsByName[tool.Name] = tool
	}
	if toolsByName["tool_a"].DisplayName != "Existing title" || toolsByName["tool_a"].Description != "Existing description" {
		t.Fatalf("preserved metadata = %q/%q", toolsByName["tool_a"].DisplayName, toolsByName["tool_a"].Description)
	}
	customized := loadStoredMCPTool(t, db, toolsByName["tool_a"].ID)
	if customized.MetadataCustomized == nil || !*customized.MetadataCustomized {
		t.Fatalf("changed legacy metadata state = %v, want true", customized.MetadataCustomized)
	}
	remoteManaged := loadStoredMCPTool(t, db, toolsByName["tool_b"].ID)
	if remoteManaged.MetadataCustomized == nil || *remoteManaged.MetadataCustomized {
		t.Fatalf("unchanged legacy metadata state = %v, want false", remoteManaged.MetadataCustomized)
	}
}

func TestRemovingMCPToolsCleansConversationProjectAssociations(t *testing.T) {
	db := openMCPSQLiteTestDB(t)
	ctx := context.Background()
	repo := NewRepo(db)
	server := createMCPServer(t, db, "server-cascade")
	if err := repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{Name: "tool_a", DisplayName: "Tool A", InputSchemaJSON: "{}", Status: "active"},
		{Name: "tool_b", DisplayName: "Tool B", InputSchemaJSON: "{}", Status: "active"},
	}, false); err != nil {
		t.Fatalf("replace tools: %v", err)
	}
	tools, err := repo.ListTools(ctx, server.ID, false)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range tools {
		if err = db.Create(&model.ConversationProjectMCPTool{ProjectID: 9, ToolID: tool.ID}).Error; err != nil {
			t.Fatalf("create project MCP association: %v", err)
		}
	}

	if err = repo.ReplaceServerTools(ctx, server.ID, []domainmcp.Tool{
		{Name: "tool_b", DisplayName: "Tool B", InputSchemaJSON: "{}", Status: "active"},
	}, false); err != nil {
		t.Fatalf("replace tools with removal: %v", err)
	}
	var associations []model.ConversationProjectMCPTool
	if err = db.Order("tool_id ASC").Find(&associations).Error; err != nil {
		t.Fatalf("list project MCP associations: %v", err)
	}
	if len(associations) != 1 || associations[0].ToolID != tools[1].ID {
		t.Fatalf("associations after tool removal = %#v", associations)
	}

	if err = repo.DeleteServer(ctx, server.ID); err != nil {
		t.Fatalf("DeleteServer() error = %v", err)
	}
	var associationCount int64
	if err = db.Model(&model.ConversationProjectMCPTool{}).Count(&associationCount).Error; err != nil {
		t.Fatalf("count project MCP associations: %v", err)
	}
	if associationCount != 0 {
		t.Fatalf("project MCP association count = %d, want 0", associationCount)
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
	}, false); err != nil {
		t.Fatalf("replace server a tools: %v", err)
	}
	if err := repo.ReplaceServerTools(ctx, serverB.ID, []domainmcp.Tool{
		{Name: "tool_b", DisplayName: "Tool B", InputSchemaJSON: "{}", Status: "active"},
	}, false); err != nil {
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
	}, false); err != nil {
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
	}, false); err != nil {
		t.Fatalf("replace server a tools: %v", err)
	}
	if err := repo.ReplaceServerTools(ctx, serverB.ID, []domainmcp.Tool{
		{Name: "tool_b", DisplayName: "Tool B", InputSchemaJSON: "{}", Status: "active"},
	}, false); err != nil {
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
	if err := db.AutoMigrate(&model.MCPServer{}, &model.MCPTool{}, &model.ConversationProjectMCPTool{}); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return db
}

func openSharedMCPSQLiteTestRepos(t *testing.T) (*gorm.DB, *Repo, *Repo) {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "mcp-shared.db")) +
		"?_busy_timeout=10000&_journal_mode=WAL"
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatalf("open shared sqlite: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("get shared sqlite connection: %v", err)
		}
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		t.Cleanup(func() {
			if err := sqlDB.Close(); err != nil {
				t.Errorf("close shared sqlite: %v", err)
			}
		})
		return db
	}
	firstDB := open()
	if err := firstDB.AutoMigrate(&model.MCPServer{}, &model.MCPTool{}); err != nil {
		t.Fatalf("migrate shared sqlite: %v", err)
	}
	secondDB := open()
	return firstDB, NewRepo(firstDB), NewRepo(secondDB)
}

type contextJWTBarrierCall struct {
	name string
	call func() (*domainmcp.Server, error)
}

type contextJWTBarrierResult struct {
	name   string
	server *domainmcp.Server
	err    error
}

func runContextJWTBarrierCalls(t *testing.T, calls ...contextJWTBarrierCall) []contextJWTBarrierResult {
	t.Helper()
	ready := make(chan struct{}, len(calls))
	start := make(chan struct{})
	resultCh := make(chan struct {
		index  int
		result contextJWTBarrierResult
	}, len(calls))
	for index, item := range calls {
		go func() {
			ready <- struct{}{}
			<-start
			server, err := item.call()
			resultCh <- struct {
				index  int
				result contextJWTBarrierResult
			}{
				index: index,
				result: contextJWTBarrierResult{
					name: item.name, server: server, err: err,
				},
			}
		}()
	}
	for range calls {
		<-ready
	}
	close(start)
	results := make([]contextJWTBarrierResult, len(calls))
	for range calls {
		item := <-resultCh
		results[item.index] = item.result
	}
	return results
}

func createMCPServer(t *testing.T, db *gorm.DB, name string) model.MCPServer {
	t.Helper()
	server := model.MCPServer{Name: name, BaseURL: "https://example.com/mcp", HeadersJSON: "{}", Status: "active"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create mcp server: %v", err)
	}
	return server
}

func createContextJWTPendingServer(
	t *testing.T,
	db *gorm.DB,
	name string,
	secret string,
	kid string,
	createdAt time.Time,
	expiresAt time.Time,
) model.MCPServer {
	t.Helper()
	server := model.MCPServer{
		Name: name, BaseURL: "https://example.test/mcp", HeadersJSON: "{}", Status: "active",
		ContextJWTPendingSecretEnc: secret, ContextJWTPendingKeyID: kid,
		ContextJWTPendingCreatedAt: &createdAt, ContextJWTPendingExpiresAt: &expiresAt,
	}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create mcp server: %v", err)
	}
	return server
}

func assertContextJWTPending(
	t *testing.T,
	server *domainmcp.Server,
	wantSecret string,
	wantKID string,
	wantCreatedAt time.Time,
	wantExpiresAt time.Time,
) {
	t.Helper()
	if server.ContextJWTPendingSecretEnc != wantSecret || server.ContextJWTPendingKeyID != wantKID ||
		server.ContextJWTPendingCreatedAt == nil || !server.ContextJWTPendingCreatedAt.Equal(wantCreatedAt) ||
		server.ContextJWTPendingExpiresAt == nil || !server.ContextJWTPendingExpiresAt.Equal(wantExpiresAt) {
		t.Fatalf("pending rotation = %#v", server)
	}
}

func assertContextJWTPendingEmpty(t *testing.T, server *domainmcp.Server) {
	t.Helper()
	if server.ContextJWTPendingSecretEnc != "" || server.ContextJWTPendingKeyID != "" ||
		server.ContextJWTPendingCreatedAt != nil || server.ContextJWTPendingExpiresAt != nil {
		t.Fatalf("pending rotation not empty = %#v", server)
	}
}

func loadStoredMCPTool(t *testing.T, db *gorm.DB, toolID uint) model.MCPTool {
	t.Helper()
	var tool model.MCPTool
	if err := db.First(&tool, "id = ?", toolID).Error; err != nil {
		t.Fatalf("load stored MCP tool: %v", err)
	}
	return tool
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
