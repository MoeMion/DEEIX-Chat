# Phase C Per-MCP-Server Signed Context Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add independently keyed, audience-bound, short-lived HS256 `X-DEEIX-Context` assertions to each MCP Server with safe two-stage rotation and administrator UI.

**Architecture:** MCP Server rows gain a stable public ID, non-secret policy fields, an encrypted current secret, and one encrypted pending secret. A testable infra signer consumes the immutable A+B `TemplateContext`; application services own random-key generation, encryption, rotation transactions, safe audit metadata, and runtime configuration; the MCP client signs once per operation session and injects the reserved Header after custom Headers.

**Tech Stack:** Go 1.26, Gorm, PostgreSQL/SQLite, `crypto/rand`, `encoding/base64`, `github.com/golang-jwt/jwt/v5`, existing `secretbox`/`DataEncryptionKey`, Gin, Next.js 16, React 19, TypeScript, next-intl.

## Global Constraints

- Run every command from the active isolated feature worktree root; `cd backend` and `cd frontend` are intentionally repository-relative.
- Gate A+B in `2026-07-10-mcp-header-templates-ab.md` must pass before this plan starts.
- Reuse `inframcp.ContextMode`, `inframcp.TemplateContext`, `inframcp.CallConfig`, and the A+B Header blacklist without renaming or reinterpretation.
- Preserve the A+B single template behavior; phase C does not introduce Open WebUI aliases, compatibility flags, or strict evaluation.
- The signed Header name is always `X-DEEIX-Context`. It remains blocked by both the exact `X-DEEIX-Context` and prefix `X-DEEIX-` blacklist rules.
- Each MCP Server has an independent secret. Generate 32 random bytes with `crypto/rand` and return `base64.RawURLEncoding` text; use the encoded UTF-8 bytes directly as the HMAC key.
- Encrypt current and pending secrets with the existing `DataEncryptionKey` and `secretbox` before persistence.
- Never reuse `AuthToken`, `AuthTokenEnc`, MCP OAuth secrets, login `JWTSecret`, or another Server secret.
- JWT Header is exactly `alg=HS256`, `typ=JWT`, plus `kid`. Claims include `iss`, exact `aud`, `sub`, `jti`, `iat`, `nbf`, `exp`, and `mode`.
- `iss` equals trimmed `PUBLIC_WEB_BASE_URL` with trailing `/` characters removed. Parse it as an absolute URL: require host and `http`/`https`, require `https` in production, and reject userinfo, query, and fragment. `aud` equals `urn:deeix:mcp:<server-public-id>`.
- Default TTL is 300 seconds; accepted values are 60 through 900 seconds inclusive.
- The emitted JWT/Header is at most 8192 bytes. Byte limits are: each public ID 128, display name 256, email 320, role 64, request ID 128, run ID 64, trace ID 64, issuer 512, audience 128, key ID 64, and JTI 64.
- A signing secret must be canonical unpadded `base64.RawURLEncoding`, decode to exactly 32 bytes, and re-encode byte-for-byte to the supplied string. Signing still uses the encoded UTF-8 bytes—not decoded bytes—as the HMAC key.
- Name, email, and role claims are independently opt-in and default false. Omitted means absent from JSON, not an empty claim.
- Chat and probe require the actual user public ID as `sub`. Sync uses `sub=system:mcp-sync` and `mode=sync` with no user PII claims.
- Allow at most one pending rotation. Pending expires 24 hours after prepare. Activate uses a dialect-portable conditional update inside a transaction and atomically promotes pending to current; correctness never depends on a row lock.
- DEEIX never stores a previous signing secret. The target MCP Server owns its old/new verification window.
- Prepare is the only response containing plaintext secret and must set `Cache-Control: no-store`. No other response returns real or masked secret.
- Audit, log, trace, system event, error details, and `last_error` may contain server public ID, action, `kid`, TTL, opt-in booleans, and result only; never secret, JWT, encryption key, or email.
- Do not implement asymmetric keys, JWKS, target MCP Server middleware, multiple pending keys, or phase D session reuse.

---

## File and Interface Map

**New backend files**

- `backend/internal/infra/mcp/context_jwt.go` and `context_jwt_test.go`: deterministic signer and claims.
- `backend/internal/application/mcp/context_jwt.go` and `context_jwt_test.go`: policy, prepare, activate, cancel, disable.
- `backend/internal/transport/http/mcp/handler_context_jwt_test.go`: one-time response and route tests.

**New frontend files**

- `frontend/features/admin/components/sections/tools/mcp-context-jwt-panel.tsx`.
- `frontend/features/admin/components/sections/tools/mcp-context-secret-dialog.tsx`.

**Frozen phase C interfaces**

```go
type SignedContextConfig struct {
	Secret          string
	Issuer          string
	Audience        string
	KeyID           string
	ExpiresSeconds  int
	IncludeName     bool
	IncludeEmail    bool
	IncludeRole     bool
}

type ContextSigner interface {
	Sign(context TemplateContext, config SignedContextConfig) (string, error)
}

var ErrContextSignerUnavailable = errors.New("mcp context signer unavailable")
```

Append `SignedContext *SignedContextConfig` to A+B `CallConfig`. Do not place plaintext secret in any domain HTTP response type.

### Task 1: Add Stable MCP Server Public IDs and Signed-Context Columns

**Files:**
- Modify: `backend/internal/infra/persistence/models/mcp.go:6-17`
- Modify: `backend/internal/domain/mcp/types.go:6-20`
- Modify: `backend/internal/repository/mcp.go:9-53`
- Modify: `backend/internal/infra/persistence/postgres/mcp/repository.go:22-75`
- Modify: `backend/internal/infra/persistence/schema/schema.go:68-82`
- Modify: `backend/internal/infra/persistence/schema/schema_test.go`
- Modify: `backend/internal/infra/persistence/postgres/mcp/repository_sqlite_test.go`
- Modify: `backend/internal/application/mcp/service.go`

**Interfaces:**
- Consumes: existing `schema.Migrate`, `ControlPlaneModel`, UUID conventions, PostgreSQL/SQLite shared repository.
- Produces: stable `PublicID`/audience and signed-context storage fields with a post-backfill unique index.

- [ ] **Step 1: Write a legacy-row migration test**

Create two `mcp_servers` rows using the pre-C shape, run `schema.Migrate`, and assert:

```go
if first.PublicID == "" || second.PublicID == "" || first.PublicID == second.PublicID {
	t.Fatalf("invalid public ids: %q %q", first.PublicID, second.PublicID)
}
if !strings.HasPrefix(first.PublicID, "mcp_") {
	t.Fatalf("unexpected public id %q", first.PublicID)
}
if first.ContextJWTAudience != "urn:deeix:mcp:"+first.PublicID {
	t.Fatalf("unexpected audience %q", first.ContextJWTAudience)
}
```

Attempt a duplicate non-empty public ID insert and assert the database returns a unique-index error. Also insert two rows with `public_id=''` after migration and assert both succeed; this proves an A+B binary can create Servers after a C rollback.

- [ ] **Step 2: Run the schema test and confirm red**

```powershell
cd backend
go test ./internal/infra/persistence/schema -run TestMigrateBackfillsMCPServerPublicIDs -count=1
```

Expected: compilation fails because the model fields do not exist.

- [ ] **Step 3: Add model and domain fields**

Add these fields, without a Gorm unique tag on `PublicID`:

