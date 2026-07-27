# Identity Provider TLS Verification Override Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a secure-by-default, per-identity-provider option that disables certificate verification only for that provider's backend OAuth2/OIDC protocol requests.

**Architecture:** Persist a Boolean on each identity provider and carry it through the management contract while omitting it from public login options. The auth service owns immutable secure and provider-only insecure HTTP clients, selects one from the loaded provider record, and never changes the secure client used by logos or Turnstile.

**Tech Stack:** Go 1.26, Gin, Gorm, PostgreSQL/SQLite, net/http and crypto/tls, Next.js 16, React 19, TypeScript, Node test runner, pnpm.

## Global Constraints

- The database and API names are `tls_insecure_skip_verify` and `tlsInsecureSkipVerify`.
- Existing and new providers default to secure certificate verification.
- Only discovery, token, user-info, provider-specific supplemental profile requests, and future JWKS requests may use the insecure client.
- Browser authorization navigation, remote logos, Turnstile, and every other outbound integration remain secure.
- The insecure client keeps the existing timeout, SSRF-aware dialer, redirect behavior, and tracing.
- Never log OAuth secrets, authorization codes, access tokens, provider profiles, or TLS configuration values.
- Tests must fail for the missing behavior before production code is added.

---

### Task 1: Persist and project the provider TLS policy

**Files:**
- Modify: `backend/internal/domain/user/types.go`
- Modify: `backend/internal/infra/persistence/models/user.go`
- Modify: `backend/internal/repository/user.go`
- Create: `backend/internal/repository/user_test.go`
- Modify: `backend/internal/infra/persistence/postgres/user/repository.go`
- Modify: `backend/internal/infra/persistence/postgres/user/repository_sqlite_test.go`
- Modify: `backend/internal/infra/persistence/schema/schema_test.go`
- Modify: `backend/internal/application/auth/provider.go`
- Modify: `backend/internal/application/auth/provider_test.go`

**Interfaces:**
- Consumes: existing identity-provider create/update repository methods.
- Produces: `domainuser.IdentityProvider.TLSInsecureSkipVerify bool`, `repository.UpdateIdentityProviderInput.TLSInsecureSkipVerify *bool`, `appauth.UpsertIdentityProviderInput.TLSInsecureSkipVerify *bool`, and an admin-only pointer on `IdentityProviderView`.

- [ ] **Step 1: Write failing application tests for defaulting, update preservation, and public/admin projection**

Add these tests to `backend/internal/application/auth/provider_test.go`:

~~~go
func validOIDCProviderInput() UpsertIdentityProviderInput {
	return UpsertIdentityProviderInput{
		ActorRole:    domainuser.RoleAdmin,
		Type:         domainuser.IdentityProviderTypeOIDC,
		Name:         "Acme SSO",
		ClientID:     "client",
		ClientSecret: "secret",
		DiscoveryURL: "https://idp.example/.well-known/openid-configuration",
		DefaultRole:  domainuser.RoleUser,
	}
}

func TestNormalizeProviderInputTLSInsecureSkipVerify(t *testing.T) {
	service := NewService(config.Config{JWTSecret: "test-secret"}, &providerLoginRepo{}, nil)
	current := &domainuser.IdentityProvider{
		Type:                  domainuser.IdentityProviderTypeOIDC,
		Name:                  "Acme SSO",
		Slug:                  "acme",
		ClientID:              "client",
		ClientSecret:          "stored-secret",
		DiscoveryURL:          "https://idp.example/.well-known/openid-configuration",
		DefaultRole:           domainuser.RoleUser,
		TLSInsecureSkipVerify: true,
	}
	cases := []struct {
		name    string
		input   UpsertIdentityProviderInput
		current *domainuser.IdentityProvider
		want    bool
	}{
		{name: "create defaults secure", input: validOIDCProviderInput(), want: false},
		{name: "create explicitly insecure", input: func() UpsertIdentityProviderInput {
			input := validOIDCProviderInput()
			input.TLSInsecureSkipVerify = boolPtr(true)
			return input
		}(), want: true},
		{name: "update omission preserves current", input: func() UpsertIdentityProviderInput {
			input := validOIDCProviderInput()
			input.ClientSecret = ""
			return input
		}(), current: current, want: true},
		{name: "update explicitly restores verification", input: func() UpsertIdentityProviderInput {
			input := validOIDCProviderInput()
			input.ClientSecret = ""
			input.TLSInsecureSkipVerify = boolPtr(false)
			return input
		}(), current: current, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := service.normalizeProviderInput(tc.input, tc.current)
			if err != nil {
				t.Fatalf("normalizeProviderInput() error = %v", err)
			}
			if provider.TLSInsecureSkipVerify != tc.want {
				t.Fatalf("TLSInsecureSkipVerify = %v, want %v", provider.TLSInsecureSkipVerify, tc.want)
			}
		})
	}
}

