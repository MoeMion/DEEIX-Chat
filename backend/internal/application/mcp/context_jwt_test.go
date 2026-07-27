package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/pkg/secretbox"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
)

const (
	contextJWTTestDataKey = "test-context-jwt-data-encryption-key"
	contextJWTTestKeyID   = "ctx_test_pending"
)

var contextJWTTestNow = time.Date(2026, time.July, 11, 8, 30, 0, 0, time.UTC)

func TestContextJWTApplicationContract(t *testing.T) {
	var _ func(*Service, context.Context, uint, ContextJWTPolicyInput) (ContextJWTStatus, error) = (*Service).UpdateContextJWTPolicy
	var _ func(*Service, context.Context, uint) (PrepareContextJWTRotationResult, error) = (*Service).PrepareContextJWTRotation
	var _ func(*Service, context.Context, uint, string) (ContextJWTStatus, error) = (*Service).ActivateContextJWTRotation
	var _ func(*Service, context.Context, uint, string) (ContextJWTStatus, error) = (*Service).CancelContextJWTRotation
	var _ func(*Service, context.Context, uint) (ContextJWTStatus, error) = (*Service).DisableContextJWT

	service := NewServiceWithRuntime(config.NewRuntime(config.Config{}), &mcpRepositoryStub{}, nil)
	if service.contextJWTNow == nil || service.contextJWTRandom == nil || service.contextJWTNewKeyID == nil {
		t.Fatal("NewServiceWithRuntime() left a context JWT hook uninitialized")
	}

	sentinels := []struct {
		err  error
		text string
	}{
		{ErrMCPContextJWTInvalidPolicy, "invalid mcp context jwt policy"},
		{ErrMCPContextJWTUnavailable, "mcp context jwt unavailable"},
		{ErrMCPContextJWTPendingExists, "mcp context jwt pending rotation exists"},
		{ErrMCPContextJWTRotationConflict, "mcp context jwt rotation conflict"},
		{ErrMCPContextJWTRotationExpired, "mcp context jwt rotation expired"},
		{ErrMCPContextJWTInvalidStorage, "mcp context jwt storage invalid"},
	}
	for _, sentinel := range sentinels {
		if sentinel.err == nil || sentinel.err.Error() != sentinel.text {
			t.Errorf("sentinel error = %v, want %q", sentinel.err, sentinel.text)
		}
	}
}

func TestContextJWTResultShapesExcludeSecrets(t *testing.T) {
	assertContextJWTFields(t, reflect.TypeOf(ContextJWTPolicyInput{}), []string{
		"ExpiresSeconds", "IncludeName", "IncludeEmail", "IncludeRole",
	})
	assertContextJWTFields(t, reflect.TypeOf(ContextJWTStatus{}), []string{
		"ServerPublicID", "Mode", "Configured", "Issuer", "Audience", "KeyID",
		"ExpiresSeconds", "IncludeName", "IncludeEmail", "IncludeRole",
		"PendingKeyID", "PendingExpiresAt",
	})
	assertContextJWTFields(t, reflect.TypeOf(PrepareContextJWTRotationResult{}), []string{
		"ServerPublicID", "TemplateToken", "RecommendedHeader", "Algorithm", "Secret", "Issuer", "Audience",
		"KeyID", "ExpiresSeconds",
	})

	statusType := reflect.TypeOf(ContextJWTStatus{})
	for index := 0; index < statusType.NumField(); index++ {
		if strings.Contains(strings.ToLower(statusType.Field(index).Name), "secret") {
			t.Fatalf("ContextJWTStatus unexpectedly exposes field %q", statusType.Field(index).Name)
		}
	}
	prepareType := reflect.TypeOf(PrepareContextJWTRotationResult{})
	if field, ok := prepareType.FieldByName("Secret"); !ok || field.Type.Kind() != reflect.String {
		t.Fatal("PrepareContextJWTRotationResult must be the only result carrying a string Secret")
	}
}

func TestContextJWTDescribeServer(t *testing.T) {
	server := newContextJWTTestServer()
	server.ContextJWTMode = "future-mode"
	server.ContextJWTSecretEnc = "v1:current-ciphertext-leak-marker"
	server.ContextJWTKeyID = "ctx_current"
	server.ContextJWTIncludeName = true
	server.ContextJWTIncludeEmail = true
	server.ContextJWTIncludeRole = true
	server.ContextJWTPendingSecretEnc = "v1:pending-ciphertext-leak-marker"
	server.ContextJWTPendingKeyID = "ctx_pending"
	pendingCreatedAt := contextJWTTestNow.Add(-time.Hour)
	pendingExpiresAt := contextJWTTestNow.Add(time.Hour)
	server.ContextJWTPendingCreatedAt = &pendingCreatedAt
	server.ContextJWTPendingExpiresAt = &pendingExpiresAt

	repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
	service, _ := newContextJWTTestService(repo)
	cfg := service.cfg.Snapshot()
	cfg.DataEncryptionKey = "intentionally-wrong-key"
	service.cfg.Store(cfg)

	view := service.DescribeServer(*server)
	if !reflect.DeepEqual(view.Server, *server) {
		t.Fatalf("DescribeServer() server = %#v, want original row", view.Server)
	}
	if view.ContextJWT.Mode != "none" || view.ContextJWT.Configured ||
		view.ContextJWT.ServerPublicID != server.PublicID ||
		view.ContextJWT.Issuer != "https://chat.example.com" ||
		view.ContextJWT.PendingKeyID != "ctx_pending" ||
		view.ContextJWT.PendingExpiresAt == nil ||
		!view.ContextJWT.PendingExpiresAt.Equal(pendingExpiresAt) {
		t.Fatalf("DescribeServer() status = %#v", view.ContextJWT)
	}
	if repo.getCalls != 0 || repo.policyCalls != 0 || repo.prepareCalls != 0 ||
		repo.activateCalls != 0 || repo.cancelCalls != 0 || repo.disableCalls != 0 {
		t.Fatalf("DescribeServer() unexpectedly used repository: %#v", repo.operations)
	}

	service.contextJWTNow = func() time.Time { panic("clock-leak-marker") }
	view = service.DescribeServer(*server)
	if view.ContextJWT.PendingKeyID != "" || view.ContextJWT.PendingExpiresAt != nil {
		t.Fatalf("DescribeServer() exposed pending state when clock failed: %#v", view.ContextJWT)
	}
}