```go
PublicID                   string     `gorm:"size:40;not null;default:'';comment:公开MCP服务ID"`
ContextJWTMode              string     `gorm:"size:16;not null;default:'none';comment:上下文JWT模式"`
ContextJWTSecretEnc         string     `gorm:"type:text;not null;default:'';comment:当前上下文JWT密钥密文"`
ContextJWTAudience          string     `gorm:"size:128;not null;default:'';comment:上下文JWT受众"`
ContextJWTKeyID             string     `gorm:"size:64;not null;default:'';comment:当前上下文JWT kid"`
ContextJWTExpiresSeconds    int        `gorm:"not null;default:300;comment:上下文JWT有效期秒"`
ContextJWTIncludeName       bool       `gorm:"not null;default:false;comment:是否签入用户名称"`
ContextJWTIncludeEmail      bool       `gorm:"not null;default:false;comment:是否签入用户邮箱"`
ContextJWTIncludeRole       bool       `gorm:"not null;default:false;comment:是否签入用户角色"`
ContextJWTPendingSecretEnc  string     `gorm:"type:text;not null;default:'';comment:待激活上下文JWT密钥密文"`
ContextJWTPendingKeyID      string     `gorm:"size:64;not null;default:'';comment:待激活上下文JWT kid"`
ContextJWTPendingCreatedAt  *time.Time `gorm:"comment:待激活密钥创建时间"`
ContextJWTPendingExpiresAt  *time.Time `gorm:"index:idx_mcp_context_pending_expires_at;comment:待激活密钥过期时间"`
```

Mirror every field above into `domain/mcp.Server` without Gorm tags, including the encrypted current/pending values and both pending timestamps, and map every field in `postgres/mcp.toDomainServer`. These ciphertext fields are required by Tasks 3-5 but are never copied into an HTTP DTO; Task 6 tests that the explicit transport mapper omits them.

- [ ] **Step 4: Backfill before unique index creation**

After `AutoMigrate`, call:

```go
func backfillMCPServerPublicIDs(db *gorm.DB) error
func ensureMCPServerPublicIDIndex(db *gorm.DB) error
```

The backfill selects rows with blank public ID, assigns `mcp_` plus a dashless UUID, sets audience in the same row update, and retries on collision. The index step executes:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_servers_public_id
ON mcp_servers(public_id)
WHERE public_id <> ''
```

Both supported PostgreSQL and SQLite deployments support this partial index. Do not put `uniqueIndex` on the model field because AutoMigrate would attempt the index before legacy rows are backfilled, and a normal unique index would break rollback when an older A+B binary writes the default empty public ID.

- [ ] **Step 5: Generate public IDs for new Servers**

Add both `PublicID` and `ContextJWTAudience` to `CreateMCPServerInput` and its persistence mapping. In application create, generate `mcp_<dashless-uuid>` and `urn:deeix:mcp:<public-id>` before calling the repository. Repository tests assert both persist unchanged; repository must not derive a different audience.

- [ ] **Step 6: Run schema/repository tests**

```powershell
cd backend
go test ./internal/infra/persistence/schema ./internal/infra/persistence/postgres/mcp -count=1
```

Expected: tests pass on SQLite; the SQL remains valid for PostgreSQL.

- [ ] **Step 7: Commit the migration**

```powershell
git add backend/internal/infra/persistence backend/internal/domain/mcp backend/internal/repository/mcp.go backend/internal/application/mcp/service.go
git commit -m "feat: add mcp signed context schema"
```

### Task 2: Implement the Deterministic HS256 Context Signer

**Files:**
- Create: `backend/internal/infra/mcp/context_jwt.go`
- Create: `backend/internal/infra/mcp/context_jwt_test.go`

**Interfaces:**
- Consumes: A+B `TemplateContext` and phase C `SignedContextConfig`.
- Produces: `JWTContextSigner` implementing `ContextSigner`, plus `ValidateSignedContextSecret(string) error`, with fixed algorithm, claims, opt-in behavior, and test hooks.

- [ ] **Step 1: Write signer contract tests**

Use a fixed time and fixed JTI. Parse the token with `jwt.WithTimeFunc(func() time.Time { return fixedNow })`, `jwt.WithValidMethods([]string{"HS256"})`, `jwt.WithIssuer`, and `jwt.WithAudience`. Assert Header `typ`/`kid`, registered claims, mode/context public IDs, 300-second expiry, and absence of name/email/role by default.

Add cases for each opt-in, TTL 59/901 rejection, missing issuer/audience/key ID/secret, non-canonical/padded/31-byte secret, probe without user rejection, and sync system subject. Add byte-bound cases for every context/config string plus a final token above 8192 bytes; each fails with a safe sentinel and no claim value in `err.Error()`.

- [ ] **Step 2: Run tests and confirm missing signer**

```powershell
cd backend
go test ./internal/infra/mcp -run TestJWTContextSigner -count=1
```

Expected: compilation fails because signer types do not exist.

- [ ] **Step 3: Define claims and signer**

```go
type ContextJWTClaims struct {
	Mode                     string `json:"mode"`
	Name                     string `json:"name,omitempty"`
	Email                    string `json:"email,omitempty"`
	Role                     string `json:"role,omitempty"`
	ConversationPublicID     string `json:"conversation_id,omitempty"`
	AssistantMessagePublicID string `json:"assistant_message_id,omitempty"`
	UserMessagePublicID      string `json:"user_message_id,omitempty"`
	RequestID                string `json:"request_id,omitempty"`
	RunID                    string `json:"run_id,omitempty"`
	TraceID                  string `json:"trace_id,omitempty"`
	jwt.RegisteredClaims
}

type JWTContextSigner struct {
	now   func() time.Time
	newID func() string
}

const (
	maxSignedContextHeaderBytes = 8192
	maxContextPublicIDBytes     = 128
	maxContextDisplayNameBytes  = 256
	maxContextEmailBytes        = 320
	maxContextRoleBytes         = 64
	maxContextRequestIDBytes    = 128
	maxContextRunIDBytes        = 64
	maxContextTraceIDBytes      = 64
	maxContextIssuerBytes       = 512
	maxContextAudienceBytes     = 128
	maxContextKeyIDBytes        = 64
	maxContextJTIBytes          = 64
)

var (
	ErrInvalidSignedContext       = errors.New("invalid mcp signed context")
	ErrSignedContextHeaderTooLarge = errors.New("mcp signed context header exceeds limit")
)