func TestToProviderViewScopesTLSInsecureSkipVerifyToAdmin(t *testing.T) {
	item := domainuser.IdentityProvider{TLSInsecureSkipVerify: true}
	publicView := toProviderView(item, false)
	if publicView.TLSInsecureSkipVerify != nil {
		t.Fatalf("public TLS policy = %v, want nil", *publicView.TLSInsecureSkipVerify)
	}
	adminView := toProviderView(item, true)
	if adminView.TLSInsecureSkipVerify == nil || !*adminView.TLSInsecureSkipVerify {
		t.Fatalf("admin TLS policy = %v, want true", adminView.TLSInsecureSkipVerify)
	}
	item.TLSInsecureSkipVerify = false
	adminView = toProviderView(item, true)
	if adminView.TLSInsecureSkipVerify == nil || *adminView.TLSInsecureSkipVerify {
		t.Fatalf("admin TLS policy = %v, want explicit false", adminView.TLSInsecureSkipVerify)
	}
}
~~~

- [ ] **Step 2: Write failing repository and migration tests**

Create `backend/internal/repository/user_test.go`:

~~~go
package repository

import "testing"

func TestUpdateIdentityProviderInputIsZeroIncludesTLSInsecureSkipVerify(t *testing.T) {
	if !(UpdateIdentityProviderInput{}).IsZero() {
		t.Fatal("zero input must be zero")
	}
	for _, value := range []bool{false, true} {
		value := value
		if (UpdateIdentityProviderInput{TLSInsecureSkipVerify: &value}).IsZero() {
			t.Fatalf("TLS policy %v was ignored by IsZero", value)
		}
	}
}
~~~

Add this round-trip test to `backend/internal/infra/persistence/postgres/user/repository_sqlite_test.go` and import `domainuser`:

~~~go
func TestIdentityProviderTLSInsecureSkipVerifyRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:identity_provider_tls_policy?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = db.AutoMigrate(&models.AuthIdentityProvider{}); err != nil {
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
~~~

Add a legacy table type and migration test to `backend/internal/infra/persistence/schema/schema_test.go`:

~~~go
type legacyAuthIdentityProvider struct {
	model.BaseModel
	PublicID string `gorm:"size:32;not null;default:'';uniqueIndex:idx_identity_providers_public_id"`
	Type     string `gorm:"size:16;not null;default:''"`
	Name     string `gorm:"size:80;not null;default:''"`
	Slug     string `gorm:"size:64;not null;default:'';uniqueIndex:idx_identity_providers_slug"`
}

func (legacyAuthIdentityProvider) TableName() string {
	return "identity_providers"
}

func TestMigrateAddsIdentityProviderTLSInsecureSkipVerifyWithFalseDefault(t *testing.T) {
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = db.AutoMigrate(&legacyAuthIdentityProvider{}); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	legacy := legacyAuthIdentityProvider{PublicID: "provider_legacy", Type: "oauth2", Name: "Legacy", Slug: "legacy"}
	if err = db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy provider: %v", err)
	}
	if err = Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if !db.Migrator().HasColumn(&model.AuthIdentityProvider{}, "tls_insecure_skip_verify") {
		t.Fatal("migration did not add tls_insecure_skip_verify")
	}
	var migrated model.AuthIdentityProvider
	if err = db.Where("public_id = ?", "provider_legacy").First(&migrated).Error; err != nil {
		t.Fatalf("load migrated provider: %v", err)
	}
	if migrated.TLSInsecureSkipVerify {
		t.Fatal("legacy provider must default to secure TLS verification")
	}
}
~~~

- [ ] **Step 3: Run the new tests and verify RED**

Run from `backend`:

~~~bash
go test ./internal/repository ./internal/infra/persistence/schema ./internal/infra/persistence/postgres/user ./internal/application/auth -run 'TLSInsecureSkipVerify' -count=1
~~~