func TestContextJWTListServersClearsExpiredPendingOnce(t *testing.T) {
	expired := *newContextJWTTestServer()
	expired.ID = 7
	expired.ContextJWTPendingSecretEnc = "v1:expired-pending-ciphertext-leak-marker"
	expired.ContextJWTPendingKeyID = "ctx_expired"
	expiredAt := contextJWTTestNow.Add(-time.Minute)
	expired.ContextJWTPendingExpiresAt = &expiredAt

	active := *newContextJWTTestServer()
	active.ID = 8
	active.PublicID = "mcp_active_public"
	active.ContextJWTAudience = "urn:deeix:mcp:mcp_active_public"
	active.ContextJWTMode = "hs256"
	active.ContextJWTSecretEnc = "v1:current-ciphertext-leak-marker"
	active.ContextJWTKeyID = "ctx_current"

	repo := &contextJWTRepositoryFake{
		mcpRepositoryStub: mcpRepositoryStub{server: &expired},
		listServers:       []domainmcp.Server{expired, active},
	}
	service, _ := newContextJWTTestService(repo)

	items, err := service.ListServers(t.Context())
	if err != nil {
		t.Fatalf("ListServers() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("ListServers() len = %d, want 2", len(items))
	}
	if !reflect.DeepEqual(repo.operations, []string{"clear", "list"}) ||
		repo.clearCalls != 1 || repo.listCalls != 1 || !repo.clearNow.Equal(contextJWTTestNow) {
		t.Fatalf("repository operations/calls/now = %v/%d/%d/%v", repo.operations, repo.clearCalls, repo.listCalls, repo.clearNow)
	}
	if repo.getCalls != 0 || repo.policyCalls != 0 || repo.prepareCalls != 0 ||
		repo.activateCalls != 0 || repo.cancelCalls != 0 || repo.disableCalls != 0 {
		t.Fatalf("ListServers() performed per-row work: %#v", repo.operations)
	}
	if items[0].ContextJWTPendingSecretEnc != "" || items[0].ContextJWTPendingKeyID != "" ||
		items[0].ContextJWTPendingExpiresAt != nil {
		t.Fatalf("ListServers() returned stale expired pending state: %#v", items[0])
	}
}

func TestContextJWTPrepareReturnsTemplateContractAndStoresCiphertext(t *testing.T) {
	repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: newContextJWTTestServer()}}
	service, random := newContextJWTTestService(repo)

	result, err := service.PrepareContextJWTRotation(t.Context(), repo.server.ID)
	if err != nil {
		t.Fatalf("PrepareContextJWTRotation() error = %v", err)
	}
	wantSecret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
	if result.Secret != wantSecret {
		t.Fatalf("Secret = %q, want deterministic RawURL value", result.Secret)
	}
	if result.ServerPublicID != repo.server.PublicID || result.TemplateToken != "{{DEEIX_SIGNED_CONTEXT}}" ||
		result.RecommendedHeader != "X-MCP-CLIENT-SIGNED-CONTEXT" ||
		result.Algorithm != "HS256" || result.Issuer != "https://chat.example.com" ||
		result.Audience != repo.server.ContextJWTAudience || result.KeyID != contextJWTTestKeyID ||
		result.ExpiresSeconds != repo.server.ContextJWTExpiresSeconds {
		t.Fatalf("unexpected prepare result: %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	if strings.Count(string(encoded), wantSecret) != 1 {
		t.Fatalf("prepare result contains plaintext secret %d times, want exactly once", strings.Count(string(encoded), wantSecret))
	}
	if repo.prepareCalls != 1 || repo.prepareInput.ServerID != repo.server.ID ||
		repo.prepareInput.PendingKeyID != contextJWTTestKeyID ||
		!repo.prepareInput.CreatedAt.Equal(contextJWTTestNow) ||
		!repo.prepareInput.ExpiresAt.Equal(contextJWTTestNow.Add(24*time.Hour)) {
		t.Fatalf("unexpected repository prepare input: %#v", repo.prepareInput)
	}
	if !strings.HasPrefix(repo.prepareInput.PendingSecretEnc, "v1:") ||
		repo.prepareInput.PendingSecretEnc == wantSecret ||
		repo.server.ContextJWTPendingSecretEnc != repo.prepareInput.PendingSecretEnc {
		t.Fatalf("repository did not receive only v1 ciphertext: %#v", repo.prepareInput)
	}
	decrypted, err := secretbox.DecryptString(contextJWTTestDataKey, repo.prepareInput.PendingSecretEnc)
	if err != nil || decrypted != wantSecret {
		t.Fatalf("stored ciphertext decrypt = %q, %v; want generated secret", decrypted, err)
	}
	if random.offset != 32 || !reflect.DeepEqual(random.requests, []int{32}) {
		t.Fatalf("random reader offset/requests = %d/%v, want exactly one 32-byte read", random.offset, random.requests)
	}
}

func TestContextJWTStatusNormalizesModeAndConfiguration(t *testing.T) {
	base := *newContextJWTTestServer()
	base.ContextJWTMode = "hs256"
	base.ContextJWTSecretEnc = "v1:current-ciphertext"
	base.ContextJWTKeyID = "ctx_current"

	tests := []struct {
		name       string
		mutate     func(*domainmcp.Server)
		wantMode   string
		configured bool
	}{
		{name: "configured", mutate: func(*domainmcp.Server) {}, wantMode: "hs256", configured: true},
		{name: "blank current ciphertext", mutate: func(server *domainmcp.Server) { server.ContextJWTSecretEnc = " \t" }, wantMode: "hs256"},
		{name: "blank current key", mutate: func(server *domainmcp.Server) { server.ContextJWTKeyID = " " }, wantMode: "hs256"},
		{name: "blank audience", mutate: func(server *domainmcp.Server) { server.ContextJWTAudience = "\n" }, wantMode: "hs256"},
		{name: "ttl too short", mutate: func(server *domainmcp.Server) { server.ContextJWTExpiresSeconds = 59 }, wantMode: "hs256"},
		{name: "ttl too long", mutate: func(server *domainmcp.Server) { server.ContextJWTExpiresSeconds = 901 }, wantMode: "hs256"},
		{name: "disabled", mutate: func(server *domainmcp.Server) { server.ContextJWTMode = "none" }, wantMode: "none"},
		{name: "unknown mode", mutate: func(server *domainmcp.Server) { server.ContextJWTMode = "future" }, wantMode: "none"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := base
			test.mutate(&server)
			status := buildContextJWTStatus(server, "https://chat.example.com", contextJWTTestNow)
			if status.Mode != test.wantMode || status.Configured != test.configured {
				t.Fatalf("status mode/configured = %q/%v, want %q/%v", status.Mode, status.Configured, test.wantMode, test.configured)
			}
			if status.ServerPublicID != server.PublicID || status.Issuer != "https://chat.example.com" ||
				status.Audience != server.ContextJWTAudience || status.KeyID != server.ContextJWTKeyID ||
				status.ExpiresSeconds != server.ContextJWTExpiresSeconds {
				t.Fatalf("status did not preserve public policy fields: %#v", status)
			}
		})
	}
}