func ValidateSignedContextSecret(secret string) error
```

Production constructor uses `time.Now` and `ctx_` plus dashless UUID. Tests use an unexported constructor with fixed functions.

`assistant_message_id` is the DEEIX phase-C claim name for `TemplateContext.AssistantMessagePublicID`; do not emit the research report's illustrative `message_id` alias or any Open WebUI-compatible duplicate.

- [ ] **Step 4: Implement exact subject and time behavior**

Chat/probe require trimmed `UserPublicID`. Sync sets `sub=system:mcp-sync` regardless of user fields and omits all PII. Validate byte lengths before constructing claims: all public IDs use 128; request 128; run 64; trace 64; opt-in display name 256/email 320/role 64; issuer 512; audience 128; key ID/JTI 64. Errors identify only the field class, never the rejected value. Set `IssuedAt` and `NotBefore` to `now.UTC()` and `ExpiresAt` to `now+TTL`.

- [ ] **Step 5: Lock algorithm and Header**

Use `jwt.NewWithClaims(jwt.SigningMethodHS256, claims)`, then set:

```go
token.Header["typ"] = "JWT"
token.Header["kid"] = config.KeyID
```

Implement the exported `ValidateSignedContextSecret` once: decode with `base64.RawURLEncoding`, require exactly 32 decoded bytes, and require re-encoding to equal the original unpadded string. `Sign` calls this validator, then continues to sign with the original encoded UTF-8 bytes `[]byte(config.Secret)`; decoding is validation only. Never accept an algorithm from configuration.

After signing, require token length at most 8192 bytes and `httpguts.ValidHeaderFieldValue(token)`. Otherwise return `ErrSignedContextHeaderTooLarge`/`ErrInvalidSignedContext`; never return an oversized token to Client/Transport.

- [ ] **Step 6: Run signer tests**

```powershell
cd backend
go test ./internal/infra/mcp -run TestJWTContextSigner -count=1
```

Expected: all signer tests pass.

- [ ] **Step 7: Commit**

```powershell
git add backend/internal/infra/mcp/context_jwt.go backend/internal/infra/mcp/context_jwt_test.go
git commit -m "feat: sign mcp context assertions"
```

### Task 3: Add Atomic Repository Rotation Operations

**Files:**
- Modify: `backend/internal/repository/mcp.go`
- Modify: `backend/internal/infra/persistence/postgres/mcp/repository.go`
- Modify: `backend/internal/infra/persistence/postgres/mcp/repository_sqlite_test.go`

**Interfaces:**
- Consumes: Task 1 storage fields.
- Produces: the exact repository sentinels and methods below for policy update, prepare, activate, cancel, disable, and bulk pending-expiry cleanup.

- [ ] **Step 1: Add repository tests for state transitions**

In `backend/internal/infra/persistence/postgres/mcp/repository_sqlite_test.go`, add top-level tests whose names begin with `TestContextJWT`. Use one shared file-backed SQLite DSN for concurrency cases, two independent repository instances, start barriers, and result channels. Cover:

- prepare on empty state saves pending but leaves mode/current unchanged;
- second unexpired prepare returns `ErrMCPContextJWTPendingExists`;
- expired pending may be replaced;
- two concurrent prepare calls have exactly one winner and one pending-conflict result;
- activate with wrong `kid` fails;
- activate after expiry fails and clears only the exact expired pending snapshot it observed;
- an activate racing with prepare must never erase the replacement pending rotation;
- activate promotes secret/key, sets `ContextJWTMode="hs256"`, and clears every pending field atomically;
- two concurrent activate calls for one `kid` have exactly one winner;
- cancel clears pending only;
- disable sets `ContextJWTMode="none"`, clears current and pending, and preserves public ID/audience.

- [ ] **Step 2: Run the repository tests and verify the red state**

```powershell
cd backend
go test ./internal/infra/persistence/postgres/mcp -run TestContextJWT -count=1
```

Expected: FAIL at compile time because `UpdateMCPContextJWTPolicyInput`, the three MCP context repository sentinels, and the six repository methods do not exist. Do not proceed if a test passes without exercising a missing symbol.

- [ ] **Step 3: Define the exact repository contract**

```go
var (
	ErrMCPContextJWTPendingExists    = errors.New("mcp context jwt pending rotation exists")
	ErrMCPContextJWTRotationConflict = errors.New("mcp context jwt rotation conflict")
	ErrMCPContextJWTPendingExpired   = errors.New("mcp context jwt pending rotation expired")
)

type UpdateMCPContextJWTPolicyInput struct {
	ExpiresSeconds int
	IncludeName    bool
	IncludeEmail   bool
	IncludeRole    bool
}

type PrepareMCPContextJWTRotationInput struct {
	ServerID         uint
	PendingSecretEnc string
	PendingKeyID     string
	CreatedAt        time.Time
	ExpiresAt        time.Time
}

type MCPRepository interface {
	// Existing A+B methods remain unchanged.
	UpdateContextJWTPolicy(context.Context, uint, UpdateMCPContextJWTPolicyInput) (*domainmcp.Server, error)
	PrepareContextJWTRotation(context.Context, PrepareMCPContextJWTRotationInput) (*domainmcp.Server, error)
	ActivateContextJWTRotation(context.Context, uint, string, time.Time) (*domainmcp.Server, error)
	CancelContextJWTRotation(context.Context, uint, string) (*domainmcp.Server, error)
	DisableContextJWT(context.Context, uint) (*domainmcp.Server, error)
	ClearExpiredContextJWTPending(context.Context, time.Time) error
}
```

Keep the method declarations on the existing `MCPRepository`; the abbreviated interface above shows only the additions. Persistence returns `repository.ErrNotFound` for a missing Server, `ErrMCPContextJWTPendingExists` for an unexpired prepare collision, `ErrMCPContextJWTPendingExpired` only after the exact expired pending snapshot was cleared and committed, and `ErrMCPContextJWTRotationConflict` for a wrong/stale `kid` or another compare-and-swap loss.

- [ ] **Step 4: Implement dialect-portable compare-and-swap transactions**

For prepare/activate/cancel/disable use `db.Transaction`. PostgreSQL may additionally use:

```go
tx.Clauses(clause.Locking{Strength: "UPDATE"}).
	First(&row, "id = ?", serverID)
```

Correctness must not rely on that lock because SQLite ignores `FOR UPDATE`. Make each state transition a conditional `UPDATE` and require `RowsAffected == 1`:

- prepare matches the Server ID only when pending material is empty, expiry is null, or expiry is at/before `now`;
- activate matches Server ID, exact pending `kid`, non-empty pending secret, and `pending_expires_at > now`;
- cancel matches Server ID plus exact pending `kid`;
- disable matches the Server ID and atomically clears current/pending material while setting mode `none`;
- bulk expired-pending cleanup clears only rows with non-null `pending_expires_at <= now`; application invokes it once before building list/status views, never once per Server.

Use `map[string]interface{}` updates when clearing strings, setting opt-in booleans false, or clearing nullable timestamps so Gorm cannot omit zero/nil values. On zero affected rows, reload inside the transaction to distinguish `repository.ErrNotFound`, pending conflict, wrong `kid`, and expiry. Activate success sets mode to `hs256`, promotes the pending secret/key, and clears all pending fields in that same conditional update.

An expired activate request must persist pending-field cleanup and then return `ErrMCPContextJWTPendingExpired`. Its cleanup is itself a conditional `UPDATE`: match Server ID, the exact expired pending `kid`, the exact `pending_expires_at` snapshot read by activate, and `pending_expires_at <= now`. If `RowsAffected == 0`, another transition won; reload and classify the current state, but never clear it. Do not return the expired sentinel from inside the transaction callback, because Gorm would roll the cleanup back. Instead, set an `expired` flag only after the conditional cleanup succeeds, return nil so the cleanup commits, and return the sentinel after `db.Transaction` succeeds. Keep the repository test that reloads the row after the sentinel and proves every pending field is empty, plus the barrier-controlled activate-versus-prepare test proving a newly prepared replacement survives the stale expired cleanup.

Implement policy updates with a `map[string]interface{}` so all three opt-ins can transition from true to false. After every successful mutation, reload through `GetServer`; never return a Gorm model.

- [ ] **Step 5: Run the repository tests and verify the green state**

```powershell
cd backend
go test ./internal/infra/persistence/postgres/mcp -run TestContextJWT -count=1
```

Expected: PASS, including concurrent prepare/activate cases on a shared file-backed SQLite database. The conditional-update contract—not SQLite lock emulation—is the evidence that the same SQL remains atomic on PostgreSQL.

- [ ] **Step 6: Commit**

```powershell
git add backend/internal/repository/mcp.go backend/internal/infra/persistence/postgres/mcp
git commit -m "feat: add mcp context key rotation"
```

### Task 4: Implement Signed-Context Application Governance

**Files:**
- Create: `backend/internal/application/mcp/context_jwt.go`
- Create: `backend/internal/application/mcp/context_jwt_test.go`
- Modify: `backend/internal/application/mcp/service.go`

**Interfaces:**
- Consumes: Task 3 repository operations, runtime config, `secretbox`, `crypto/rand`, and the existing A+B `ErrMCPServerNotFound`.
- Produces: the exact policy/status/result types and five service methods below. Task 6 maps them to HTTP and owns audit calls.

- [ ] **Step 1: Write application tests**

In `backend/internal/application/mcp/context_jwt_test.go`, use a fake `repository.MCPRepository` and set the package-private Service hooks to a fixed clock, a deterministic 32-byte reader, and a fixed `kid`. Name every top-level test with the `TestContextJWT` prefix. Assert:

- prepare returns the expected RawURL secret exactly once while repository stores only `v1:` ciphertext;
- query/update/activate/cancel/disable result types contain no secret field;
- configured status becomes false for blank current key/secret/audience or TTL outside 60..900;
- an unknown stored mode is reported publicly as `mode="none", configured=false`;
- invalid TTL and blank `PUBLIC_WEB_BASE_URL` reject prepare/activate;
- issuer tests reject relative/hostless URLs, non-HTTP schemes, userinfo, query, and fragment in every environment; production rejects HTTP; `https://chat.example.com///` normalizes only by removing trailing `/` and becomes `https://chat.example.com`;
- activate first loads the Server and accepts only an exact pending `kid`; it decrypts that pending ciphertext and runs `inframcp.ValidateSignedContextSecret` before the fake repository records any activate CAS call;
- corrupted/undecryptable pending ciphertext and decryptable but weak/non-canonical secrets (including a RawURL value that decodes to fewer than 32 bytes) return `ErrMCPContextJWTInvalidStorage`, leave current/pending fields unchanged, and make zero `ActivateContextJWTRotation` repository calls;
- cancel and disable still succeed when issuer configuration is invalid, and disable clears undecryptable/corrupted current and pending ciphertext without decrypting it;
- an expired pending key is omitted from status and a single bulk cleanup clears its encrypted pending material.