Expected: compilation fails because the TLS policy fields do not exist.

- [ ] **Step 4: Add the minimal field and mapping implementation**

Add `TLSInsecureSkipVerify bool` to the domain and Gorm models. The Gorm field is:

~~~go
TLSInsecureSkipVerify bool `gorm:"not null;default:false;comment:是否跳过身份源 TLS 证书校验"`
~~~

Add this field to repository update input and its `IsZero` chain:

~~~go
TLSInsecureSkipVerify *bool
~~~

Map it in `identityProviderUpdates`, `toDomainIdentityProvider`, and `toModelIdentityProvider`:

~~~go
if input.TLSInsecureSkipVerify != nil {
	updates["tls_insecure_skip_verify"] = *input.TLSInsecureSkipVerify
}
~~~

Add the pointer to application input/view, preserve current values during updates, and map it into repository updates:

~~~go
tlsInsecureSkipVerify := false
if current != nil {
	tlsInsecureSkipVerify = current.TLSInsecureSkipVerify
}
if input.TLSInsecureSkipVerify != nil {
	tlsInsecureSkipVerify = *input.TLSInsecureSkipVerify
}
provider.TLSInsecureSkipVerify = tlsInsecureSkipVerify
~~~

For admin projection, create a pointer only when `includeSensitive` is true:

~~~go
var tlsInsecureSkipVerify *bool
if includeSensitive {
	value := item.TLSInsecureSkipVerify
	tlsInsecureSkipVerify = &value
}
~~~

- [ ] **Step 5: Format and verify GREEN**

Run:

~~~bash
gofmt -w internal/domain/user/types.go internal/infra/persistence/models/user.go internal/repository/user.go internal/repository/user_test.go internal/infra/persistence/postgres/user/repository.go internal/infra/persistence/postgres/user/repository_sqlite_test.go internal/infra/persistence/schema/schema_test.go internal/application/auth/provider.go internal/application/auth/provider_test.go
go test ./internal/repository ./internal/infra/persistence/schema ./internal/infra/persistence/postgres/user ./internal/application/auth -count=1
~~~

Expected: all listed packages pass.

- [ ] **Step 6: Commit the persisted policy**

~~~bash
git add backend/internal/domain/user/types.go backend/internal/infra/persistence/models/user.go backend/internal/repository/user.go backend/internal/repository/user_test.go backend/internal/infra/persistence/postgres/user/repository.go backend/internal/infra/persistence/postgres/user/repository_sqlite_test.go backend/internal/infra/persistence/schema/schema_test.go backend/internal/application/auth/provider.go backend/internal/application/auth/provider_test.go
git commit -m "feat: persist provider TLS verification override"
~~~

---

### Task 2: Expose the policy only through provider management APIs

**Files:**
- Modify: `backend/internal/transport/http/auth/dto.go`
- Modify: `backend/internal/transport/http/auth/handler.go`
- Create: `backend/internal/transport/http/auth/dto_test.go`
- Regenerate: `backend/docs/docs.go`
- Regenerate: `backend/docs/swagger.json`
- Regenerate: `backend/docs/swagger.yaml`

**Interfaces:**
- Consumes: admin-only `IdentityProviderView.TLSInsecureSkipVerify *bool`.
- Produces: optional JSON response field and optional update request field named `tlsInsecureSkipVerify`.

- [ ] **Step 1: Write failing DTO visibility and input-mapping tests**

Create `backend/internal/transport/http/auth/dto_test.go`:

~~~go
package auth

import (
	"encoding/json"
	"testing"

	appauth "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/auth"
)

func providerJSON(t *testing.T, value interface{}) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	var decoded map[string]interface{}
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
	return decoded
}

func TestLoginOptionsResponseOmitsTLSInsecureSkipVerify(t *testing.T) {
	response := toLoginOptionsResponse(&appauth.LoginOptions{
		Providers: []appauth.IdentityProviderView{{Name: "Public Provider"}},
	})
	raw := providerJSON(t, response)
	providers := raw["providers"].([]interface{})
	provider := providers[0].(map[string]interface{})
	if _, exists := provider["tlsInsecureSkipVerify"]; exists {
		t.Fatal("public login options exposed the TLS policy")
	}
}