func TestContextJWTPendingStatusIncludesOnlyUnexpiredKey(t *testing.T) {
	server := *newContextJWTTestServer()
	server.ContextJWTPendingSecretEnc = "v1:pending-leak-marker"
	server.ContextJWTPendingKeyID = "ctx_pending"

	for _, test := range []struct {
		name        string
		expiresAt   time.Time
		wantKey     string
		wantExpires bool
	}{
		{name: "future", expiresAt: contextJWTTestNow.Add(time.Minute), wantKey: "ctx_pending", wantExpires: true},
		{name: "equal is expired", expiresAt: contextJWTTestNow},
		{name: "past", expiresAt: contextJWTTestNow.Add(-time.Nanosecond)},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := server
			item.ContextJWTPendingExpiresAt = &test.expiresAt
			status := buildContextJWTStatus(item, "https://chat.example.com", contextJWTTestNow)
			if status.PendingKeyID != test.wantKey || (status.PendingExpiresAt != nil) != test.wantExpires {
				t.Fatalf("pending status = %q/%v, want %q/%v", status.PendingKeyID, status.PendingExpiresAt, test.wantKey, test.wantExpires)
			}
			if status.Configured {
				t.Fatal("pending-only server must never be configured")
			}
		})
	}
}

func TestContextJWTIssuerNormalization(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		env     string
		want    string
		wantErr bool
	}{
		{name: "development http", raw: " http://localhost:8080/ ", env: "dev", want: "http://localhost:8080"},
		{name: "trim trailing slashes", raw: " https://chat.example.com/// ", env: "dev", want: "https://chat.example.com"},
		{name: "production https", raw: "https://chat.example.com/", env: "prod", want: "https://chat.example.com"},
		{name: "production alias https", raw: "https://chat.example.com/", env: " production ", want: "https://chat.example.com"},
		{name: "relative", raw: "/chat", env: "dev", wantErr: true},
		{name: "hostless", raw: "https:///chat", env: "dev", wantErr: true},
		{name: "development port-only authority", raw: "http://:8080", env: "dev", wantErr: true},
		{name: "production port-only authority", raw: "https://:443", env: "prod", wantErr: true},
		{name: "non http scheme", raw: "ftp://chat.example.com", env: "dev", wantErr: true},
		{name: "userinfo", raw: "https://user:pass@chat.example.com", env: "dev", wantErr: true},
		{name: "query", raw: "https://chat.example.com?tenant=one", env: "dev", wantErr: true},
		{name: "empty query delimiter", raw: "https://chat.example.com?", env: "dev", wantErr: true},
		{name: "fragment", raw: "https://chat.example.com#part", env: "dev", wantErr: true},
		{name: "empty fragment delimiter", raw: "https://chat.example.com#", env: "dev", wantErr: true},
		{name: "prod http", raw: "http://chat.example.com", env: "prod", wantErr: true},
		{name: "production alias http", raw: "http://chat.example.com", env: "production", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeContextJWTIssuer(test.raw, test.env)
			if test.wantErr {
				if !errors.Is(err, ErrMCPContextJWTUnavailable) || got != "" {
					t.Fatalf("normalizeContextJWTIssuer() = %q, %v; want unavailable", got, err)
				}
				if strings.Contains(err.Error(), test.raw) {
					t.Fatalf("issuer error leaked input %q: %v", test.raw, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("normalizeContextJWTIssuer() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestContextJWTPolicyAndRuntimeValidation(t *testing.T) {
	t.Run("policy delegates all fields", func(t *testing.T) {
		repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: newContextJWTTestServer()}}
		service, _ := newContextJWTTestService(repo)
		input := ContextJWTPolicyInput{
			ExpiresSeconds: 600,
			IncludeName:    true,
			IncludeEmail:   false,
			IncludeRole:    true,
		}
		status, err := service.UpdateContextJWTPolicy(t.Context(), repo.server.ID, input)
		if err != nil {
			t.Fatalf("UpdateContextJWTPolicy() error = %v", err)
		}
		wantInput := repository.UpdateMCPContextJWTPolicyInput{
			ExpiresSeconds: input.ExpiresSeconds,
			IncludeName:    input.IncludeName,
			IncludeEmail:   input.IncludeEmail,
			IncludeRole:    input.IncludeRole,
		}
		if repo.policyCalls != 1 || repo.policyServerID != repo.server.ID || repo.policyInput != wantInput {
			t.Fatalf("repository policy call = %d/%d/%#v, want 1/%d/%#v", repo.policyCalls, repo.policyServerID, repo.policyInput, repo.server.ID, wantInput)
		}
		if status.ExpiresSeconds != input.ExpiresSeconds || !status.IncludeName || status.IncludeEmail || !status.IncludeRole ||
			status.Issuer != "https://chat.example.com" {
			t.Fatalf("policy status = %#v", status)
		}
	})

	for _, ttl := range []int{0, 59, 901} {
		t.Run("reject invalid ttl", func(t *testing.T) {
			repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: newContextJWTTestServer()}}
			service, _ := newContextJWTTestService(repo)
			_, err := service.UpdateContextJWTPolicy(t.Context(), repo.server.ID, ContextJWTPolicyInput{ExpiresSeconds: ttl})
			if !errors.Is(err, ErrMCPContextJWTInvalidPolicy) || repo.policyCalls != 0 {
				t.Fatalf("UpdateContextJWTPolicy(ttl=%d) error/calls = %v/%d, want invalid policy/0", ttl, err, repo.policyCalls)
			}
		})
	}

	t.Run("prepare rejects invalid stored ttl", func(t *testing.T) {
		server := newContextJWTTestServer()
		server.ContextJWTExpiresSeconds = 59
		repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
		service, _ := newContextJWTTestService(repo)
		_, err := service.PrepareContextJWTRotation(t.Context(), server.ID)
		if !errors.Is(err, ErrMCPContextJWTInvalidPolicy) || repo.prepareCalls != 0 {
			t.Fatalf("PrepareContextJWTRotation() error/calls = %v/%d, want invalid policy/0", err, repo.prepareCalls)
		}
	})

	for _, ttl := range []int{59, 901} {
		t.Run("activate rejects invalid stored ttl after secret validation", func(t *testing.T) {
			secret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x34}, 32))
			server := newContextJWTTestPendingServer(t, secret)
			server.ContextJWTExpiresSeconds = ttl
			repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
			service, _ := newContextJWTTestService(repo)
			_, err := service.ActivateContextJWTRotation(t.Context(), server.ID, contextJWTTestKeyID)
			if !errors.Is(err, ErrMCPContextJWTInvalidPolicy) || repo.getCalls != 1 || repo.activateCalls != 0 {
				t.Fatalf("ActivateContextJWTRotation(ttl=%d) error/get/activate = %v/%d/%d, want invalid policy/1/0", ttl, err, repo.getCalls, repo.activateCalls)
			}
		})
	}

	t.Run("prepare rejects blank stored audience", func(t *testing.T) {
		server := newContextJWTTestServer()
		server.ContextJWTAudience = " \t"
		repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
		service, _ := newContextJWTTestService(repo)
		_, err := service.PrepareContextJWTRotation(t.Context(), server.ID)
		if !errors.Is(err, ErrMCPContextJWTInvalidStorage) || repo.prepareCalls != 0 {
			t.Fatalf("PrepareContextJWTRotation() error/calls = %v/%d, want invalid storage/0", err, repo.prepareCalls)
		}
	})

	t.Run("blank issuer rejects prepare and activate", func(t *testing.T) {
		secret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x35}, 32))
		server := newContextJWTTestPendingServer(t, secret)
		repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
		service, _ := newContextJWTTestService(repo)
		cfg := service.cfg.Snapshot()
		cfg.PublicWebBaseURL = " \t"
		service.cfg.Store(cfg)

		if _, err := service.PrepareContextJWTRotation(t.Context(), server.ID); !errors.Is(err, ErrMCPContextJWTUnavailable) {
			t.Fatalf("PrepareContextJWTRotation() error = %v, want unavailable", err)
		}
		if _, err := service.ActivateContextJWTRotation(t.Context(), server.ID, contextJWTTestKeyID); !errors.Is(err, ErrMCPContextJWTUnavailable) {
			t.Fatalf("ActivateContextJWTRotation() error = %v, want unavailable", err)
		}
		if repo.prepareCalls != 0 || repo.getCalls != 1 || repo.activateCalls != 0 {
			t.Fatalf("blank issuer repository calls prepare/get/activate = %d/%d/%d, want 0/1/0", repo.prepareCalls, repo.getCalls, repo.activateCalls)
		}
	})
}