- [ ] **Step 2: Run the application tests and verify the red state**

```powershell
cd backend
go test ./internal/application/mcp -run TestContextJWT -count=1
```

Expected: FAIL at compile time because `ContextJWTPolicyInput`, `ContextJWTStatus`, `PrepareContextJWTRotationResult`, the five Service methods, and the deterministic Service hooks do not exist.

- [ ] **Step 3: Define the exact application contract and deterministic hooks**

```go
type ContextJWTPolicyInput struct {
	ExpiresSeconds int
	IncludeName    bool
	IncludeEmail   bool
	IncludeRole    bool
}

type ContextJWTStatus struct {
	ServerPublicID  string
	Mode             string
	Configured       bool
	Issuer           string
	Audience         string
	KeyID            string
	ExpiresSeconds   int
	IncludeName      bool
	IncludeEmail     bool
	IncludeRole      bool
	PendingKeyID    string
	PendingExpiresAt *time.Time
}

type PrepareContextJWTRotationResult struct {
	ServerPublicID string
	Header          string
	Algorithm       string
	Secret          string
	Issuer          string
	Audience        string
	KeyID           string
	ExpiresSeconds  int
}

func (s *Service) UpdateContextJWTPolicy(
	ctx context.Context,
	serverID uint,
	input ContextJWTPolicyInput,
) (ContextJWTStatus, error)

func (s *Service) PrepareContextJWTRotation(
	ctx context.Context,
	serverID uint,
) (PrepareContextJWTRotationResult, error)

func (s *Service) ActivateContextJWTRotation(
	ctx context.Context,
	serverID uint,
	kid string,
) (ContextJWTStatus, error)

func (s *Service) CancelContextJWTRotation(
	ctx context.Context,
	serverID uint,
	kid string,
) (ContextJWTStatus, error)

func (s *Service) DisableContextJWT(
	ctx context.Context,
	serverID uint,
) (ContextJWTStatus, error)
```

Only the prepare result has `Secret`. `ServerPublicID` is application-only audit metadata: Task 6 copies it to the top-level Server DTO when rendering a Server, but deliberately omits it from direct status/prepare response objects because the route is already scoped by the numeric Server ID and the frontend already holds the Server's `publicID`.

Add these package-private fields to the existing `Service`, initialize them in `NewServiceWithRuntime`, and let same-package tests replace them:

```go
contextJWTNow      func() time.Time // production: time.Now
contextJWTRandom   io.Reader        // production: crypto/rand.Reader
contextJWTNewKeyID func() string     // production: "ctx_" + dashless UUID
```

Define the exact application sentinels:

```go
var (
	ErrMCPContextJWTInvalidPolicy    = errors.New("invalid mcp context jwt policy")
	ErrMCPContextJWTUnavailable      = errors.New("mcp context jwt unavailable")
	ErrMCPContextJWTPendingExists    = errors.New("mcp context jwt pending rotation exists")
	ErrMCPContextJWTRotationConflict = errors.New("mcp context jwt rotation conflict")
	ErrMCPContextJWTRotationExpired  = errors.New("mcp context jwt rotation expired")
	ErrMCPContextJWTInvalidStorage   = errors.New("mcp context jwt storage invalid")
)
```

- [ ] **Step 4: Implement status, prepare, policy, activate, cancel, and disable**

Implement:

```go
func buildContextJWTStatus(
	server domainmcp.Server,
	issuer string,
	now time.Time,
) ContextJWTStatus

func normalizeContextJWTIssuer(raw string, env string) (string, error)

func mapContextJWTRepositoryError(err error) error
```

`buildContextJWTStatus` never decrypts key material. It copies `ServerPublicID`, normalizes public `Mode` to exactly `none` or `hs256` (unknown storage reports `none`), and sets `Configured=true` only when the stored mode is exactly `hs256`, current ciphertext/key ID/audience are non-empty, and TTL is 60..900. Pending fields are included only when `PendingExpiresAt != nil && now.Before(*PendingExpiresAt)`; pending alone is never configured.

`normalizeContextJWTIssuer` trims whitespace and trailing `/`, requires an absolute `http` or `https` URL with host and no userinfo/query/fragment, and in production additionally requires `https`. Prepare and activate require it; status may display the normalized configured value without failing list operations. Cancel and disable never validate issuer and never decrypt current/pending ciphertext, so emergency cleanup remains available.

Policy update validates TTL 60..900 and delegates all four fields to Task 3. Prepare loads the Server, requires non-empty audience and valid policy/issuer, reads exactly 32 bytes with `io.ReadFull`, encodes with `base64.RawURLEncoding`, encrypts immediately, and stores pending with `now`/`now+24h`.

Activate uses this exact pre-CAS order:

1. trim and require non-empty `kid`, then validate issuer;
2. call `s.repo.GetServer(ctx, serverID)` and map not-found/repository errors;
3. require `server.ContextJWTPendingKeyID == kid`; a blank or different value returns `ErrMCPContextJWTRotationConflict` without attempting decryption or CAS;
4. decrypt exactly `server.ContextJWTPendingSecretEnc` with the current `DataEncryptionKey`; a blank ciphertext or any decrypt failure maps to `ErrMCPContextJWTInvalidStorage`;
5. call `inframcp.ValidateSignedContextSecret(plaintext)`; any weak, padded, non-canonical, or wrong-length secret maps to `ErrMCPContextJWTInvalidStorage`;
6. only after successful validation call `s.repo.ActivateContextJWTRotation(ctx, serverID, kid, s.contextJWTNow())` and map its CAS result.

The repository CAS remains the authority for expiry and concurrent mutation, so validation never substitutes for the Task 3 predicate. Keep the decrypted value local to this method and never return or log it. Cancel validates non-empty trimmed `kid` but not issuer and never decrypts. Disable delegates without issuer/decryption. Map Task 3 sentinels one-for-one to the application sentinels above and map `repository.ErrNotFound` to A+B `ErrMCPServerNotFound`. Never include secret, ciphertext, email, issuer input, or JWT in an error.

- [ ] **Step 5: Run the application tests and verify the green state**

```powershell
cd backend
go test ./internal/application/mcp -run TestContextJWT -count=1
```

Expected: PASS; deterministic prepare, safe result shapes, issuer/policy validation, pre-CAS pending-secret validation with zero promotion on invalid storage, status normalization, emergency cancel/disable, and expired cleanup all pass.

- [ ] **Step 6: Commit**

```powershell
git add backend/internal/application/mcp
git commit -m "feat: govern mcp signed context"
```

### Task 5: Inject Signed Context into Chat, Probe, and Sync

**Files:**
- Modify: `backend/internal/infra/mcp/client.go`
- Modify: `backend/internal/infra/mcp/client_test.go`
- Modify: `backend/internal/application/mcp/service.go`
- Modify: `backend/internal/application/conversation/service_mcp_tools.go`
- Modify: `backend/internal/application/conversation/service_mcp_tools_test.go`

**Interfaces:**
- Consumes: A+B `CallConfig.Context`, Task 2 signer, encrypted Server secret/policy.
- Produces: optional `CallConfig.SignedContext` and one signed Header reused across an A+B per-operation session.

- [ ] **Step 1: Write lifecycle Header tests**