func TestIdentityProviderResponseIncludesTLSInsecureSkipVerify(t *testing.T) {
	for _, value := range []bool{false, true} {
		value := value
		response := toIdentityProviderResponse(appauth.IdentityProviderView{
			TLSInsecureSkipVerify: &value,
		})
		raw := providerJSON(t, response)
		if got, exists := raw["tlsInsecureSkipVerify"]; !exists || got != value {
			t.Fatalf("TLS policy = %v, exists=%v, want %v", got, exists, value)
		}
	}
}

func TestToUpsertIdentityProviderInputMapsTLSInsecureSkipVerify(t *testing.T) {
	value := true
	input := toUpsertIdentityProviderInput(UpsertIdentityProviderRequest{
		TLSInsecureSkipVerify: &value,
	}, "admin")
	if input.TLSInsecureSkipVerify == nil || !*input.TLSInsecureSkipVerify {
		t.Fatalf("mapped TLS policy = %v, want true", input.TLSInsecureSkipVerify)
	}
}
~~~

- [ ] **Step 2: Run the transport test and verify RED**

Run:

~~~bash
go test ./internal/transport/http/auth -run 'TLSInsecureSkipVerify' -count=1
~~~

Expected: compilation fails because the request/response fields and mappings are missing.

- [ ] **Step 3: Add the management request/response contract**

Add these exact DTO fields:

~~~go
TLSInsecureSkipVerify *bool `json:"tlsInsecureSkipVerify,omitempty"`
~~~

Use the same pointer type on `UpsertIdentityProviderRequest` with `json:"tlsInsecureSkipVerify"`. Map it into `UpsertIdentityProviderInput` and from `IdentityProviderView` into `IdentityProviderResponse`.

The public view already contains nil from Task 1, so `omitempty` removes the field. Admin list/create/update views contain non-nil pointers, including explicit false.

- [ ] **Step 4: Format, test, and regenerate Swagger**

Run:

~~~bash
gofmt -w internal/transport/http/auth/dto.go internal/transport/http/auth/handler.go internal/transport/http/auth/dto_test.go
go test ./internal/transport/http/auth -count=1
make swagger
git diff --check
~~~

Expected: transport tests pass and Swagger artifacts include `tlsInsecureSkipVerify` on provider management DTOs.

- [ ] **Step 5: Commit the API contract**

~~~bash
git add backend/internal/transport/http/auth/dto.go backend/internal/transport/http/auth/handler.go backend/internal/transport/http/auth/dto_test.go backend/docs/docs.go backend/docs/swagger.json backend/docs/swagger.yaml
git commit -m "feat: expose provider TLS verification override"
~~~

---

### Task 3: Select an immutable provider-only insecure HTTP client

**Files:**
- Modify: `backend/internal/application/auth/service.go`
- Modify: `backend/internal/application/auth/provider.go`
- Modify: `backend/internal/application/auth/provider_test.go`
- Modify: `backend/internal/application/auth/registration_test.go`

**Interfaces:**
- Consumes: `domainuser.IdentityProvider.TLSInsecureSkipVerify bool`.
- Produces: `newAuthOutboundHTTPClient(env string, ssrfProtectionEnabled bool, tlsInsecureSkipVerify bool) *http.Client` and `identityProviderHTTPClient(provider domainuser.IdentityProvider) *http.Client`.

- [ ] **Step 1: Write failing client-construction and selection tests**

Add `github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/security` to `provider_test.go`. Add:

~~~go
func TestAuthOutboundHTTPClientHonorsTLSVerificationPolicy(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	secureClient := newAuthOutboundHTTPClient("", false, false)
	if response, err := secureClient.Get(server.URL); err == nil {
		response.Body.Close()
		t.Fatal("secure client unexpectedly accepted a self-signed certificate")
	}
	insecureClient := newAuthOutboundHTTPClient("", false, true)
	response, err := insecureClient.Get(server.URL)
	if err != nil {
		t.Fatalf("insecure provider client rejected test certificate: %v", err)
	}
	response.Body.Close()
}

func TestAuthOutboundHTTPClientKeepsSSRFProtectionWhenTLSVerificationSkipped(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newAuthOutboundHTTPClient("prod", true, true)
	_, err := client.Get(server.URL)
	if !errors.Is(err, security.ErrUnsafeOutboundURL) {
		t.Fatalf("SSRF error = %v, want ErrUnsafeOutboundURL", err)
	}
}