func TestContextJWTActivateValidatesPendingBeforeCAS(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x36}, 32))
	server := newContextJWTTestPendingServer(t, secret)
	pendingCiphertext := server.ContextJWTPendingSecretEnc
	repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
	service, _ := newContextJWTTestService(repo)

	status, err := service.ActivateContextJWTRotation(t.Context(), server.ID, "  "+contextJWTTestKeyID+"  ")
	if err != nil {
		t.Fatalf("ActivateContextJWTRotation() error = %v", err)
	}
	if !reflect.DeepEqual(repo.operations, []string{"get", "activate"}) || repo.getCalls != 1 || repo.activateCalls != 1 {
		t.Fatalf("repository operations = %v, want get then activate", repo.operations)
	}
	if repo.activateID != server.ID || repo.activateKeyID != contextJWTTestKeyID || !repo.activateNow.Equal(contextJWTTestNow) {
		t.Fatalf("activate arguments = %d/%q/%v", repo.activateID, repo.activateKeyID, repo.activateNow)
	}
	if status.Mode != "hs256" || !status.Configured || status.KeyID != contextJWTTestKeyID ||
		status.PendingKeyID != "" || status.PendingExpiresAt != nil {
		t.Fatalf("activated status = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("json.Marshal(status) error = %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte(pendingCiphertext)) {
		t.Fatal("activated status leaked plaintext or ciphertext")
	}
}

func TestContextJWTActivateRequiresExactPendingKey(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x37}, 32))
	tests := []struct {
		name      string
		storedKey string
		inputKey  string
		wantGets  int
		wantErr   error
	}{
		{name: "blank input", storedKey: contextJWTTestKeyID, inputKey: " \t", wantErr: ErrMCPContextJWTInvalidPolicy},
		{name: "different input", storedKey: contextJWTTestKeyID, inputKey: "ctx_other", wantGets: 1, wantErr: ErrMCPContextJWTRotationConflict},
		{name: "blank stored", storedKey: "", inputKey: contextJWTTestKeyID, wantGets: 1, wantErr: ErrMCPContextJWTRotationConflict},
		{name: "stored whitespace is not trimmed", storedKey: " " + contextJWTTestKeyID, inputKey: contextJWTTestKeyID, wantGets: 1, wantErr: ErrMCPContextJWTRotationConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newContextJWTTestPendingServer(t, secret)
			server.ContextJWTPendingKeyID = test.storedKey
			repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
			service, _ := newContextJWTTestService(repo)
			_, err := service.ActivateContextJWTRotation(t.Context(), server.ID, test.inputKey)
			if !errors.Is(err, test.wantErr) || repo.getCalls != test.wantGets || repo.activateCalls != 0 {
				t.Fatalf("ActivateContextJWTRotation() error/get/activate = %v/%d/%d, want %v/%d/0", err, repo.getCalls, repo.activateCalls, test.wantErr, test.wantGets)
			}
		})
	}
}