In `backend/internal/infra/mcp/client_test.go`, add the exact tests `TestClientContextJWTUsesOneTokenAcrossOperation`, `TestClientContextJWTRejectsCustomHeaderOverride`, `TestClientContextJWTFailsClosedWithoutSigner`, and `TestClientContextJWTFailsClosedOnSignerError`. Configure a fake signer and assert initialize, initialized, call/list, and DELETE receive the exact same non-empty `X-DEEIX-Context` token. A direct `CustomHeaders["X-DEEIX-Context"]` remains rejected by the unchanged A+B blacklist and sends zero requests; there is no validator bypass for an internally signed token. A non-nil `SignedContext` without a signer returns `ErrContextSignerUnavailable`; signer errors are returned unchanged through their safe sentinel; both paths send zero MCP HTTP requests.

In `backend/internal/application/mcp/service_test.go` add `TestContextJWTBuildCallConfigDisabled`, `TestContextJWTBuildCallConfigConfigured`, `TestContextJWTBuildCallConfigRejectsInvalidStorage`, and `TestContextJWTBuildCallConfigRequiresIssuer`; in `backend/internal/application/conversation/service_mcp_tools_test.go` add `TestContextJWTConversationBuilderPreservesModes`. Use table rows for mode `none`, valid `hs256`, unknown mode, blank active fields, DataEncryptionKey mismatch, padded/malformed/31-byte/non-canonical decrypted secrets, missing issuer, and chat/probe/sync context preservation. Every invalid row returns the exact Task 4 sentinel and sends zero HTTP requests.

- [ ] **Step 2: Run the client/builder tests and verify the red state**

```powershell
cd backend
go test ./internal/infra/mcp ./internal/application/mcp ./internal/application/conversation -run "Test(ClientContextJWT|ContextJWT)" -count=1
```

Expected: FAIL at compile time because `CallConfig.SignedContext`, `Client.contextSigner`, and the signed-context extension of `BuildCallConfig` do not exist; nil-signer and invalid-storage assertions also fail against the A+B client/builder.

- [ ] **Step 3: Extend Client construction and sign once per operation**

Add `contextSigner ContextSigner` to `Client`. Production constructors use `NewJWTContextSigner()`. Tests may use a fake signer. Append:

```go
type CallConfig struct {
	BaseURL       string
	AuthToken     string
	TimeoutMS     int
	CustomHeaders map[string]string
	Context       TemplateContext
	SignedContext *SignedContextConfig
}
```

Keep A+B `ValidateRenderedCustomHeaders(cfg.CustomHeaders)` unchanged and run it before signing. Never insert `X-DEEIX-Context` into `CustomHeaders` or any map accepted by that validator. Add an unexported per-operation carrier:

```go
type operationHeaders struct {
	Custom             map[string]string
	SignedContextToken string
}
```

`ListTools`/`CallTool` validate and clone custom Headers, then—when `SignedContext != nil`—require a non-nil signer and place the signed value only in `operationHeaders.SignedContextToken`. Pass that immutable carrier through initialize, initialized, list/call, and DELETE. The HTTP request builder sets custom Headers first, then auth/content/accept/session/protocol Headers, then sets `X-DEEIX-Context` from the dedicated token field last. On a nil signer return `ErrContextSignerUnavailable`; on signer error return it without issuing any request. Phase D may re-sign this same immutable config/context for each physical request, but it retains this separate transport-owned field.

- [ ] **Step 4: Extend the one shared A+B call-config builder**

Extend `application/mcp.Service.BuildCallConfig`; do not assemble signed config in conversation. Keep its A+B signature unchanged. When Server mode is `hs256`:

1. decrypt `ContextJWTSecretEnc`;
2. call Task 2 `inframcp.ValidateSignedContextSecret`, then require current `KeyID`, audience, normalized issuer, and valid TTL;
3. construct `SignedContextConfig` with stored opt-ins;
4. attach it to the existing CallConfig.

Mode `none` attaches nil even if no key exists. Unknown mode, weak/corrupt decrypted secret, missing current key/audience, or invalid TTL returns `ErrMCPContextJWTInvalidStorage`. Missing/invalid runtime issuer returns `ErrMCPContextJWTUnavailable`. A secretbox decryption failure also returns `ErrMCPContextJWTInvalidStorage`; none of these errors may contain the underlying value/ciphertext. Sync leaves A+B TemplateContext user fields empty; signer converts mode sync to system subject. Probe/chat preserve actual user public ID. Conversation continues to consume the same builder through its A+B narrow interface, so chat/probe/sync cannot drift.

- [ ] **Step 5: Keep all errors and observability value-free**

Do not format `CallConfig` or `SignedContextConfig` with `%+v`. Error text identifies only missing/invalid configuration class and Server public ID. Traces record `signed_context=true` and `kid`, not token.

- [ ] **Step 6: Run integration/race tests and verify the green state**

```powershell
cd backend
go test ./internal/infra/mcp ./internal/application/mcp ./internal/application/conversation -count=1
go test -race ./internal/infra/mcp ./internal/application/mcp ./internal/application/conversation -run "Test(ClientContextJWT|ContextJWT)" -count=1
```

Expected: PASS without races; lifecycle requests share one assertion, invalid signer/storage/issuer cases send zero requests, and all three context modes use the shared builder.

- [ ] **Step 7: Commit**

```powershell
git add backend/internal/infra/mcp backend/internal/application/mcp backend/internal/application/conversation
git commit -m "feat: send signed mcp context"
```

### Task 6: Expose Rotation APIs, Audit, and Swagger Safely

**Files:**
- Modify: `backend/internal/application/mcp/context_jwt.go`
- Modify: `backend/internal/application/mcp/context_jwt_test.go`
- Modify: `backend/internal/application/mcp/service.go`
- Modify: `backend/internal/transport/http/mcp/dto.go`
- Modify: `backend/internal/transport/http/mcp/handler.go`
- Modify: `backend/internal/transport/http/mcp/router.go`
- Create: `backend/internal/transport/http/mcp/handler_context_jwt_test.go`
- Modify: `backend/internal/shared/response/error_code.go`
- Modify: `backend/docs/docs.go`
- Modify: `backend/docs/swagger.json`
- Modify: `backend/docs/swagger.yaml`

**Interfaces:**
- Consumes: Task 4 governance use cases.
- Produces: five fixed routes, one-time no-store response, non-secret Server status, stable errors, and Swagger.

- [ ] **Step 1: Write failing application-view and handler tests first**

In `backend/internal/application/mcp/context_jwt_test.go`, add `TestContextJWTDescribeServer` and `TestContextJWTListServersClearsExpiredPendingOnce`. Assert normalized mode/configured state, one bulk cleanup before one list query, no per-row cleanup, and no secret decryption.

In `backend/internal/transport/http/mcp/handler_context_jwt_test.go`, add the exact top-level tests `TestContextJWTServerResponses`, `TestContextJWTRoutes`, `TestContextJWTPrepareNoStore`, `TestContextJWTErrorMapping`, and `TestContextJWTAuditRedaction` for:

- list/create/update/reorder Server responses containing `publicID` and a nested non-secret `contextJWT`;
- each of the five routes calling the matching Task 4 Service method with parsed Server ID, trimmed `kid`, policy fields, and request context;
- prepare returning the exact one-time fields plus `Cache-Control: no-store`/`Pragma: no-cache`;
- update/activate/cancel/disable returning `ContextJWTStatusResponse` directly inside the standard envelope;
- every error table row below, including exact HTTP status, `errorCode`, `data:null`, and request ID;
- success/error audit action, actor, request ID, IP, user agent, numeric Server resource ID, safe public ID/`kid`/policy/outcome/code fields, and zero secret/JWT/email/ciphertext matches.

Use an `httptest` Gin router, the real application Service with a fake repository, and the A+B capturing audit writer. Seed deterministic encrypted current/pending fixture strings containing unique leak markers; tests search the entire serialized HTTP envelope and captured audit detail for those markers.