func TestIdentityProviderHTTPClientSelectsPerProviderPolicy(t *testing.T) {
	secureClient := &http.Client{}
	insecureClient := &http.Client{}
	service := &Service{
		providerHTTPClient:            secureClient,
		providerTLSInsecureHTTPClient: insecureClient,
	}
	if got := service.identityProviderHTTPClient(domainuser.IdentityProvider{}); got != secureClient {
		t.Fatal("secure provider selected the wrong client")
	}
	if got := service.identityProviderHTTPClient(domainuser.IdentityProvider{TLSInsecureSkipVerify: true}); got != insecureClient {
		t.Fatal("insecure provider selected the wrong client")
	}
}
~~~

- [ ] **Step 2: Make provider protocol tests require the new selector**

In `TestCompleteProviderLoginAutoLinksGitHubVerifiedPrimaryEmail`, replace its `httptest.NewServer` constructor with:

~~~go
server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
~~~

Keep the existing handler body unchanged, and add this field to that test's provider fixture next to `DefaultRole`:

~~~go
TLSInsecureSkipVerify: true,
~~~

This existing flow must still traverse token, user-info, and `/user/emails`.

Add:

~~~go
func TestResolveProviderEndpointsHonorsTLSInsecureSkipVerify(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"https://idp.example/auth","token_endpoint":"https://idp.example/token","userinfo_endpoint":"https://idp.example/userinfo"}`))
	}))
	defer server.Close()

	service := NewService(config.Config{}, &providerLoginRepo{}, nil)
	for _, tc := range []struct {
		name string
		skip bool
		ok   bool
	}{
		{name: "secure rejects self signed", skip: false, ok: false},
		{name: "override permits self signed", skip: true, ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := service.resolveProviderEndpoints(context.Background(), domainuser.IdentityProvider{
				Type:                  domainuser.IdentityProviderTypeOIDC,
				DiscoveryURL:          server.URL,
				TLSInsecureSkipVerify: tc.skip,
			})
			if (err == nil) != tc.ok {
				t.Fatalf("resolveProviderEndpoints() error = %v, want success %v", err, tc.ok)
			}
		})
	}
}

func TestGetIdentityProviderLogoKeepsTLSVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer server.Close()

	service := NewService(config.Config{}, &providerLoginRepo{
		providersBySlug: map[string]*domainuser.IdentityProvider{
			"acme": {
				Slug:                  "acme",
				LogoURL:               server.URL,
				TLSInsecureSkipVerify: true,
			},
		},
	}, nil)
	if _, err := service.GetIdentityProviderLogo(context.Background(), "acme"); !errors.Is(err, ErrIdentityProviderLogoUnavailable) {
		t.Fatalf("logo error = %v, want ErrIdentityProviderLogoUnavailable", err)
	}
}
~~~

Add this regression test to `registration_test.go`:

~~~go
func TestVerifyRegistrationTurnstileKeepsTLSVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	service := NewService(config.Config{}, &providerLoginRepo{}, nil)
	err := service.verifyRegistrationTurnstile(context.Background(), config.Config{
		TurnstileRegistrationEnabled: true,
		TurnstileSiteKey:             "site-key",
		TurnstileSecretKey:           "secret-key",
		TurnstileSiteverifyURL:        server.URL,
	}, "token", "")
	if !errors.Is(err, errTurnstileFailed) {
		t.Fatalf("Turnstile error = %v, want errTurnstileFailed", err)
	}
}
~~~

- [ ] **Step 3: Run focused tests and verify RED**

Run:

~~~bash
go test ./internal/application/auth -run 'AuthOutboundHTTPClient|IdentityProviderHTTPClient|ResolveProviderEndpointsHonors|KeepsTLSVerification|CompleteProviderLoginAutoLinksGitHub' -count=1
~~~

Expected: compilation or TLS failures show that no provider-only client exists and protocol calls still use the secure shared client.

- [ ] **Step 4: Implement immutable secure and provider-only clients**

In `service.go`, add `crypto/tls`, add `providerTLSInsecureHTTPClient *http.Client`, and build both clients at service construction. Implement:

~~~go
func newAuthOutboundHTTPClient(env string, ssrfProtectionEnabled bool, tlsInsecureSkipVerify bool) *http.Client {
	client := security.NewOutboundHTTPClient(env, ssrfProtectionEnabled, providerHTTPTimeout)
	if tlsInsecureSkipVerify {
		transport := client.Transport.(*http.Transport).Clone()
		tlsConfig := transport.TLSClientConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			tlsConfig = tlsConfig.Clone()
		}
		// Security: this client is selected only for an administrator-enabled identity provider.
		// #nosec G402 -- the explicit provider setting is the purpose of this isolated client.
		tlsConfig.InsecureSkipVerify = true
		transport.TLSClientConfig = tlsConfig
		client.Transport = transport
	}
	client.Transport = platformtracing.NewHTTPTransport(client.Transport)
	return client
}

func (s *Service) identityProviderHTTPClient(provider domainuser.IdentityProvider) *http.Client {
	if provider.TLSInsecureSkipVerify && s.providerTLSInsecureHTTPClient != nil {
		return s.providerTLSInsecureHTTPClient
	}
	return s.providerHTTPClient
}
~~~

Use `identityProviderHTTPClient(provider).Do(request)` only in discovery, token exchange, user-info, and supplemental GitHub-email requests. Pass the provider into `enrichGitHubVerifiedEmail`:

~~~go
func (s *Service) enrichGitHubVerifiedEmail(
	ctx context.Context,
	provider domainuser.IdentityProvider,
	accessToken string,
	profile map[string]interface{},
	emailsURL string,
) error
~~~

Leave `GetIdentityProviderLogo` and `verifyRegistrationTurnstile` on `s.providerHTTPClient`.

- [ ] **Step 5: Format and verify GREEN**

Run:

~~~bash
gofmt -w internal/application/auth/service.go internal/application/auth/provider.go internal/application/auth/provider_test.go internal/application/auth/registration_test.go
go test ./internal/application/auth -count=1
go vet ./internal/application/auth
~~~

Expected: all auth tests pass; logs from intentional self-signed failures may appear, but no secrets are emitted.

- [ ] **Step 6: Commit the scoped TLS client behavior**

~~~bash
git add backend/internal/application/auth/service.go backend/internal/application/auth/provider.go backend/internal/application/auth/provider_test.go backend/internal/application/auth/registration_test.go
git commit -m "feat: apply provider TLS verification override"
~~~

---

### Task 4: Add the administrator switch and warning

**Files:**
- Modify: `frontend/features/admin/api/auth.ts`
- Modify: `frontend/shared/api/auth.types.ts`
- Modify: `frontend/features/admin/model/login-settings.ts`
- Modify: `frontend/features/admin/model/login-settings.test.mjs`
- Modify: `frontend/features/admin/components/sections/login/admin-login.tsx`
- Modify: `frontend/i18n/messages/zh-CN/admin-login.json`
- Modify: `frontend/i18n/messages/en-US/admin-login.json`

**Interfaces:**
- Consumes: management JSON field `tlsInsecureSkipVerify`.
- Produces: required provider-form Boolean, advanced-settings switch, and enabled-state warning.

- [ ] **Step 1: Write failing model tests**

Add to `frontend/features/admin/model/login-settings.test.mjs`:

~~~js
test("keeps TLS certificate verification enabled by default", () => {
  const payload = loginSettings.buildIdentityProviderPayload({
    ...loginSettings.DEFAULT_PROVIDER_FORM,
    type: "oauth2",
    name: "Acme OAuth",
    slug: "acme-oauth",
  });

  assert.equal(payload?.tlsInsecureSkipVerify, false);
});

test("preserves the TLS certificate verification override when editing", () => {
  const form = loginSettings.providerToForm({
    publicID: "provider_1",
    type: "oauth2",
    name: "Acme OAuth",
    slug: "acme-oauth",
    logoURL: "",
    loginEnabled: true,
    registrationEnabled: true,
    clientID: "client-id",
    issuerURL: "",
    discoveryURL: "",
    authURL: "https://idp.example/authorize",
    tokenURL: "https://idp.example/token",
    userinfoURL: "https://idp.example/userinfo",
    jwksURL: "",
    scopes: "profile email",
    defaultRole: "user",
    subjectField: "id",
    emailField: "email",
    emailVerifiedField: "email_verified",
    nameField: "name",
    avatarField: "picture",
    tlsInsecureSkipVerify: true,
    createdAt: "2026-07-14T00:00:00Z",
    updatedAt: "2026-07-14T00:00:00Z",
  });

  assert.equal(form.tlsInsecureSkipVerify, true);
});
~~~

- [ ] **Step 2: Run the focused frontend test and verify RED**

Run:

~~~bash
node --import tsx --test features/admin/model/login-settings.test.mjs
~~~

Expected: assertions receive `undefined` because the form model does not yet carry the field.

- [ ] **Step 3: Add the API and form model fields**

Add this required field to `IdentityProviderPayload` and `IdentityProviderDTO`:

~~~ts
tlsInsecureSkipVerify: boolean;
~~~

Add this secure default to `DEFAULT_PROVIDER_FORM`:

~~~ts
tlsInsecureSkipVerify: false,
~~~

Add this compatibility-preserving edit mapping to `providerToForm`:

~~~ts
tlsInsecureSkipVerify: provider.tlsInsecureSkipVerify ?? false,
~~~

- [ ] **Step 4: Add the advanced-settings switch and warning**

Import `TriangleAlert`, `Alert`, and `AlertDescription`. At the top of `AccordionContent`, before claim mappings, add:

~~~tsx
<div className="space-y-3">
  <div className="flex items-center justify-between gap-4">
    <div className="min-w-0 space-y-1">
      <p className="text-sm font-medium">{t("providerDialog.tlsInsecureSkipVerify")}</p>
      <p className="text-xs text-muted-foreground">{t("providerDialog.tlsInsecureSkipVerifyDescription")}</p>
    </div>
    <Switch
      checked={providerForm.tlsInsecureSkipVerify}
      onCheckedChange={(checked) =>
        setProviderForm((previous) => ({ ...previous, tlsInsecureSkipVerify: checked }))
      }
    />
  </div>
  {providerForm.tlsInsecureSkipVerify ? (
    <Alert variant="destructive">
      <TriangleAlert aria-hidden="true" />
      <AlertDescription>{t("providerDialog.tlsInsecureSkipVerifyWarning")}</AlertDescription>
    </Alert>
  ) : null}
</div>
<Separator className="my-3" />
~~~

Add these Chinese keys under `providerDialog`:

~~~json
"tlsInsecureSkipVerify": "忽略 TLS 证书校验",
"tlsInsecureSkipVerifyDescription": "仅影响服务器访问此身份源的 HTTPS 请求。",
"tlsInsecureSkipVerifyWarning": "启用后服务器将不验证该身份源的 HTTPS 证书，连接可能遭受中间人攻击。请仅临时启用，并尽快修复证书。",
~~~

Add these English keys:

~~~json
"tlsInsecureSkipVerify": "Skip TLS certificate verification",
"tlsInsecureSkipVerifyDescription": "This affects only server-side HTTPS requests to this identity provider.",
"tlsInsecureSkipVerifyWarning": "The server will no longer verify this identity provider's HTTPS certificate, exposing the connection to man-in-the-middle attacks. Enable this only temporarily and fix the certificate as soon as possible.",
~~~

- [ ] **Step 5: Verify frontend GREEN**

Run from `frontend`:

~~~bash
node --import tsx --test features/admin/model/login-settings.test.mjs
pnpm test
pnpm lint
pnpm build
~~~

Expected: all commands pass, and the static export includes the updated provider dialog.

- [ ] **Step 6: Commit the administrator UI**

~~~bash
git add frontend/features/admin/api/auth.ts frontend/shared/api/auth.types.ts frontend/features/admin/model/login-settings.ts frontend/features/admin/model/login-settings.test.mjs frontend/features/admin/components/sections/login/admin-login.tsx frontend/i18n/messages/zh-CN/admin-login.json frontend/i18n/messages/en-US/admin-login.json
git commit -m "feat: configure provider TLS verification override"
~~~

---

## Final Verification

- [ ] Run the backend contract and security-focused suites:

~~~bash
cd backend
go test ./internal/repository ./internal/infra/persistence/schema ./internal/infra/persistence/postgres/user ./internal/application/auth ./internal/transport/http/auth -count=1
make test
go vet ./...
make build
make swagger
git diff --exit-code -- docs/docs.go docs/swagger.json docs/swagger.yaml
~~~

- [ ] Run the complete frontend checks:

~~~bash
cd frontend
pnpm test
pnpm lint
pnpm build
~~~

- [ ] Review the final diff for scope:

~~~bash
git diff 5186481..HEAD --check
git status --short
git log -4 --oneline
~~~

Expected: only the approved provider-scoped field, client selection, management contract, tests, UI, i18n, and Swagger artifacts are changed; `docker-compose.yml.bak` remains untouched.