func TestContextJWTActivateRejectsInvalidStoredSecretsWithoutCAS(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x38}, 32))
	invalid31 := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x39}, 31))
	tests := []struct {
		name       string
		ciphertext string
		plaintext  string
	}{
		{name: "blank ciphertext", ciphertext: ""},
		{name: "corrupted ciphertext", ciphertext: "v1:corrupted-ciphertext-leak-marker"},
		{name: "weak plaintext", plaintext: "weak-secret-leak-marker"},
		{name: "padded raw url", plaintext: valid + "="},
		{name: "raw url decodes to 31 bytes", plaintext: invalid31},
		{name: "non canonical alphabet", plaintext: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ciphertext := test.ciphertext
			if ciphertext == "" && test.plaintext != "" {
				var err error
				ciphertext, err = secretbox.EncryptString(contextJWTTestDataKey, test.plaintext)
				if err != nil {
					t.Fatalf("EncryptString() fixture error = %v", err)
				}
			}
			server := newContextJWTTestServer()
			server.ContextJWTSecretEnc = "v1:current-secret-leak-marker"
			server.ContextJWTKeyID = "ctx_current"
			server.ContextJWTPendingSecretEnc = ciphertext
			server.ContextJWTPendingKeyID = contextJWTTestKeyID
			createdAt := contextJWTTestNow.Add(-time.Hour)
			expiresAt := contextJWTTestNow.Add(time.Hour)
			server.ContextJWTPendingCreatedAt = &createdAt
			server.ContextJWTPendingExpiresAt = &expiresAt
			before, cloneErr := cloneContextJWTTestServer(server)
			if cloneErr != nil {
				t.Fatalf("clone fixture error = %v", cloneErr)
			}
			repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
			service, _ := newContextJWTTestService(repo)

			_, err := service.ActivateContextJWTRotation(t.Context(), server.ID, contextJWTTestKeyID)
			if !errors.Is(err, ErrMCPContextJWTInvalidStorage) || repo.getCalls != 1 || repo.activateCalls != 0 {
				t.Fatalf("ActivateContextJWTRotation() error/get/activate = %v/%d/%d, want invalid storage/1/0", err, repo.getCalls, repo.activateCalls)
			}
			if !reflect.DeepEqual(repo.server, before) {
				t.Fatalf("invalid activation mutated server\ngot:  %#v\nwant: %#v", repo.server, before)
			}
			for _, leak := range []string{ciphertext, test.plaintext, server.ContextJWTSecretEnc} {
				if leak != "" && strings.Contains(err.Error(), leak) {
					t.Fatalf("activation error leaked stored value %q: %v", leak, err)
				}
			}
		})
	}
}

func TestContextJWTEmergencyCancelAndDisableIgnoreIssuerAndCiphertext(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		server := newContextJWTTestServer()
		server.ContextJWTMode = "hs256"
		server.ContextJWTSecretEnc = "v1:undecryptable-current-leak-marker"
		server.ContextJWTKeyID = "ctx_current"
		server.ContextJWTPendingSecretEnc = "v1:undecryptable-pending-leak-marker"
		server.ContextJWTPendingKeyID = contextJWTTestKeyID
		createdAt := contextJWTTestNow.Add(-time.Hour)
		expiresAt := contextJWTTestNow.Add(time.Hour)
		server.ContextJWTPendingCreatedAt = &createdAt
		server.ContextJWTPendingExpiresAt = &expiresAt
		repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
		service, _ := newContextJWTTestService(repo)
		cfg := service.cfg.Snapshot()
		cfg.PublicWebBaseURL = "issuer://invalid?leak-marker"
		cfg.DataEncryptionKey = "wrong-data-key"
		service.cfg.Store(cfg)

		status, err := service.CancelContextJWTRotation(t.Context(), server.ID, " "+contextJWTTestKeyID+" ")
		if err != nil {
			t.Fatalf("CancelContextJWTRotation() error = %v", err)
		}
		if repo.cancelCalls != 1 || repo.cancelKeyID != contextJWTTestKeyID ||
			repo.server.ContextJWTPendingSecretEnc != "" || repo.server.ContextJWTPendingKeyID != "" {
			t.Fatalf("cancel did not clear pending material: calls=%d server=%#v", repo.cancelCalls, repo.server)
		}
		if repo.server.ContextJWTSecretEnc != "v1:undecryptable-current-leak-marker" || status.KeyID != "ctx_current" {
			t.Fatalf("cancel unexpectedly changed current key: %#v", repo.server)
		}
		if status.Issuer != "" || strings.Contains(status.Issuer, "leak-marker") {
			t.Fatalf("cancel status exposed invalid configured issuer %q", status.Issuer)
		}
	})

	t.Run("disable", func(t *testing.T) {
		server := newContextJWTTestServer()
		server.ContextJWTMode = "hs256"
		server.ContextJWTSecretEnc = "v1:undecryptable-current-leak-marker"
		server.ContextJWTKeyID = "ctx_current"
		server.ContextJWTIncludeName = true
		server.ContextJWTIncludeEmail = true
		server.ContextJWTIncludeRole = true
		server.ContextJWTPendingSecretEnc = "v1:undecryptable-pending-leak-marker"
		server.ContextJWTPendingKeyID = contextJWTTestKeyID
		createdAt := contextJWTTestNow.Add(-time.Hour)
		expiresAt := contextJWTTestNow.Add(time.Hour)
		server.ContextJWTPendingCreatedAt = &createdAt
		server.ContextJWTPendingExpiresAt = &expiresAt
		repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
		service, _ := newContextJWTTestService(repo)
		cfg := service.cfg.Snapshot()
		cfg.PublicWebBaseURL = "issuer://invalid#leak-marker"
		cfg.DataEncryptionKey = "wrong-data-key"
		service.cfg.Store(cfg)

		status, err := service.DisableContextJWT(t.Context(), server.ID)
		if err != nil {
			t.Fatalf("DisableContextJWT() error = %v", err)
		}
		if repo.disableCalls != 1 || status.Mode != "none" || status.Configured ||
			repo.server.ContextJWTSecretEnc != "" || repo.server.ContextJWTKeyID != "" ||
			repo.server.ContextJWTPendingSecretEnc != "" || repo.server.ContextJWTPendingKeyID != "" ||
			repo.server.ContextJWTIncludeName || repo.server.ContextJWTIncludeEmail || repo.server.ContextJWTIncludeRole {
			t.Fatalf("disable did not clear all governed key material: status=%#v server=%#v", status, repo.server)
		}
		if status.Issuer != "" || strings.Contains(status.Issuer, "leak-marker") {
			t.Fatalf("disable status exposed invalid configured issuer %q", status.Issuer)
		}
	})

	t.Run("cancel rejects blank key without repository call", func(t *testing.T) {
		repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: newContextJWTTestServer()}}
		service, _ := newContextJWTTestService(repo)
		_, err := service.CancelContextJWTRotation(t.Context(), repo.server.ID, " \r\n")
		if !errors.Is(err, ErrMCPContextJWTInvalidPolicy) || repo.cancelCalls != 0 {
			t.Fatalf("CancelContextJWTRotation() error/calls = %v/%d, want invalid policy/0", err, repo.cancelCalls)
		}
	})
}