- [ ] **Step 2: Run the focused tests and verify the red state**

```powershell
cd backend
go test ./internal/application/mcp ./internal/transport/http/mcp -run "TestContextJWT" -count=1
```

Expected: FAIL at compile time because `ServerView`, `DescribeServer`, the transport DTO mappers, and all five Handler methods do not exist. If the failure is only a fixture/setup error, fix the test setup and rerun until the missing production API is the reason.

- [ ] **Step 3: Add the application Server view and exact HTTP DTOs**

```go
type UpdateContextJWTRequest struct {
	ExpiresSeconds int  `json:"expiresSeconds" binding:"required,min=60,max=900"`
	IncludeName    bool `json:"includeName"`
	IncludeEmail   bool `json:"includeEmail"`
	IncludeRole    bool `json:"includeRole"`
}

type ContextJWTStatusResponse struct {
	Mode             string     `json:"mode"`
	Configured       bool       `json:"configured"`
	Issuer           string     `json:"issuer"`
	Audience         string     `json:"audience"`
	KeyID            string     `json:"keyID"`
	ExpiresSeconds   int        `json:"expiresSeconds"`
	IncludeName      bool       `json:"includeName"`
	IncludeEmail     bool       `json:"includeEmail"`
	IncludeRole      bool       `json:"includeRole"`
	PendingKeyID     string     `json:"pendingKeyID,omitempty"`
	PendingExpiresAt *time.Time `json:"pendingExpiresAt,omitempty"`
}

type PrepareContextJWTRotationResponse struct {
	Header         string `json:"header"`
	Algorithm      string `json:"algorithm"`
	Secret         string `json:"secret"`
	Issuer         string `json:"issuer"`
	Audience       string `json:"audience"`
	KeyID          string `json:"keyID"`
	ExpiresSeconds int    `json:"expiresSeconds"`
}

type ServerView struct {
	Server     domainmcp.Server
	ContextJWT ContextJWTStatus
}

func (s *Service) DescribeServer(server domainmcp.Server) ServerView

func toContextJWTStatusResponse(
	status appmcp.ContextJWTStatus,
) ContextJWTStatusResponse

func toPrepareContextJWTRotationResponse(
	result appmcp.PrepareContextJWTRotationResult,
) PrepareContextJWTRotationResponse
```

Append these exact fields to the existing A+B `ServerResponse`:

```go
PublicID   string                   `json:"publicID"`
ContextJWT ContextJWTStatusResponse `json:"contextJWT"`
```

Change `toServerResponse` to accept `appmcp.ServerView`, copy only public Server fields plus the nested status, and never copy `AuthTokenEnc`, current/pending context ciphertext, or pending creation time. `ListServers` calls `ClearExpiredContextJWTPending(ctx, s.contextJWTNow())` exactly once and returns the normal Server rows; its Handler maps each with `DescribeServer`. Create/update/reorder map their already loaded rows with `DescribeServer`, avoiding Handler config access and N+1 repository queries.

- [ ] **Step 4: Implement the five handlers, routes, explicit error mapping, and audit**

Register:

```go
group.PATCH("/servers/:id/context-jwt", m.Handler.UpdateContextJWT)
group.POST("/servers/:id/context-jwt/rotations", m.Handler.PrepareContextJWTRotation)
group.POST("/servers/:id/context-jwt/rotations/:kid/activate", m.Handler.ActivateContextJWTRotation)
group.DELETE("/servers/:id/context-jwt/rotations/:kid", m.Handler.CancelContextJWTRotation)
group.DELETE("/servers/:id/context-jwt", m.Handler.DisableContextJWT)
```

Implement exactly these Handler-to-Service calls:

```go
UpdateContextJWT:
	status, err := h.service.UpdateContextJWTPolicy(
		c.Request.Context(),
		serverID,
		appmcp.ContextJWTPolicyInput{
			ExpiresSeconds: req.ExpiresSeconds,
			IncludeName: req.IncludeName,
			IncludeEmail: req.IncludeEmail,
			IncludeRole: req.IncludeRole,
		},
	)

PrepareContextJWTRotation:
	result, err := h.service.PrepareContextJWTRotation(c.Request.Context(), serverID)

ActivateContextJWTRotation:
	status, err := h.service.ActivateContextJWTRotation(
		c.Request.Context(), serverID, strings.TrimSpace(c.Param("kid")),
	)

CancelContextJWTRotation:
	status, err := h.service.CancelContextJWTRotation(
		c.Request.Context(), serverID, strings.TrimSpace(c.Param("kid")),
	)

DisableContextJWT:
	status, err := h.service.DisableContextJWT(c.Request.Context(), serverID)
```

All five use the existing `parseIDParam`. Update binds `UpdateContextJWTRequest`. On success, update/activate/cancel/disable call `response.Success(c, toContextJWTStatusResponse(status))`. Prepare sets:

```go
c.Header("Cache-Control", "no-store")
c.Header("Pragma", "no-cache")
response.Success(c, toPrepareContextJWTRotationResponse(result))
```

No handler logs a result or response object. Never pass an application result directly to `response.Success`; explicit mappers are the one-time secret boundary.

Add `writeContextJWTServiceError(c, err) string`; it must use `response.ErrorWithCode`, return the selected code for audit, and implement this exact table:

| Sentinel | `errorCode` | HTTP |
| --- | --- | --- |
| `ErrMCPContextJWTInvalidPolicy` | `mcp.context_jwt.invalid_policy` | 400 |
| `ErrMCPContextJWTUnavailable` | `mcp.context_jwt.unavailable` | 503 |
| `ErrMCPContextJWTPendingExists` | `mcp.context_jwt.pending_exists` | 409 |
| `ErrMCPContextJWTRotationConflict` | `mcp.context_jwt.rotation_conflict` | 409 |
| `ErrMCPContextJWTRotationExpired` | `mcp.context_jwt.rotation_expired` | 409 |
| `ErrMCPContextJWTInvalidStorage` | `mcp.context_jwt.invalid_storage` | 500 |
| A+B `ErrMCPServerNotFound` | `mcp.server.not_found` | 404 |

Unknown errors map to `internal.error`/500. Add the six context-JWT code/message entries to `backend/internal/shared/response/error_code.go`; do not rely on `InferErrorCode` to derive these names.

After both success and error, reuse A+B `RecordAudit` with actions `mcp.context_jwt.policy_update`, `mcp.context_jwt.rotation_prepare`, `mcp.context_jwt.rotation_activate`, `mcp.context_jwt.rotation_cancel`, and `mcp.context_jwt.disable`. Set `AuditInput.ResourceID` to the numeric route Server ID so missing-Server failures remain auditable. Add this exact input and builder so each handler supplies only values that actually exist in its branch:

```go
type contextJWTAuditDetailInput struct {
	Outcome        string
	ErrorCode      string
	ServerPublicID string
	KeyID          string
	Policy         *UpdateContextJWTRequest
}

var (
	safeMCPPublicIDAuditPattern = regexp.MustCompile(`^mcp_[A-Za-z0-9_-]+$`)
	safeContextKIDAuditPattern  = regexp.MustCompile(`^ctx_[A-Za-z0-9_-]+$`)
)

func buildContextJWTAuditDetail(input contextJWTAuditDetailInput) map[string]interface{} {
	detail := map[string]interface{}{"outcome": input.Outcome}
	if input.ErrorCode != "" {
		detail["error_code"] = input.ErrorCode
	}
	if len(input.ServerPublicID) <= 128 && safeMCPPublicIDAuditPattern.MatchString(input.ServerPublicID) {
		detail["server_public_id"] = input.ServerPublicID
	}
	if len(input.KeyID) <= 64 && safeContextKIDAuditPattern.MatchString(input.KeyID) {
		detail["kid"] = input.KeyID
	}
	if input.Policy != nil {
		detail["expires_seconds"] = input.Policy.ExpiresSeconds
		detail["include_name"] = input.Policy.IncludeName
		detail["include_email"] = input.Policy.IncludeEmail
		detail["include_role"] = input.Policy.IncludeRole
	}
	return detail
}
```