func TestContextJWTRepositoryErrorMapping(t *testing.T) {
	unknown := errors.New("repository error leak marker")
	tests := []struct {
		name string
		in   error
		want error
	}{
		{name: "nil", in: nil, want: nil},
		{name: "not found", in: repository.ErrNotFound, want: ErrMCPServerNotFound},
		{name: "pending exists", in: repository.ErrMCPContextJWTPendingExists, want: ErrMCPContextJWTPendingExists},
		{name: "rotation conflict", in: repository.ErrMCPContextJWTRotationConflict, want: ErrMCPContextJWTRotationConflict},
		{name: "pending expired", in: repository.ErrMCPContextJWTPendingExpired, want: ErrMCPContextJWTRotationExpired},
		{name: "unknown", in: unknown, want: unknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mapContextJWTRepositoryError(test.in)
			if test.want == nil {
				if got != nil {
					t.Fatalf("mapContextJWTRepositoryError(nil) = %v", got)
				}
				return
			}
			if !errors.Is(got, test.want) {
				t.Fatalf("mapContextJWTRepositoryError(%v) = %v, want %v", test.in, got, test.want)
			}
		})
	}
}

func TestContextJWTRepositoryErrorsAreMappedByMethods(t *testing.T) {
	validSecret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x45}, 32))
	tests := []struct {
		name      string
		configure func(*contextJWTRepositoryFake)
		call      func(*Service, uint) error
		want      error
	}{
		{
			name:      "update not found",
			configure: func(repo *contextJWTRepositoryFake) { repo.policyErr = repository.ErrNotFound },
			call: func(service *Service, id uint) error {
				_, err := service.UpdateContextJWTPolicy(t.Context(), id, ContextJWTPolicyInput{ExpiresSeconds: 300})
				return err
			},
			want: ErrMCPServerNotFound,
		},
		{
			name:      "prepare pending exists",
			configure: func(repo *contextJWTRepositoryFake) { repo.prepareErr = repository.ErrMCPContextJWTPendingExists },
			call: func(service *Service, id uint) error {
				_, err := service.PrepareContextJWTRotation(t.Context(), id)
				return err
			},
			want: ErrMCPContextJWTPendingExists,
		},
		{
			name:      "activate expired",
			configure: func(repo *contextJWTRepositoryFake) { repo.activateErr = repository.ErrMCPContextJWTPendingExpired },
			call: func(service *Service, id uint) error {
				_, err := service.ActivateContextJWTRotation(t.Context(), id, contextJWTTestKeyID)
				return err
			},
			want: ErrMCPContextJWTRotationExpired,
		},
		{
			name:      "cancel conflict",
			configure: func(repo *contextJWTRepositoryFake) { repo.cancelErr = repository.ErrMCPContextJWTRotationConflict },
			call: func(service *Service, id uint) error {
				_, err := service.CancelContextJWTRotation(t.Context(), id, contextJWTTestKeyID)
				return err
			},
			want: ErrMCPContextJWTRotationConflict,
		},
		{
			name:      "disable not found",
			configure: func(repo *contextJWTRepositoryFake) { repo.disableErr = repository.ErrNotFound },
			call: func(service *Service, id uint) error {
				_, err := service.DisableContextJWT(t.Context(), id)
				return err
			},
			want: ErrMCPServerNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newContextJWTTestPendingServer(t, validSecret)
			repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
			test.configure(repo)
			service, _ := newContextJWTTestService(repo)
			if err := test.call(service, server.ID); !errors.Is(err, test.want) {
				t.Fatalf("method error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestContextJWTHooksFailClosedWithoutPanics(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Service)
	}{
		{name: "nil clock", mutate: func(service *Service) { service.contextJWTNow = nil }},
		{name: "panicking clock", mutate: func(service *Service) { service.contextJWTNow = func() time.Time { panic("clock leak marker") } }},
		{name: "zero clock", mutate: func(service *Service) { service.contextJWTNow = func() time.Time { return time.Time{} } }},
		{name: "nil random reader", mutate: func(service *Service) { service.contextJWTRandom = nil }},
		{name: "error random reader", mutate: func(service *Service) {
			service.contextJWTRandom = contextJWTErrorReader{err: errors.New("random leak marker")}
		}},
		{name: "short random reader", mutate: func(service *Service) { service.contextJWTRandom = bytes.NewReader([]byte{1, 2, 3}) }},
		{name: "panicking random reader", mutate: func(service *Service) { service.contextJWTRandom = contextJWTPanicReader{} }},
		{name: "nil key id hook", mutate: func(service *Service) { service.contextJWTNewKeyID = nil }},
		{name: "panicking key id hook", mutate: func(service *Service) { service.contextJWTNewKeyID = func() string { panic("kid leak marker") } }},
		{name: "blank key id", mutate: func(service *Service) { service.contextJWTNewKeyID = func() string { return " \t" } }},
		{name: "oversized key id", mutate: func(service *Service) { service.contextJWTNewKeyID = func() string { return strings.Repeat("k", 65) } }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: newContextJWTTestServer()}}
			service, _ := newContextJWTTestService(repo)
			test.mutate(service)
			_, err := callContextJWTPrepareWithoutPanic(t, service, repo.server.ID)
			if !errors.Is(err, ErrMCPContextJWTUnavailable) || repo.prepareCalls != 0 {
				t.Fatalf("PrepareContextJWTRotation() error/calls = %v/%d, want unavailable/0", err, repo.prepareCalls)
			}
			for _, leak := range []string{"clock leak marker", "random leak marker", "kid leak marker"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("hook error leaked internal value %q: %v", leak, err)
				}
			}
		})
	}
}