`Outcome` is exactly `success` or `error`; `ErrorCode` is blank on success and the return value of `writeContextJWTServiceError` on error. For a successful update pass `status.ServerPublicID/status.KeyID` plus `Policy: &req`; for prepare pass `result.ServerPublicID/result.KeyID`; for activate pass `status.ServerPublicID/status.KeyID`; for cancel pass `status.ServerPublicID` plus the accepted trimmed route `kid`; for disable pass `status.ServerPublicID/status.KeyID`. Error branches pass blank public ID/key ID and never attach a policy unless update binding succeeded. The builder always emits `outcome`, emits `error_code` only on errors, emits the four policy keys only for update, and emits identifiers only after the stated byte-limit and ASCII-pattern checks.

Build the `AuditInput` with `middleware.MustUserID`, `middleware.MustRequestID`, `c.ClientIP()`, and `c.Request.UserAgent()`. Never derive public ID from audience. On an error, do not include a raw error string, unvalidated path `kid`, issuer, audience, secret, JWT, ciphertext, or email.

- [ ] **Step 5: Run the focused tests and verify the green state**

```powershell
cd backend
go test ./internal/application/mcp ./internal/transport/http/mcp -run "TestContextJWT" -count=1
```

Expected: PASS; all five routes, exact response shapes/error codes, one-time no-store behavior, safe Server views, and audit assertions pass.

- [ ] **Step 6: Annotate handlers and regenerate Swagger**

Add Bearer-security Swagger annotations for all five handlers, using `UpdateContextJWTRequest`, `ContextJWTStatusResponse`, and `PrepareContextJWTRotationResponse` rather than application/domain types. Then run:

```powershell
cd backend
make swagger
rg -n "context-jwt" docs/swagger.yaml
```

Expected: the generator exits 0 and all five exact routes appear; generated schemas contain `secret` only in the prepare response and contain no ciphertext fields.

- [ ] **Step 7: Commit**

```powershell
git add backend/internal/application/mcp backend/internal/transport/http/mcp backend/internal/shared/response/error_code.go backend/docs
git commit -m "feat: expose mcp context key rotation"
```

### Task 7: Add Typed Frontend Signed-Context APIs

**Files:**
- Modify: `frontend/features/admin/api/mcp.types.ts`
- Modify: `frontend/features/admin/api/mcp.ts`

**Interfaces:**
- Consumes: Task 6 JSON.
- Produces: typed policy, prepare, activate, cancel, and disable calls; only prepare result has `secret`.

- [ ] **Step 1: Add safe status and one-time types**

```ts
export type MCPContextJWTStatus = {
  mode: "none" | "hs256";
  configured: boolean;
  issuer: string;
  audience: string;
  keyID: string;
  expiresSeconds: number;
  includeName: boolean;
  includeEmail: boolean;
  includeRole: boolean;
  pendingKeyID?: string;
  pendingExpiresAt?: string;
};

export type MCPContextJWTPolicyPayload = {
  expiresSeconds: number;
  includeName: boolean;
  includeEmail: boolean;
  includeRole: boolean;
};

export type MCPContextJWTPrepareResult = {
  header: "X-DEEIX-Context";
  algorithm: "HS256";
  secret: string;
  issuer: string;
  audience: string;
  keyID: string;
  expiresSeconds: number;
};
```

Add `contextJWT: MCPContextJWTStatus` and `publicID: string` to the Server DTO.

- [ ] **Step 2: Add five API functions**

Use `authedRequest` and `pathParam` for both Server ID and `kid`. Add these exact exports:

```ts
export function updateAdminMCPContextJWTPolicy(
  accessToken: string,
  serverID: number,
  payload: MCPContextJWTPolicyPayload,
): Promise<MCPContextJWTStatus>;

export function prepareAdminMCPContextJWTRotation(
  accessToken: string,
  serverID: number,
): Promise<MCPContextJWTPrepareResult>;

export function activateAdminMCPContextJWTRotation(
  accessToken: string,
  serverID: number,
  kid: string,
): Promise<MCPContextJWTStatus>;

export function cancelAdminMCPContextJWTRotation(
  accessToken: string,
  serverID: number,
  kid: string,
): Promise<MCPContextJWTStatus>;

export function disableAdminMCPContextJWT(
  accessToken: string,
  serverID: number,
): Promise<MCPContextJWTStatus>;
```

The prepare function is the only frontend API type containing `secret`; the remaining functions return the direct status object from Task 6.

- [ ] **Step 3: Run the honest frontend type/build gate**

This API/type-only task has no new pure feature model and no component/E2E runner; do not manufacture a red step from nonexistent DTO fixtures. Run the existing Node model suite plus the required lint/build gates:

```powershell
cd frontend
pnpm test
pnpm lint
pnpm build
```

Expected: all three commands exit 0; TypeScript proves route return types and required Server DTO fields are consistent, and no frontend type includes current/pending ciphertext or a normal-status `secret`.

- [ ] **Step 4: Commit**

```powershell
git add frontend/features/admin/api
git commit -m "feat: add mcp signed context api"
```

### Task 8: Build the Context Panel, One-Time Dialog, and Acceptance Gate

**Files:**
- Create: `frontend/features/admin/model/mcp-context-jwt.ts`
- Create: `frontend/features/admin/model/mcp-context-jwt.test.mjs`
- Create: `frontend/features/admin/components/sections/tools/mcp-context-jwt-panel.tsx`
- Create: `frontend/features/admin/components/sections/tools/mcp-context-secret-dialog.tsx`
- Modify: `frontend/features/admin/components/sections/tools/mcp-server-dialog.tsx`
- Modify: `frontend/i18n/messages/en-US/admin-tools.json`
- Modify: `frontend/i18n/messages/zh-CN/admin-tools.json`
- Modify: `frontend/i18n/messages/en-US/errors.json`
- Modify: `frontend/i18n/messages/zh-CN/errors.json`

**Interfaces:**
- Consumes: Task 7 APIs and A+B Server dialog.
- Produces: safe policy UI, prepare/activate/cancel/disable controls, ephemeral secret display, and phase C verification.

- [ ] **Step 1: Write failing signed-context model tests**

Create `frontend/features/admin/model/mcp-context-jwt.test.mjs` with these named Node tests:

```js
import assert from "node:assert/strict";
import test from "node:test";

import {
  canActivatePreparedContext,
  normalizeMCPContextJWTPolicy,
} from "./mcp-context-jwt.ts";

test("MCP signed context normalizes a valid policy", () => {
  assert.deepEqual(
    normalizeMCPContextJWTPolicy({
      expiresSeconds: "300",
      includeName: true,
      includeEmail: false,
      includeRole: true,
    }),
    {
      ok: true,
      payload: {
        expiresSeconds: 300,
        includeName: true,
        includeEmail: false,
        includeRole: true,
      },
    },
  );
});

test("MCP signed context rejects invalid TTL text and range", () => {
  assert.deepEqual(
    normalizeMCPContextJWTPolicy({
      expiresSeconds: "",
      includeName: false,
      includeEmail: false,
      includeRole: false,
    }),
    { ok: false, error: "expiresSecondsRequired" },
  );
  assert.equal(
    normalizeMCPContextJWTPolicy({
      expiresSeconds: "60.5",
      includeName: false,
      includeEmail: false,
      includeRole: false,
    }).error,
    "expiresSecondsInteger",
  );
  for (const expiresSeconds of ["59", "901"]) {
    assert.equal(
      normalizeMCPContextJWTPolicy({
        expiresSeconds,
        includeName: false,
        includeEmail: false,
        includeRole: false,
      }).error,
      "expiresSecondsRange",
    );
  }
});

test("MCP signed context activates only the matching unexpired prepared key", () => {
  const nowMS = Date.parse("2026-07-10T08:00:00Z");
  const status = {
    mode: "hs256",
    configured: true,
    issuer: "https://chat.example.com",
    audience: "urn:deeix:mcp:mcp_example",
    keyID: "ctx_current",
    expiresSeconds: 300,
    includeName: false,
    includeEmail: false,
    includeRole: false,
    pendingKeyID: "ctx_pending",
    pendingExpiresAt: "2026-07-10T09:00:00Z",
  };
  const prepared = {
    header: "X-DEEIX-Context",
    algorithm: "HS256",
    secret: "one-time-secret",
    issuer: status.issuer,
    audience: status.audience,
    keyID: "ctx_pending",
    expiresSeconds: 300,
  };

  assert.equal(canActivatePreparedContext(status, prepared, nowMS), true);
  assert.equal(
    canActivatePreparedContext(
      { ...status, pendingKeyID: "ctx_other" },
      prepared,
      nowMS,
    ),
    false,
  );
  assert.equal(
    canActivatePreparedContext(
      { ...status, pendingExpiresAt: "2026-07-10T07:59:59Z" },
      prepared,
      nowMS,
    ),
    false,
  );
  assert.equal(
    canActivatePreparedContext(status, { ...prepared, secret: "" }, nowMS),
    false,
  );
});
```