func TestContextJWTActivateClockHookFailsBeforeCAS(t *testing.T) {
	validSecret := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x46}, 32))
	for _, test := range []struct {
		name string
		now  func() time.Time
	}{
		{name: "nil clock", now: nil},
		{name: "panicking clock", now: func() time.Time { panic("clock leak marker") }},
		{name: "zero clock", now: func() time.Time { return time.Time{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newContextJWTTestPendingServer(t, validSecret)
			repo := &contextJWTRepositoryFake{mcpRepositoryStub: mcpRepositoryStub{server: server}}
			service, _ := newContextJWTTestService(repo)
			service.contextJWTNow = test.now
			_, err := callContextJWTActivateWithoutPanic(t, service, server.ID, contextJWTTestKeyID)
			if !errors.Is(err, ErrMCPContextJWTUnavailable) || repo.activateCalls != 0 {
				t.Fatalf("ActivateContextJWTRotation() error/calls = %v/%d, want unavailable/0", err, repo.activateCalls)
			}
		})
	}
}

func callContextJWTPrepareWithoutPanic(t *testing.T, service *Service, serverID uint) (result PrepareContextJWTRotationResult, err error) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("PrepareContextJWTRotation() panicked: %v", recovered)
		}
	}()
	return service.PrepareContextJWTRotation(t.Context(), serverID)
}

func callContextJWTActivateWithoutPanic(t *testing.T, service *Service, serverID uint, kid string) (status ContextJWTStatus, err error) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("ActivateContextJWTRotation() panicked: %v", recovered)
		}
	}()
	return service.ActivateContextJWTRotation(t.Context(), serverID, kid)
}

func newContextJWTTestPendingServer(t *testing.T, plaintext string) *domainmcp.Server {
	t.Helper()
	ciphertext, err := secretbox.EncryptString(contextJWTTestDataKey, plaintext)
	if err != nil {
		t.Fatalf("EncryptString() fixture error = %v", err)
	}
	server := newContextJWTTestServer()
	server.ContextJWTPendingSecretEnc = ciphertext
	server.ContextJWTPendingKeyID = contextJWTTestKeyID
	createdAt := contextJWTTestNow.Add(-time.Hour)
	expiresAt := contextJWTTestNow.Add(time.Hour)
	server.ContextJWTPendingCreatedAt = &createdAt
	server.ContextJWTPendingExpiresAt = &expiresAt
	return server
}

func assertContextJWTFields(t *testing.T, target reflect.Type, names []string) {
	t.Helper()
	if target.NumField() != len(names) {
		t.Fatalf("%s field count = %d, want %d", target.Name(), target.NumField(), len(names))
	}
	for index, name := range names {
		if target.Field(index).Name != name {
			t.Fatalf("%s field %d = %q, want %q", target.Name(), index, target.Field(index).Name, name)
		}
	}
}

type contextJWTRepositoryFake struct {
	mcpRepositoryStub

	operations    []string
	getCalls      int
	listCalls     int
	policyCalls   int
	prepareCalls  int
	activateCalls int
	cancelCalls   int
	disableCalls  int
	clearCalls    int

	getErr      error
	policyErr   error
	prepareErr  error
	activateErr error
	cancelErr   error
	disableErr  error
	clearErr    error
	listErr     error
	listServers []domainmcp.Server

	policyServerID uint
	policyInput    repository.UpdateMCPContextJWTPolicyInput
	prepareInput   repository.PrepareMCPContextJWTRotationInput
	activateID     uint
	activateKeyID  string
	activateNow    time.Time
	cancelID       uint
	cancelKeyID    string
	disableID      uint
	clearNow       time.Time
}

func (r *contextJWTRepositoryFake) ListServers(context.Context) ([]domainmcp.Server, error) {
	r.operations = append(r.operations, "list")
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	items := make([]domainmcp.Server, len(r.listServers))
	copy(items, r.listServers)
	if r.clearCalls > 0 {
		for index := range items {
			if items[index].ContextJWTPendingExpiresAt != nil &&
				!items[index].ContextJWTPendingExpiresAt.After(r.clearNow) {
				clearContextJWTTestPending(&items[index])
			}
		}
	}
	return items, nil
}

func (r *contextJWTRepositoryFake) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	r.operations = append(r.operations, "get")
	r.getCalls++
	if r.getErr != nil {
		return nil, r.getErr
	}
	return cloneContextJWTTestServer(r.server)
}

func (r *contextJWTRepositoryFake) UpdateContextJWTPolicy(
	_ context.Context,
	serverID uint,
	input repository.UpdateMCPContextJWTPolicyInput,
) (*domainmcp.Server, error) {
	r.operations = append(r.operations, "policy")
	r.policyCalls++
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
	return cloneContextJWTTestServer(r.server)
}

func (r *contextJWTRepositoryFake) PrepareContextJWTRotation(
	_ context.Context,
	input repository.PrepareMCPContextJWTRotationInput,
) (*domainmcp.Server, error) {
	r.operations = append(r.operations, "prepare")
	r.prepareCalls++
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
	return cloneContextJWTTestServer(r.server)
}

func (r *contextJWTRepositoryFake) ActivateContextJWTRotation(
	_ context.Context,
	serverID uint,
	kid string,
	now time.Time,
) (*domainmcp.Server, error) {
	r.operations = append(r.operations, "activate")
	r.activateCalls++
	r.activateID = serverID
	r.activateKeyID = kid
	r.activateNow = now
	if r.activateErr != nil {
		return nil, r.activateErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	r.server.ContextJWTMode = "hs256"
	r.server.ContextJWTSecretEnc = r.server.ContextJWTPendingSecretEnc
	r.server.ContextJWTKeyID = r.server.ContextJWTPendingKeyID
	clearContextJWTTestPending(r.server)
	return cloneContextJWTTestServer(r.server)
}

func (r *contextJWTRepositoryFake) CancelContextJWTRotation(
	_ context.Context,
	serverID uint,
	kid string,
) (*domainmcp.Server, error) {
	r.operations = append(r.operations, "cancel")
	r.cancelCalls++
	r.cancelID = serverID
	r.cancelKeyID = kid
	if r.cancelErr != nil {
		return nil, r.cancelErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	clearContextJWTTestPending(r.server)
	return cloneContextJWTTestServer(r.server)
}

func (r *contextJWTRepositoryFake) DisableContextJWT(
	_ context.Context,
	serverID uint,
) (*domainmcp.Server, error) {
	r.operations = append(r.operations, "disable")
	r.disableCalls++
	r.disableID = serverID
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
	clearContextJWTTestPending(r.server)
	return cloneContextJWTTestServer(r.server)
}

func (r *contextJWTRepositoryFake) ClearExpiredContextJWTPending(
	_ context.Context,
	now time.Time,
) error {
	r.operations = append(r.operations, "clear")
	r.clearCalls++
	r.clearNow = now
	if r.clearErr != nil {
		return r.clearErr
	}
	if r.server != nil && r.server.ContextJWTPendingExpiresAt != nil &&
		!r.server.ContextJWTPendingExpiresAt.After(now) {
		clearContextJWTTestPending(r.server)
	}
	return nil
}

func newContextJWTTestService(repo repository.MCPRepository) (*Service, *contextJWTExactReader) {
	random := &contextJWTExactReader{data: bytes.Repeat([]byte{0x5a}, 32)}
	service := NewServiceWithRuntime(config.NewRuntime(config.Config{
		Env:               "dev",
		PublicWebBaseURL:  " https://chat.example.com/// ",
		DataEncryptionKey: contextJWTTestDataKey,
	}), repo, nil)
	service.contextJWTNow = func() time.Time { return contextJWTTestNow }
	service.contextJWTRandom = random
	service.contextJWTNewKeyID = func() string { return contextJWTTestKeyID }
	return service, random
}

func newContextJWTTestServer() *domainmcp.Server {
	return &domainmcp.Server{
		ID:                       7,
		PublicID:                 "mcp_test_public",
		ContextJWTMode:           "none",
		ContextJWTAudience:       "urn:deeix:mcp:mcp_test_public",
		ContextJWTExpiresSeconds: 300,
	}
}

func cloneContextJWTTestServer(server *domainmcp.Server) (*domainmcp.Server, error) {
	if server == nil {
		return nil, repository.ErrNotFound
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
	return &item, nil
}

func clearContextJWTTestPending(server *domainmcp.Server) {
	server.ContextJWTPendingSecretEnc = ""
	server.ContextJWTPendingKeyID = ""
	server.ContextJWTPendingCreatedAt = nil
	server.ContextJWTPendingExpiresAt = nil
}

type contextJWTExactReader struct {
	data     []byte
	offset   int
	requests []int
}

func (r *contextJWTExactReader) Read(buffer []byte) (int, error) {
	r.requests = append(r.requests, len(buffer))
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	read := copy(buffer, r.data[r.offset:])
	r.offset += read
	if read < len(buffer) {
		return read, io.EOF
	}
	return read, nil
}

type contextJWTErrorReader struct{ err error }

func (r contextJWTErrorReader) Read([]byte) (int, error) { return 0, r.err }

type contextJWTPanicReader struct{}

func (contextJWTPanicReader) Read([]byte) (int, error) { panic("reader leak marker") }

// These compatibility methods close the Task 3 application-test interface debt.
// Task 6 owns the remaining transport/http test-stub compatibility work.
func (r *mcpRepositoryStub) UpdateContextJWTPolicy(
	_ context.Context,
	_ uint,
	input repository.UpdateMCPContextJWTPolicyInput,
) (*domainmcp.Server, error) {
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	item.ContextJWTExpiresSeconds = input.ExpiresSeconds
	item.ContextJWTIncludeName = input.IncludeName
	item.ContextJWTIncludeEmail = input.IncludeEmail
	item.ContextJWTIncludeRole = input.IncludeRole
	return &item, nil
}

func (r *mcpRepositoryStub) PrepareContextJWTRotation(
	_ context.Context,
	input repository.PrepareMCPContextJWTRotationInput,
) (*domainmcp.Server, error) {
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	item.ContextJWTPendingSecretEnc = input.PendingSecretEnc
	item.ContextJWTPendingKeyID = input.PendingKeyID
	item.ContextJWTPendingCreatedAt = &input.CreatedAt
	item.ContextJWTPendingExpiresAt = &input.ExpiresAt
	return &item, nil
}

func (r *mcpRepositoryStub) ActivateContextJWTRotation(
	_ context.Context,
	_ uint,
	_ string,
	_ time.Time,
) (*domainmcp.Server, error) {
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	item.ContextJWTMode = "hs256"
	item.ContextJWTSecretEnc = item.ContextJWTPendingSecretEnc
	item.ContextJWTKeyID = item.ContextJWTPendingKeyID
	item.ContextJWTPendingSecretEnc = ""
	item.ContextJWTPendingKeyID = ""
	item.ContextJWTPendingCreatedAt = nil
	item.ContextJWTPendingExpiresAt = nil
	return &item, nil
}

func (r *mcpRepositoryStub) CancelContextJWTRotation(
	_ context.Context,
	_ uint,
	_ string,
) (*domainmcp.Server, error) {
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	item.ContextJWTPendingSecretEnc = ""
	item.ContextJWTPendingKeyID = ""
	item.ContextJWTPendingCreatedAt = nil
	item.ContextJWTPendingExpiresAt = nil
	return &item, nil
}

func (r *mcpRepositoryStub) DisableContextJWT(context.Context, uint) (*domainmcp.Server, error) {
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	item.ContextJWTMode = "none"
	item.ContextJWTSecretEnc = ""
	item.ContextJWTKeyID = ""
	item.ContextJWTIncludeName = false
	item.ContextJWTIncludeEmail = false
	item.ContextJWTIncludeRole = false
	item.ContextJWTPendingSecretEnc = ""
	item.ContextJWTPendingKeyID = ""
	item.ContextJWTPendingCreatedAt = nil
	item.ContextJWTPendingExpiresAt = nil
	return &item, nil
}

func (*mcpRepositoryStub) ClearExpiredContextJWTPending(context.Context, time.Time) error {
	return nil
}