Also add assertions for TTL `60`/`900` success, whitespace trimming, missing pending fields, invalid pending timestamp, empty/mismatched `kid`, and `pendingExpiresAt == now` returning false.

- [ ] **Step 2: Run the model tests and verify the red state**

```powershell
cd frontend
pnpm test --test-name-pattern="MCP signed context"
```

Expected: FAIL because `mcp-context-jwt.ts` does not exist.

- [ ] **Step 3: Implement the complete exported model**

Create `frontend/features/admin/model/mcp-context-jwt.ts` with this complete contract:

```ts
import type {
  MCPContextJWTPolicyPayload,
  MCPContextJWTPrepareResult,
  MCPContextJWTStatus,
} from "@/features/admin/api/mcp.types";

export type MCPContextJWTPolicyDraft = {
  expiresSeconds: string;
  includeName: boolean;
  includeEmail: boolean;
  includeRole: boolean;
};

export type MCPContextJWTPolicyError =
  | "expiresSecondsRequired"
  | "expiresSecondsInteger"
  | "expiresSecondsRange";

export type MCPContextJWTPolicyNormalization =
  | { ok: true; payload: MCPContextJWTPolicyPayload }
  | { ok: false; error: MCPContextJWTPolicyError };

export function normalizeMCPContextJWTPolicy(
  draft: MCPContextJWTPolicyDraft,
): MCPContextJWTPolicyNormalization {
  const raw = draft.expiresSeconds.trim();
  if (!raw) {
    return { ok: false, error: "expiresSecondsRequired" };
  }
  if (!/^[0-9]+$/.test(raw)) {
    return { ok: false, error: "expiresSecondsInteger" };
  }
  const expiresSeconds = Number(raw);
  if (!Number.isSafeInteger(expiresSeconds)) {
    return { ok: false, error: "expiresSecondsInteger" };
  }
  if (expiresSeconds < 60 || expiresSeconds > 900) {
    return { ok: false, error: "expiresSecondsRange" };
  }
  return {
    ok: true,
    payload: {
      expiresSeconds,
      includeName: draft.includeName,
      includeEmail: draft.includeEmail,
      includeRole: draft.includeRole,
    },
  };
}

export function canActivatePreparedContext(
  status: MCPContextJWTStatus,
  prepared: MCPContextJWTPrepareResult,
  nowMS: number = Date.now(),
): boolean {
  const pendingKeyID = status.pendingKeyID?.trim() ?? "";
  const preparedKeyID = prepared.keyID.trim();
  if (
    !pendingKeyID ||
    !preparedKeyID ||
    pendingKeyID !== preparedKeyID ||
    !prepared.secret.trim()
  ) {
    return false;
  }
  const pendingExpiresAt = status.pendingExpiresAt
    ? Date.parse(status.pendingExpiresAt)
    : Number.NaN;
  return Number.isFinite(pendingExpiresAt) && pendingExpiresAt > nowMS;
}
```

Keep this file pure: no React, browser globals, API calls, timers, or translation imports.

- [ ] **Step 4: Run the same model tests and verify the green state**

```powershell
cd frontend
pnpm test --test-name-pattern="MCP signed context"
```

Expected: PASS for normalization and activation eligibility, including exact expiry-boundary behavior.

- [ ] **Step 5: Keep plaintext secret in child-local state**

The panel receives status and callbacks but no secret prop. The secret dialog owns:

```tsx
const [prepared, setPrepared] =
  React.useState<MCPContextJWTPrepareResult | null>(null);

const handleOpenChange = (open: boolean) => {
  if (!open) setPrepared(null);
  setOpen(open);
};
```

Prepare assigns response directly to this state. Then request a parent Server-list/status refresh while keeping the dialog mounted and its `prepared` state child-local. The parent receives only the non-secret refreshed DTO. If refresh fails, keep the one-time dialog open, show a safe retry-status error, and do not enable Activate until the current pending `kid` is confirmed. Close, cancel, unmount, and successful activation clear the secret state. Never pass it to toast or parent.

- [ ] **Step 6: Implement policy controls using the tested model**

Initialize an `MCPContextJWTPolicyDraft` from the current status, validate it with `normalizeMCPContextJWTPolicy`, and pass only the successful payload to `updateAdminMCPContextJWTPolicy`. Use numeric TTL input constrained to 60..900 and three unchecked-by-default claim checkboxes. Render the three exact model error keys locally. Display immutable header/issuer/audience and configured/current/pending `kid`. Explain that role is a signed attribute, not an automatic authorization grant.

- [ ] **Step 7: Implement rotation controls**

Prepare calls `prepareAdminMCPContextJWTRotation` and opens the one-time dialog with copy action. After the non-secret refresh, enable Activate only through `canActivatePreparedContext(status, prepared)` and require confirmation that the target Server accepts it. Activate/cancel/disable use the exact Task 7 APIs; cancel clears pending, and disable requires destructive confirmation and clears current plus pending.

The repository has no frontend component/E2E test runner, so record this as a manual browser acceptance checklist rather than claiming `pnpm build` proves state behavior: prepare → keep dialog open → refresh non-secret Server list → observe matching pending `kid` enables Activate → confirm the secret remains only in the dialog and never appears in parent props, toast, console, or network responses other than prepare.

- [ ] **Step 8: Add localized security copy**

Add English/Chinese text for the three local policy errors, one-time visibility, target Secret Manager deployment, dual-key verification window, PII opt-ins, no automatic rollback, and no secret recovery. Add `errors.json` keys under `mcp.contextJwt` for `invalidPolicy`, `unavailable`, `pendingExists`, `rotationConflict`, `rotationExpired`, and `invalidStorage`, matching Task 6 error codes after underscore-to-camel conversion.

- [ ] **Step 9: Run frontend gate**

```powershell
cd frontend
pnpm test
pnpm lint
pnpm build
```

Expected: all three commands exit 0.

- [ ] **Step 10: Run backend phase C gate**

```powershell
cd backend
go test -race ./internal/infra/mcp ./internal/application/mcp ./internal/application/conversation ./internal/infra/persistence/schema ./internal/infra/persistence/postgres/mcp ./internal/transport/http/mcp -count=1
make test
go vet ./...
make swagger
make build
```

Expected: every command exits 0.

- [ ] **Step 11: Run audience and secret-exposure acceptance**

Sign Server A context and validate it with Server B expected audience while deliberately using the same test HMAC bytes; validation must fail on audience. Search captured HTTP, JSON responses, audit rows, system events, trace exports, and `last_error` for the prepared secret, emitted JWT, and test email; all searches return zero matches outside test fixtures.

- [ ] **Step 12: Commit UI and any acceptance corrections**

```powershell
git add frontend/features/admin/model frontend/features/admin/components/sections/tools frontend/i18n/messages backend
git commit -m "feat: add mcp signed context controls"
```

Do not create a commit if only verification ran and no files changed.
