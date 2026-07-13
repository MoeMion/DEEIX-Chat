# MCP Custom Header Toggle, Signed Template, and Go Demo Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persistent custom-Header switch, move signed context injection into the DEEIX Header template contract, expose backend-authoritative warnings and binding state, update the admin UI, and ship a secure minimal Go MCP identity-verification demo.

**Architecture:** Persistence owns the on/off state; the backend Header parser owns token legality, signed binding discovery, blacklist enforcement, and preview state; application services combine that analysis with stored signing-policy state; transport signs once for each physical HTTP request; the frontend renders only backend-returned authority. The demo is an independent Go module that authenticates a static Bearer token, optionally verifies DEEIX signed context, and exposes one `identity_check` MCP tool.

**Tech Stack:** Go 1.26, Gin, GORM, `github.com/modelcontextprotocol/go-sdk v1.6.0`, `github.com/golang-jwt/jwt/v5 v5.3.1`, Next.js 16, React 19, TypeScript, Node test runner, pnpm, Docker.

## Global Constraints

- Implement only in `C:\Users\Mion\Desktop\DEEIX-Chat-pr-worktrees\mcp-custom-headers` on `codex/mcp-custom-headers`; preserve the user's dirty primary checkout.
- Follow test-driven development: add one focused test, run it and record the expected RED, add only enough production code for GREEN, then refactor while tests remain green.
- Execute tasks sequentially. After every task commit, generate a review package for the recorded `BASE..HEAD`, send it to a fresh reviewer, and require separate specification-compliance and code-quality/security verdicts. Fix every Critical or Important finding and repeat both verdicts before starting the next task.
- Keep `.superpowers/sdd/progress.md` as the durable execution ledger. Task briefs, reports, review packages, and RED/GREEN evidence stay under `.superpowers/sdd/` and are not product commits.
- `headersEnabled=false` disables only custom template Headers, including the signed-context binding. It must never disable `Authorization`, HTTP transport Headers, MCP protocol Headers, or tracing Headers.
- Persist `headersJSON` while disabled. Validate a newly submitted template even when the switch is off, but allow runtime calls to bypass parsing historical bad template data while disabled.
- Continue using the backend blacklist. `Authorization`, protocol/transport Headers, tracing Headers, and the complete `X-DEEIX-*` namespace remain reserved.
- Never log raw Bearer tokens, signed JWTs, signing secrets, unmasked identity values, prompts, or PII. Preview, health, error, warning, trace, and non-tool responses must not return those values. The sole intentional diagnostic exception is the Bearer-authenticated `identity_check` result: its `identity` field returns the 10 allowlisted plain identity values and its `signedIdentity` field returns the validated claims summary; no other output field may echo identity or arbitrary Headers.
- Existing `domainmcp.Server` fixtures that expect custom Header behavior must explicitly set `HeadersEnabled: true`; do not reinterpret the domain zero value as enabled.
- Raw full-backend tests on this Windows host currently fail at sqlite-vec CGO compilation because `sqlite3.h`/native symbols are unavailable. Use focused package tests during tasks and use the final Linux Docker build as the full cross-platform gate; do not weaken project tests or dependencies to hide this environment issue.
- Do not edit generated Swagger files manually. Change DTO/annotations, run `backend/make swagger`, and commit all three generated contract files.
- Every product commit subject must match `type: subject` and the repository's allowed character set.

---

### Task 1: Persist the custom Header switch

**Files:**

- Modify: `backend/internal/infra/persistence/models/mcp.go`
- Modify: `backend/internal/domain/mcp/types.go`
- Modify: `backend/internal/repository/mcp.go`
- Modify: `backend/internal/infra/persistence/postgres/mcp/repository.go`
- Test: `backend/internal/infra/persistence/schema/schema_test.go`
- Test: `backend/internal/infra/persistence/postgres/mcp/repository_sqlite_test.go`

**Interfaces:** Consumes the current GORM `MCPServer`, domain `Server`, and repository create/update flows. Produces `HeadersEnabled bool` on persisted/domain reads, `HeadersEnabled bool` on create input, and `HeadersEnabled *bool` on update input; no application or HTTP contract changes occur in this task.

- [ ] Add failing migration and repository tests.

  Add `TestMigrateAddsMCPServerHeadersEnabledWithTrueDefault` using a legacy table without `headers_enabled`. Assert migration gives existing rows `true`, a second migration is idempotent, and historical `updated_at` is not changed.

  Add `TestMCPServerHeadersEnabledPersistence`. Assert create with `false`, get/list with `false`, update to `true`, and update input `nil` leaving the value unchanged.

- [ ] Run the focused test and confirm RED because the field does not exist.

  ```powershell
  cd backend
  go test ./internal/infra/persistence/schema ./internal/infra/persistence/postgres/mcp -run 'HeadersEnabled' -count=1
  ```

- [ ] Add the field through all persistence boundaries.

  ```go
  // persistence model
  HeadersEnabled bool `gorm:"not null;default:true;comment:是否启用自定义请求头"`

  // domain.Server
  HeadersEnabled bool

  type CreateMCPServerInput struct {
      HeadersEnabled bool
  }

  type UpdateMCPServerInput struct {
      HeadersEnabled *bool
  }
  ```

  `toDomainServer` must always map the stored value. Update only when the pointer is non-nil.

- [ ] Handle GORM's `default:true` zero-value behavior explicitly.

  In the existing create transaction, perform the normal `Create`, then explicitly persist the caller's boolean without changing `updated_at`:

  ```go
  if err := tx.Model(&item).
      UpdateColumn("headers_enabled", input.HeadersEnabled).Error; err != nil {
      return nil, err
  }
  item.HeadersEnabled = input.HeadersEnabled
  ```

- [ ] Run GREEN and the complete touched packages.

  ```powershell
  go test ./internal/infra/persistence/schema ./internal/infra/persistence/postgres/mcp -run 'HeadersEnabled' -count=1
  go test ./internal/infra/persistence/schema ./internal/infra/persistence/postgres/mcp -count=1
  ```

- [ ] Commit, self-review, and run both task review verdicts.

  ```powershell
  git add backend/internal/infra/persistence/models/mcp.go backend/internal/domain/mcp/types.go backend/internal/repository/mcp.go backend/internal/infra/persistence/postgres/mcp/repository.go backend/internal/infra/persistence/schema/schema_test.go backend/internal/infra/persistence/postgres/mcp/repository_sqlite_test.go
  git commit -m "feat: persist mcp custom header toggle"
  ```

---

### Task 2: Make signed context a template-owned transport binding

**Files:**

- Modify: `backend/internal/infra/mcp/header_template.go`
- Modify: `backend/internal/infra/mcp/header_template_test.go`
- Modify: `backend/internal/infra/mcp/client.go`
- Modify: `backend/internal/infra/mcp/client_test.go`
- Modify: `backend/internal/infra/mcp/protocol.go`
- Modify: `backend/internal/infra/mcp/transport.go`
- Modify: `backend/internal/infra/mcp/transport_test.go`
- Modify: `backend/internal/infra/mcp/operation.go`
- Modify: `backend/internal/infra/mcp/operation_test.go`
- Modify: `backend/internal/infra/mcp/session_manager.go`
- Modify: `backend/internal/infra/mcp/session_manager_test.go`

**Interfaces:** Consumes `HeadersEnabled`, parsed Header templates, existing `SignedContextConfig`, and physical transport requests. Produces `HeaderTemplateAnalysis.SignedContextHeader`, marker-free rendered static Headers, and immutable `CallConfig`/operation/session inputs that include switch state and signed Header name. Application code remains the only caller that decides whether a signing policy is configured.

- [ ] Add parser RED tests for the new token and binding invariants.

  Cover all 11 supported tokens, exact whole-value use, a maximum of one signed binding, an arbitrary non-blacklisted Header name, canonical duplicate rejection, continued `X-DEEIX-*` rejection, and no marker in rendered static Headers.

  ```go
  const SignedContextTemplateToken = "{{DEEIX_SIGNED_CONTEXT}}"
  const RecommendedSignedContextHeader = "X-MCP-CLIENT-SIGNED-CONTEXT"

  type HeaderTemplateAnalysis struct {
      Tokens              []string
      Warnings            []HeaderTemplateWarning
      SignedContextHeader string
  }
  ```

  The signed token is valid only when the entire normalized value equals `SignedContextTemplateToken`. Embedded or mixed text is an error, not a partially rendered Header.

- [ ] Confirm parser RED.

  ```powershell
  cd backend
  go test ./internal/infra/mcp -run 'HeaderTemplate.*Signed|SupportedHeaderTemplateTokens' -count=1
  ```

- [ ] Implement parser-owned binding discovery and rendered Header omission.

  Add the token to the catalog in the approved order. `ParseHeaderTemplateJSON` records the exact configured Header name in `Analysis.SignedContextHeader`. `RenderHeaderTemplate` skips that Header instead of emitting the marker. Keep the existing static 4096-byte value limit.

- [ ] Add transport/session RED tests before changing transport code.

  Test a custom binding name, absent signer, fresh JWT per physical POST/GET/DELETE, signed JWT value up to 8192 bytes, combined count/aggregate bounds, snapshot validation, and session reuse equality. Assert the old fixed `X-DEEIX-Context` Header is never emitted.

  Extend the immutable call chain:

  ```go
  type CallConfig struct {
      HeadersEnabled      bool
      SignedContextHeader string
      SignedContext       *SignedContextConfig
  }

  type TransportRequest struct {
      SignedContextHeader string
      SignedContext       *SignedContextConfig
  }

  type ConfigurationVersionInput struct {
      HeadersEnabled      bool
      SignedContextHeader string
      SignedContext       *SignedContextConfig
  }
  ```

- [ ] Confirm transport RED.

  ```powershell
  go test ./internal/infra/mcp -run 'SignedContextHeader|PerRequest|ConfigurationVersion|Session.*Signed' -count=1
  ```

- [ ] Implement named per-request signing without weakening Header bounds.

  Validate the Header name through the same blacklist/canonical logic as static Headers. Permit 8192 bytes only for the runtime signed value. Validate the combined maximum of 32 Headers and 16384 aggregate bytes immediately before dispatch. Sign once for every physical request only when both binding and signing config are present. A signer error, empty result, illegal Header character, or oversized result must fail before any bytes are dispatched; it must never silently fall back to unsigned delivery.

  The operation layer must copy the binding and signer config into every POST/GET/DELETE request. The configuration digest, equality comparison, snapshot, and session key material must include `HeadersEnabled` and the signed Header name, while excluding each request's changing JWT. A switch from enabled `{}` to disabled `{}` must therefore create a distinct configuration even though both have no rendered custom Headers.

- [ ] Run GREEN, race, and vet for the package.

  ```powershell
  go test ./internal/infra/mcp -count=1
  go test -race ./internal/infra/mcp -count=1
  go vet ./internal/infra/mcp
  ```

- [ ] Commit, self-review, and run both task review verdicts.

  ```powershell
  git add backend/internal/infra/mcp
  git commit -m "feat: bind mcp signed context through templates"
  ```

---

### Task 3: Apply switch semantics and backend-authoritative warnings

**Files:**

- Modify: `backend/internal/application/mcp/service.go`
- Modify: `backend/internal/application/mcp/service_test.go`
- Modify: `backend/internal/application/mcp/service_lifecycle_test.go` to set explicit `HeadersEnabled: true` on fixtures that exercise custom Headers
- Modify: `backend/internal/application/mcp/context_jwt.go`
- Modify: `backend/internal/application/mcp/context_jwt_test.go`
- Modify: `backend/internal/application/conversation/service_mcp_context_test.go` to set explicit `HeadersEnabled: true` on custom-Header fixtures
- Modify: `backend/internal/application/conversation/service_mcp_tools_test.go` to set explicit `HeadersEnabled: true` on custom-Header fixtures
- Modify: `backend/internal/application/conversation/service_mcp_lifecycle_test.go` to set explicit `HeadersEnabled: true` on custom-Header fixtures

**Interfaces:** Consumes Task 1 repository fields and Task 2 parser/runtime contracts. Produces pointer-aware create/update inputs, runtime `CallConfig`, `ServerView.HeaderWarnings`, `ServerView.SignedContextHeader`, preview warnings/binding state, and the context-JWT prepare token/recommended-name contract. HTTP mapping is deferred to Task 4.

- [ ] Add RED tests for create/update defaults and runtime bypass.

  ```go
  type CreateServerInput struct {
      HeadersEnabled *bool // nil means true
  }

  type UpdateServerInput struct {
      HeadersEnabled *bool // nil means unchanged
  }

  type PreviewHeaderTemplateInput struct {
      HeadersJSON    string
      HeadersEnabled bool
      ServerID       *uint
      Mode           inframcp.ContextMode
  }
  ```

  Add `TestCreateServerHeadersEnabledSemantics`, `TestUpdateServerHeadersEnabledPatchSemantics`, `TestServiceBuildCallConfigHeadersDisabledPreservesBearer`, `TestServiceBuildCallConfigHeadersDisabledSkipsInvalidPersistedTemplateAndJWT`, and `TestServiceBuildCallConfigHeadersEnabledRestoresTemplate`.

  Create and update must still parse and reject a newly submitted invalid `headersJSON` when `HeadersEnabled` is false. Add separate application RED cases for disabled create and disabled update with a blacklisted or malformed template; runtime bypass applies only to already-persisted historical data.

  Disabled calls must still validate the target URL, decrypt/use the Bearer token, and preserve MCP/HTTP transport behavior. They must not parse historical `headersJSON`, decrypt context-JWT configuration, render custom Headers, or create a signer.

  Enabled calls parse once and use `HeaderTemplateAnalysis.SignedContextHeader` as the sole binding source. A binding plus configured policy produces `SignedContext`; a binding without policy keeps the Header name in the immutable session configuration but omits the outbound Header; a configured policy without a binding neither decrypts the secret nor calls the signer. Always pass the actual `headersEnabled` state into `CallConfig` so session snapshots distinguish switch-only changes.

- [ ] Add the complete warning-matrix RED tests.

  ```text
  enabled + configured + unbound => signed_context_not_referenced
  enabled + unconfigured + bound => signed_context_not_configured
  disabled + configured + any binding => signed_context_headers_disabled
  enabled + configured + bound => none
  enabled + unconfigured + unbound => none
  disabled + unconfigured + any binding => none
  ```

  Add `TestServiceHeaderWarningsMatrix`, `TestServiceDescribeServerIncludesSyntaxAndSignedContextWarnings`, `TestServicePreviewHeaderTemplateUsesServerSignedContextState`, and `TestServicePreviewHeaderTemplateWithoutServerWarnsNotConfigured`.

  Add one create/update/call assertion proving these warning codes are advisory: they are returned to the administrator but do not block persistence, probe, sync, or chat configuration construction.

- [ ] Confirm RED.

  ```powershell
  cd backend
  go test ./internal/application/mcp -run 'HeadersEnabled|HeaderWarnings|PreviewHeaderTemplateUsesServer|PrepareReturnsTemplateContract' -count=1
  ```

- [ ] Implement shared warning derivation and authoritative response state.

  ```go
  type ServerView struct {
      Server              domainmcp.Server
      ContextJWT          ContextJWTStatus
      HeaderWarnings      []inframcp.HeaderTemplateWarning
      SignedContextHeader string
  }

  type PreviewHeaderTemplateResult struct {
      Mode                inframcp.ContextMode
      SupportedTokens     []string
      Warnings            []inframcp.HeaderTemplateWarning
      Headers             []HeaderPreviewItem
      SignedContextHeader string
  }

  func deriveHeaderWarnings(
      base []inframcp.HeaderTemplateWarning,
      headersEnabled bool,
      signedConfigured bool,
      signedContextHeader string,
  ) []inframcp.HeaderTemplateWarning
  ```

  Keep syntax warnings first and append at most the applicable signing-state warning. `DescribeServer` must best-effort parse persisted legacy data and must not make list/get fail. Preview with `ServerID` uses that server's real signing-policy state; preview without it treats signing as unconfigured. Preview never signs: when the template contains the signed binding it returns one masked `Sensitive: true` item plus `SignedContextHeader`. The marker may appear only in the documented supported-token/template contract, never as a previewed or outbound Header value; no response, warning, log, trace, or error detail may contain a real JWT.

- [ ] Replace the context rotation response's fixed Header contract.

  ```go
  type PrepareContextJWTRotationResult struct {
      ServerPublicID    string
      TemplateToken     string
      RecommendedHeader string
      Algorithm         string
      Secret            string
      Issuer            string
      Audience          string
      KeyID             string
      ExpiresSeconds    int
  }
  ```

  Return `{{DEEIX_SIGNED_CONTEXT}}` and `X-MCP-CLIENT-SIGNED-CONTEXT`; remove the old `Header` field. Add `TestContextJWTPrepareReturnsTemplateContract`.

- [ ] Run GREEN and adjacent application tests.

  ```powershell
  go test ./internal/application/mcp -count=1
  go test ./internal/application/mcp ./internal/application/conversation -count=1
  go test -race ./internal/application/mcp ./internal/application/conversation -count=1
  ```

- [ ] Commit, self-review, and run both task review verdicts.

  ```powershell
  git add backend/internal/application/mcp backend/internal/application/conversation
  git commit -m "feat: apply mcp header toggle and warnings"
  ```

---

### Task 4: Expose the HTTP, audit, and Swagger contract

**Files:**

- Modify: `backend/internal/transport/http/mcp/dto.go`
- Modify: `backend/internal/transport/http/mcp/handler.go`
- Modify: `backend/internal/transport/http/mcp/handler_test.go`
- Modify: `backend/internal/transport/http/mcp/handler_context_jwt_test.go`
- Generate: `backend/docs/docs.go`
- Generate: `backend/docs/swagger.json`
- Generate: `backend/docs/swagger.yaml`

**Interfaces:** Consumes Task 3 application inputs/views. Produces optional create/update `headersEnabled`, required preview `headersEnabled`, optional preview `serverID`, required server/preview `signedContextHeader`, non-null warning arrays, and prepare response `templateToken`/`recommendedHeader`; audit output contains field names only.

- [ ] Add RED handler tests for required response fields and pointer request semantics.

  ```go
  type ServerResponse struct {
      HeadersEnabled      bool                            `json:"headersEnabled"`
      HeaderWarnings      []HeaderTemplateWarningResponse `json:"headerWarnings"`
      SignedContextHeader string                          `json:"signedContextHeader"`
  }

  type CreateServerRequest struct {
      HeadersEnabled *bool `json:"headersEnabled"`
  }

  type UpdateServerRequest struct {
      HeadersEnabled *bool `json:"headersEnabled"`
  }

  type PreviewHeaderTemplateRequest struct {
      HeadersJSON    string `json:"headersJSON" binding:"required,max=32768"`
      HeadersEnabled *bool  `json:"headersEnabled" binding:"required"`
      ServerID       *uint  `json:"serverID" binding:"omitempty,min=1"`
      Mode           string `json:"mode" binding:"required,oneof=chat probe sync"`
  }

  type PrepareContextJWTRotationResponse struct {
      TemplateToken     string `json:"templateToken"`
      RecommendedHeader string `json:"recommendedHeader"`
      Algorithm         string `json:"algorithm"`
      Secret            string `json:"secret"`
      Issuer            string `json:"issuer"`
      Audience          string `json:"audience"`
      KeyID             string `json:"keyID"`
      ExpiresSeconds    int    `json:"expiresSeconds"`
  }
  ```

  A pointer boolean is required in preview so explicit `false` is valid and omission remains distinguishable. Assert `headerWarnings` is always a non-null array, `signedContextHeader` is always present, and preview returns both warnings and binding state.

  Add HTTP cases proving create and update with `headersEnabled: false` still reject a newly supplied invalid `headersJSON` through the standard invalid-body/domain envelope, while switching off without supplying a new template succeeds.

  Also lock request pointer semantics end-to-end: create omission reaches the repository as `true` and responds `true`; explicit create false remains false; update omission reaches the repository as nil; explicit update false and true remain distinct. Preview accepts explicit false but rejects a missing `headersEnabled` with the standard invalid-body envelope.

- [ ] Add RED audit and rotation-response tests.

  Audit `changedFields` includes `headersEnabled` only when the create/update request supplied the pointer. It records only the field name. Update the rotation response to `templateToken` and `recommendedHeader`, retain `Cache-Control: no-store` and `Pragma: no-cache`, and assert the old `header` property is absent.

- [ ] Confirm RED.

  ```powershell
  cd backend
  go test ./internal/transport/http/mcp -run 'HeadersEnabled|HeaderWarnings|SignedContextHeader|PreviewHeaderTemplate|PrepareContextJWT|MCPControlPlaneHandlersRecordSafeSuccessAudits' -count=1
  ```

- [ ] Implement DTO/handler mappings and Swagger annotations, then run GREEN.

  ```powershell
  go test ./internal/transport/http/mcp -count=1
  make swagger
  go test ./internal/transport/http/mcp -count=1
  $swagger = Get-Content docs/swagger.json -Raw | ConvertFrom-Json
  $rotation = $swagger.definitions.'internal_transport_http_mcp.PrepareContextJWTRotationResponse'
  $rotationNames = @($rotation.properties.PSObject.Properties.Name)
  if ('header' -in $rotationNames) { throw 'retired rotation header property remains' }
  if ('templateToken' -notin $rotationNames -or 'recommendedHeader' -notin $rotationNames) { throw 'rotation template contract missing' }
  $server = $swagger.definitions.'internal_transport_http_mcp.ServerResponse'
  foreach ($name in @('headersEnabled', 'headerWarnings', 'signedContextHeader')) { if ($name -notin @($server.required)) { throw "ServerResponse missing required $name" } }
  $preview = $swagger.definitions.'internal_transport_http_mcp.PreviewHeaderTemplateRequest'
  if ('headersEnabled' -notin @($preview.required)) { throw 'preview headersEnabled is not required' }
  git diff --check
  ```

- [ ] Commit, self-review, and run both task review verdicts.

  ```powershell
  git add backend/internal/transport/http/mcp backend/docs
  git commit -m "feat: expose mcp header toggle contract"
  ```

---

### Task 5: Add frontend form state, payload diffs, and submit gates

**Files:**

- Modify: `frontend/features/admin/api/mcp.types.ts`
- Modify: `frontend/features/admin/model/mcp-server-form.ts`
- Modify: `frontend/features/admin/model/mcp-server-form.test.mjs`
- Modify: `frontend/features/admin/components/sections/tools/mcp-server-dialog.tsx`

**Interfaces:** Consumes Task 4's `AdminMCPServerDTO` fields and create/update payload contract. Produces pure form initialization, create/update diffs, submit readiness, and connection-change detection. This task does not pass new props to `MCPHeaderTemplateEditor`; UI switch wiring is atomic in Task 6.

- [ ] Add form-model RED tests before touching the component.

  Write tests against these target API/form fields; do not add production declarations before observing RED:

  ```ts
  // Add to AdminMCPServerDTO.
  headersEnabled: boolean;
  headerWarnings: MCPHeaderTemplateWarningDTO[];
  signedContextHeader: string;

  // Add to ServerFormState.
  headersEnabled: boolean;
  ```

  Tests must lock: empty form defaults to `headersEnabled: true` and `{}`; persisted false restores; create explicitly sends true or false; update with only the toggle emits only `{headersEnabled: ...}`; omitted unchanged fields remain omitted; disabled Headers do not require a current preview; enabled Headers do; and a toggle change counts as an MCP connection change.

- [ ] Confirm RED.

  ```powershell
  cd frontend
  node --import tsx --test features/admin/model/mcp-server-form.test.mjs
  ```

- [ ] Add the API/form fields, then implement and export the pure form functions.

  ```ts
  export const EMPTY_SERVER_FORM: ServerFormState;
  export function serverFormFromDTO(server: AdminMCPServerDTO): ServerFormState;
  export function toServerCreatePayload(form: ServerFormState): AdminMCPServerCreatePayload;
  export function toServerUpdatePayload(
    form: ServerFormState,
    original: AdminMCPServerDTO,
  ): AdminMCPServerUpdatePayload;
  export type ServerFormSubmitContext = {
    accessTokenPresent: boolean;
    contextPending: boolean;
    previewIsCurrent: boolean;
    original: AdminMCPServerDTO | null;
  };
  export function canSubmitServerForm(
    form: ServerFormState,
    context: ServerFormSubmitContext,
  ): boolean;
  export function hasMCPConnectionChanges(
    original: AdminMCPServerDTO | null,
    payload: AdminMCPServerUpdatePayload | null,
  ): boolean;
  ```

  The Header gate is exactly:

  ```ts
  const headersReady = !form.headersEnabled || context.previewIsCurrent;
  ```

- [ ] Rewire `mcp-server-dialog.tsx` to use the pure initialization, payload, submit, and connection-change functions. Keep the editor's existing prop surface in this commit so it remains type-correct; Task 6 adds the switch props to the editor and dialog together.

- [ ] Run GREEN and the complete frontend unit suite.

  ```powershell
  node --import tsx --test features/admin/model/mcp-server-form.test.mjs
  pnpm test
  pnpm lint
  pnpm build
  ```

- [ ] Commit, self-review, and run both task review verdicts.

  ```powershell
  git add frontend/features/admin/api/mcp.types.ts frontend/features/admin/model/mcp-server-form.ts frontend/features/admin/model/mcp-server-form.test.mjs frontend/features/admin/components/sections/tools/mcp-server-dialog.tsx
  git commit -m "feat: add mcp header form controls"
  ```

---

### Task 6: Build the frontend switch, default template, Tooltip, and signing state

**Files:**

- Modify: `frontend/features/admin/api/mcp.types.ts`
- Modify: `frontend/features/admin/api/mcp.ts`
- Modify: `frontend/features/admin/model/mcp-header-template.ts`
- Modify: `frontend/features/admin/model/mcp-header-template.test.mjs`
- Modify: `frontend/features/admin/model/mcp-context-jwt.test.mjs`
- Modify: `frontend/features/admin/components/sections/tools/mcp-header-template-editor.tsx`
- Modify: `frontend/features/admin/components/sections/tools/mcp-context-jwt-panel.tsx`
- Modify: `frontend/features/admin/components/sections/tools/mcp-context-secret-dialog.tsx`
- Modify: `frontend/i18n/messages/en-US/admin-tools.json`
- Modify: `frontend/i18n/messages/zh-CN/admin-tools.json`

**Interfaces:** Consumes Task 5 form state and Task 4 preview/server/rotation responses. Produces the exact 11-entry default template, full preview request key, exhaustive five-code warning mapper, switch/editor prop contract, accessible Tooltip, and backend-authoritative signing panel. No frontend code parses `headersJSON` to infer a signed binding.

- [ ] Add RED model/API tests for the exact default template and full preview key.

  ```ts
  export const DEEIX_SIGNED_CONTEXT_TOKEN =
    "{{DEEIX_SIGNED_CONTEXT}}" as const;
  export const MCP_RECOMMENDED_SIGNED_CONTEXT_HEADER =
    "X-MCP-CLIENT-SIGNED-CONTEXT" as const;
  export const DEFAULT_MCP_HEADER_TEMPLATE: Readonly<Record<string, string>>;
  export const DEFAULT_MCP_HEADER_TEMPLATE_JSON: string;
  ```

  Lock this exact order:

  ```text
  X-MCP-CLIENT-USER-PUBLIC-ID             {{DEEIX_USER_PUBLIC_ID}}
  X-MCP-CLIENT-USER-DISPLAY-NAME          {{DEEIX_USER_DISPLAY_NAME}}
  X-MCP-CLIENT-USER-EMAIL                 {{DEEIX_USER_EMAIL}}
  X-MCP-CLIENT-USER-ROLE                  {{DEEIX_USER_ROLE}}
  X-MCP-CLIENT-CONVERSATION-PUBLIC-ID     {{DEEIX_CONVERSATION_PUBLIC_ID}}
  X-MCP-CLIENT-ASSISTANT-MESSAGE-PUBLIC-ID {{DEEIX_ASSISTANT_MESSAGE_PUBLIC_ID}}
  X-MCP-CLIENT-USER-MESSAGE-PUBLIC-ID     {{DEEIX_USER_MESSAGE_PUBLIC_ID}}
  X-MCP-CLIENT-REQUEST-ID                 {{DEEIX_REQUEST_ID}}
  X-MCP-CLIENT-RUN-ID                     {{DEEIX_RUN_ID}}
  X-MCP-CLIENT-TRACE-ID                   {{DEEIX_TRACE_ID}}
  X-MCP-CLIENT-SIGNED-CONTEXT             {{DEEIX_SIGNED_CONTEXT}}
  ```

  Assert the JSON passes the local lightweight validator and contains no `X-DEEIX-Context`, Bearer value, JWT, or secret. `buildMCPHeaderPreviewRequestKey` must change when JSON, mode, `headersEnabled`, or `serverID` changes.

- [ ] In the RED tests, target the following frontend contract, warning union, mapper, and control-state interface; do not add their production declarations yet.

  ```ts
  export type MCPHeaderTemplateWarningCode =
    | "unknown_token"
    | "malformed_token"
    | "signed_context_not_referenced"
    | "signed_context_not_configured"
    | "signed_context_headers_disabled";

  export type AdminMCPHeaderTemplatePreviewPayload = {
    headersJSON: string;
    mode: MCPHeaderTemplateMode;
    headersEnabled: boolean;
    serverID?: number;
  };

  export type AdminMCPHeaderTemplatePreviewDTO = {
    mode: MCPHeaderTemplateMode;
    supportedTokens: string[];
    warnings: MCPHeaderTemplateWarningDTO[];
    headers: MCPHeaderPreviewItemDTO[];
    signedContextHeader: string;
  };

  export type MCPContextJWTPrepareResult = {
    templateToken: "{{DEEIX_SIGNED_CONTEXT}}";
    recommendedHeader: "X-MCP-CLIENT-SIGNED-CONTEXT";
    algorithm: "HS256";
    secret: string;
    issuer: string;
    audience: string;
    keyID: string;
    expiresSeconds: number;
  };

  export async function previewAdminMCPHeaderTemplate(
    accessToken: string,
    payload: AdminMCPHeaderTemplatePreviewPayload,
    signal?: AbortSignal,
  ): Promise<AdminMCPHeaderTemplatePreviewDTO>;

  export function getMCPHeaderWarningMessageKey(
    code: MCPHeaderTemplateWarningCode,
  ):
    | "headerTemplate.warnings.unknownToken"
    | "headerTemplate.warnings.malformedToken"
    | "headerTemplate.warnings.signedContextNotReferenced"
    | "headerTemplate.warnings.signedContextNotConfigured"
    | "headerTemplate.warnings.signedContextHeadersDisabled";

  export type MCPHeaderTemplateControlState = {
    switchDisabled: boolean;
    editorDisabled: boolean;
    modeDisabled: boolean;
    previewDisabled: boolean;
    defaultTemplateDisabled: boolean;
  };

  export function getMCPHeaderTemplateControlState(
    outerDisabled: boolean,
    headersEnabled: boolean,
  ): MCPHeaderTemplateControlState;

  export function nextMCPHeaderTemplateAfterApplyDefault(
    current: string,
    defaultTemplateDisabled: boolean,
  ): string;
  ```

  Preview and server DTOs receive `signedContextHeader: string`. Rotation uses `templateToken` and `recommendedHeader`; delete every `prepared.header` reference. Map all five warning codes exhaustively to distinct i18n keys.

- [ ] Confirm RED.

  ```powershell
  cd frontend
  node --import tsx --test features/admin/model/mcp-header-template.test.mjs features/admin/model/mcp-context-jwt.test.mjs
  ```

- [ ] Implement the model/API declarations, constants, preview request, warning mapper, and control-state function, then repeat the focused command and confirm GREEN before editing components.

  ```powershell
  node --import tsx --test features/admin/model/mcp-header-template.test.mjs features/admin/model/mcp-context-jwt.test.mjs
  ```

- [ ] Implement the editor switch and backend-authoritative preview lifecycle.

  ```ts
  type Props = {
    headersEnabled: boolean;
    serverID?: number;
    onHeadersEnabledChange: (enabled: boolean) => void;
  };

  const controlState = getMCPHeaderTemplateControlState(
    disabled,
    headersEnabled,
  );
  ```

  Add all three props to `MCPHeaderTemplateEditor` and pass them from `mcp-server-dialog.tsx` in the same commit:

  ```tsx
  headersEnabled={form.headersEnabled}
  onHeadersEnabledChange={(headersEnabled) =>
    setForm((previous) => ({ ...previous, headersEnabled }))
  }
  serverID={original?.id}
  ```

  The switch is disabled only by outer pending state. Unit-test `getMCPHeaderTemplateControlState` for enabled, feature-disabled, and outer-disabled cases. Unit-test `nextMCPHeaderTemplateAfterApplyDefault` to return the current draft unchanged when disabled and the exact default JSON when enabled; the component handler must call this helper and invoke `onChange` only when the returned value differs. In the component, use the five named control-state booleans directly on the switch, JSON editor, mode selector, preview trigger/state, and default-template button; all controls remain visible while disabled. Assert `switchDisabled` remains false when only the feature is off. When off, cancel/stop preview and report invalid preview state while leaving the form submittable through Task 5's gate. When on, request preview with all four key inputs and accept a result as current only when its full request key matches.

  “Apply default template” only replaces the draft and invalidates preview. It must not save, enable Headers, or configure signing.

- [ ] Move the four existing explanation strings into an accessible `?` Tooltip.

  Use the existing `Tooltip`, `TooltipTrigger asChild`, `TooltipContent`, `Button type="button"`, and `CircleHelp`. Give the trigger an i18n `aria-label`; support pointer click as well as normal focus/keyboard behavior. Remove the amber explanation block. Keep the four existing backend-authority, plaintext, blacklist, and masking messages inside `TooltipContent`.

- [ ] Render signing state only from backend-returned fields.

  `MCPContextJWTPanel` displays the template token, recommended Header, `server.signedContextHeader` or “not bound”, and every `server.headerWarnings` entry through the exhaustive mapper. Do not parse `headersJSON` in the panel. The secret dialog shows `prepared.templateToken` and `prepared.recommendedHeader`.

  Add matching English and Chinese entries for `headerTemplate.helpLabel`, `headerTemplate.enabled`, `headerTemplate.enabledHelp`, `headerTemplate.disabledHelp`, `headerTemplate.applyDefault`, `headerTemplate.tokens.signedContext`, the three new signing warning keys, `contextJwt.state.templateToken`, `contextJwt.state.recommendedHeader`, `contextJwt.state.binding`, `contextJwt.state.notBound`, `contextJwt.state.forwardingWarnings`, `contextJwt.rotation.templateToken`, and `contextJwt.rotation.recommendedHeader`.

- [ ] Run GREEN, lint, and the static residual checks.

  ```powershell
  node --import tsx --test features/admin/model/mcp-header-template.test.mjs features/admin/model/mcp-context-jwt.test.mjs
  pnpm test
  pnpm lint
  pnpm build
  $legacy = rg -n 'X-DEEIX-Context|prepared\.header|bg-amber-50/70' features/admin
  if ($LASTEXITCODE -eq 0) { $legacy; throw 'legacy MCP Header UI contract remains' }
  if ($LASTEXITCODE -gt 1) { throw 'legacy MCP Header scan failed' }
  rg -n 'backendAuthority|plaintextWarning|blacklistWarning|maskedPreservation' features/admin/components/sections/tools/mcp-header-template-editor.tsx
  ```

  The first search must have no product-code match. The second must find all four keys inside the Tooltip content.

- [ ] Commit, self-review, and run both task review verdicts.

  ```powershell
  git add frontend/features/admin frontend/i18n/messages/en-US/admin-tools.json frontend/i18n/messages/zh-CN/admin-tools.json
  git commit -m "feat: add mcp header template controls"
  ```

---

### Task 7: Scaffold the Go demo configuration and plain identity parser

**Files:**

- Create: `demo/go-mcp-server/go.mod`
- Create: `demo/go-mcp-server/go.sum`
- Create: `demo/go-mcp-server/internal/config/config.go`
- Create: `demo/go-mcp-server/internal/config/config_test.go`
- Create: `demo/go-mcp-server/internal/identity/types.go`
- Create: `demo/go-mcp-server/internal/identity/headers.go`
- Create: `demo/go-mcp-server/internal/identity/headers_test.go`

**Interfaces:** Consumes only process environment values and `http.Header`; it must not import `backend/internal`. Produces a validated immutable `config.Config`, the exact 10-field `HeaderIdentity`, an ordered defensive `PlainHeaderNames` list, and an unambiguous canonical identity digest for Task 8 session isolation.

- [ ] Create the independent module and add exact dependencies.

  ```go
  module github.com/DEEIX-AI/DEEIX-Chat/demo/go-mcp-server

  go 1.26

  require (
      github.com/golang-jwt/jwt/v5 v5.3.1
      github.com/modelcontextprotocol/go-sdk v1.6.0
  )
  ```

- [ ] Add configuration RED tests.

  ```go
  type LookupEnv func(string) (string, bool)
  type ContextJWT struct { Secret, Issuer, Audience, KeyID string }
  type Config struct {
      Addr                string
      BearerToken         string
      SignedContextHeader string
      ContextJWT          *ContextJWT
  }
  func Load(lookup LookupEnv) (Config, error)
  ```

  Cover defaults (`127.0.0.1:8090`, `X-MCP-CLIENT-SIGNED-CONTEXT`), missing/31-byte/32-byte Bearer, loopback versus non-loopback bind addresses, all-empty/all-present/partial JWT configuration, canonical raw-base64url secret decoding to exactly 32 bytes, and illegal/reserved signed Header names. Accept only an explicit loopback IP or `localhost` plus a valid port; reject wildcard and non-loopback hosts. Reject a signed Header that is reserved by HTTP/MCP/DEEIX rules or canonically collides with one of the 10 plain identity Headers.

- [ ] Run the focused configuration command and confirm RED before adding `config.go` implementation.

  ```powershell
  cd demo/go-mcp-server
  go test ./internal/config -run TestLoad -count=1
  ```

- [ ] Implement minimal loading and repeat the same focused command for GREEN, then lock dependencies.

  ```powershell
  go test ./internal/config -run TestLoad -count=1
  go mod download
  go mod verify
  ```

- [ ] Add plain-identity RED tests for the exact allowlist parser contract.

  ```go
  type HeaderIdentity struct {
      UserPublicID             string `json:"userPublicID"`
      UserDisplayName          string `json:"userDisplayName"`
      UserEmail                string `json:"userEmail"`
      UserRole                 string `json:"userRole"`
      ConversationPublicID     string `json:"conversationPublicID"`
      AssistantMessagePublicID string `json:"assistantMessagePublicID"`
      UserMessagePublicID      string `json:"userMessagePublicID"`
      RequestID                string `json:"requestID"`
      RunID                    string `json:"runID"`
      TraceID                  string `json:"traceID"`
  }

  func PlainHeaderNames() []string
  func ParseHeaders(http.Header) (HeaderIdentity, error)
  func (identity HeaderIdentity) CanonicalDigest() [32]byte
  ```

  Enforce these UTF-8 byte limits for plain identity values: user/conversation/assistant-message/user-message public IDs 128; display name 256; email 320; role 64; request ID 128; run/trace ID 64. The digest writes the exact tag `DEEIX-MCP-DEMO:plain-identity:v1` plus every field in fixed struct order, encoding the tag and each field independently as an unsigned 32-bit big-endian byte length followed by its UTF-8 bytes, then returns SHA-256. Lock a golden vector for values `user_pub`, `Dee Ix`, `user@example.test`, `admin`, `conv_pub`, `assistant_pub`, `message_pub`, `req_123`, `run_123`, `trace_123`: `e0fe9a3264e03c84d1ce89c02df1b3cad4738ebe98635754f3db1e63433ac863`. Test all 10 names, duplicate values, invalid UTF-8/control characters, every byte boundary, concatenation-ambiguity resistance, and that unknown `X-MCP-CLIENT-*` Headers are ignored and never reflected.

- [ ] Confirm plain-identity RED before adding `headers.go` implementation.

  ```powershell
  go test ./internal/identity -run 'Test(ParseHeaders|HeaderIdentity)' -count=1
  ```

- [ ] Implement only the allowlist parser and canonical digest, then repeat the identical command and confirm GREEN.

  ```powershell
  go test ./internal/identity -run 'Test(ParseHeaders|HeaderIdentity)' -count=1
  ```

- [ ] Commit, self-review, and run both task review verdicts.

  ```powershell
  git add demo/go-mcp-server
  git commit -m "feat: add mcp demo identity configuration"
  ```

---

### Task 8: Verify signed identity and bridge authenticated snapshots

**Files:**

- Create: `demo/go-mcp-server/internal/identity/signed_context.go`
- Create: `demo/go-mcp-server/internal/identity/signed_context_test.go`
- Create: `demo/go-mcp-server/internal/identity/auth.go`
- Create: `demo/go-mcp-server/internal/identity/auth_test.go`

**Interfaces:** Consumes Task 7's validated config, plain identity parser/digest, the configured signed Header, and the official SDK `auth.TokenVerifier` contract. Produces a safe `Snapshot`, stable verification reason codes, a stable opaque session `UserID`, a fixed-key defensive `TokenInfo.Extra` snapshot, and a five-minute future SDK token expiration.

- [ ] Add table-driven signed-context RED tests.

  ```go
  type SignedIdentity struct {
      Subject, Mode, Name, Email, Role string
      ConversationPublicID, AssistantMessagePublicID, UserMessagePublicID string
      RequestID, RunID, TraceID string
  }

  type Verification struct {
      Present, Configured, Valid, Verified bool
      Reason string
  }

  type Snapshot struct {
      Identity       HeaderIdentity
      SignedIdentity *SignedIdentity
      Verification   Verification
      Mismatches     []string
  }

  type SignedConfig struct { Header, Secret, Issuer, Audience, KeyID string }
  const SnapshotExtraKey = "deeix.identity.snapshot"
  func NewResolver(cfg *SignedConfig, now func() time.Time) (*Resolver, error)
  func (r *Resolver) Resolve(http.Header) (Snapshot, error)
  ```

  Mirror the DEEIX JWT wire shape exactly:

  ```go
  type contextClaims struct {
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
  ```

  Require JOSE `typ` exactly `JWT`. Enforce these UTF-8 byte limits: sub/conversation/message IDs 128; name 256; email 320; role 64; request ID 128; run/trace ID 64; issuer 512; audience 128; key ID and jti 64; compact JWT 8192. The stable `Verification.Reason` set is exactly `signed_context_unconfigured`, `signed_context_missing`, `signed_context_duplicate`, `signed_context_too_large`, `signed_context_invalid`, `signed_context_mismatch`, and `signed_context_verified`.

  Require a single audience exactly equal to config, all `exp`/`nbf`/`iat` timestamps, `iat == nbf`, `iat <= now < exp`, and `exp-iat` from 60 through 900 seconds inclusive. Cover valid, missing, unconfigured, duplicate, 8193-byte JWT, HS384/none, `typ`, `kid`, issuer, audience cardinality/value, exp/nbf/iat/jti/sub, TTL 59/60/900/901, `chat|probe|sync`, `sync` subject `system:mcp-sync`, every byte boundary, and mismatch reporting. The JWT HMAC key must be `[]byte(cfg.Secret)` exactly, even though configuration validation decodes the canonical base64url string to check entropy. Missing or invalid signed context and identity mismatches must return a Snapshot with a stable false verification reason, not an authentication error; only the independent Bearer check may reject access in the identity layer.

  Compare only identity fields present in both plain and signed forms. Compare chat/probe `sub` to plain user public ID, and compare optional name/email/role plus conversation/message/request/run/trace claims in this fixed order; sync `sub=system:mcp-sync` is not compared to an end-user public ID. Return only stable field names, never differing values, in `Mismatches`.

- [ ] Confirm signed resolver RED before adding resolver production code.

  ```powershell
  cd demo/go-mcp-server
  go test ./internal/identity -run 'Test(NewResolver|Resolver)' -count=1
  ```

- [ ] Implement the minimal resolver and repeat the identical command to confirm GREEN.

  ```powershell
  go test ./internal/identity -run 'Test(NewResolver|Resolver)' -count=1
  ```

- [ ] Add authenticator RED tests for Bearer comparison and session identity.

  ```go
  func NewAuthenticator(bearer string, resolver *Resolver) (*Authenticator, error)
  func (a *Authenticator) Verify(
      context.Context,
      string,
      *http.Request,
  ) (*auth.TokenInfo, error)
  func SnapshotFromTokenInfo(*auth.TokenInfo) (Snapshot, bool)
  ```

  Hash candidate and expected Bearer values with SHA-256, then compare using `subtle.ConstantTimeCompare`. Parse identity only after a valid Bearer. Bearer failures use a stable safe error code that unwraps to `auth.ErrInvalidToken`.

  A wrong Bearer returns an error whose `Error()` is exactly `auth.invalid_bearer` and whose `Unwrap()` is `auth.ErrInvalidToken`. A malformed allowlisted plain identity Header is not a Bearer failure: return `identity.headers_invalid` wrapping `auth.ErrOAuth`, which the official middleware maps to HTTP 400. Missing/invalid signed context never returns either error; it remains diagnostic Snapshot state.

  Derive opaque `TokenInfo.UserID` by SHA-256 with the same uint32-length-prefixed byte encoding used by `CanonicalDigest`. Every variant begins with the independently length-prefixed exact tag `DEEIX-MCP-DEMO:session-principal:v1`, then the length-prefixed discriminator `signed`, `plain`, or `bearer`, then the length-prefixed 32-byte binary SHA-256 of the Bearer. The signed variant appends sub/mode/conversation/run strings; the plain variant appends the 32-byte binary `CanonicalDigest`; the bearer variant appends nothing. Exclude `jti`, `iat`, and `exp`, and hex-encode the final digest. For Bearer `0123456789abcdef0123456789abcdef` and the Task 7 identity vector, lock golden UserIDs: signed `9fe183eb54c68a06ee71834a7d91b0d9da694acfb1a7a4f0fe383e5cb301afc2`, plain `dff14d002bcf91e63823177c396eb636455e34909b12add5dfbb846e41f187e1`, bearer-only `a0d42c7ec34dd3b4235aa13ec373d05784afcc11341e1e0c5bf8019568dcf558`. Store a defensive Snapshot copy only under `TokenInfo.Extra[SnapshotExtraKey]`. Populate `TokenInfo.Expiration` as `resolver.now().UTC().Add(5*time.Minute)`; expiration is not part of the stable `UserID` digest.

- [ ] Confirm auth RED before adding authenticator production code.

  ```powershell
  go test ./internal/identity -run 'Test(Authenticator|SnapshotFromTokenInfo)' -count=1
  ```

- [ ] Implement the minimal authenticator and repeat the same focused command for GREEN, then run package race/vet.

  ```powershell
  go test ./internal/identity -run 'Test(Authenticator|SnapshotFromTokenInfo)' -count=1
  go test ./internal/identity -count=1
  go test -race ./internal/identity -count=1
  go vet ./internal/identity
  ```

- [ ] Commit, self-review, and run both task review verdicts.

  ```powershell
  git add demo/go-mcp-server/internal/identity
  git commit -m "feat: authenticate mcp demo identity"
  ```

---

### Task 9: Serve the MCP identity tool securely

**Files:**

- Create: `demo/go-mcp-server/internal/server/server.go`
- Create: `demo/go-mcp-server/internal/server/protocol.go`
- Create: `demo/go-mcp-server/internal/server/identity_check.go`
- Create: `demo/go-mcp-server/internal/server/server_test.go`

**Interfaces:** Consumes Task 8's `Authenticator`, Snapshot bridge, and safe reason codes. Produces a stateful `/mcp` Streamable HTTP handler, public `/healthz`, exact `2025-11-25` protocol middleware, one `identity_check` tool, safe HTTP guards, and a configured `Server` lifecycle shell for Task 10.

- [ ] Add RED tests for health, Bearer, Origin, protocol, limits, and tool output.

  ```go
  const ProtocolVersion = "2025-11-25"

  type IdentityCheckInput struct{}
  type IdentityCheckOutput struct {
      Authenticated bool                  `json:"authenticated"`
      CheckedAt     string                `json:"checkedAt"`
      Verification  identity.Verification `json:"verification"`
      Identity      identity.HeaderIdentity `json:"identity"`
      SignedIdentity *identity.SignedIdentity `json:"signedIdentity,omitempty"`
      Mismatches    []string              `json:"mismatches"`
  }

  type Server struct {
      httpServer      *http.Server
      handler         http.Handler
      shutdownTimeout time.Duration
      logger          *slog.Logger
  }

  func New(cfg config.Config, logger *slog.Logger, now func() time.Time) (*Server, error)
  func (s *Server) Handler() http.Handler
  func requireProtocolVersion(next mcp.MethodHandler) mcp.MethodHandler
  func identityCheck(now func() time.Time) mcp.ToolHandlerFor[IdentityCheckInput, IdentityCheckOutput]
  func safeSDKErrorBoundary(next http.Handler) http.Handler
  ```

  Assert public `GET /healthz` returns only `ok`; `/mcp` accepts only POST/GET/DELETE; invalid Bearer returns 401; hostile Origin is rejected before authentication; invalid or missing signed context reaches the tool with `verified=false`; unsupported protocol never reaches the tool; body over 1 MiB and Header over 32 KiB fail safely; and checked time is UTC RFC3339 with `mismatches: []` when empty.

  Freeze and test this error matrix. Every application/SDK error uses `text/plain; charset=utf-8` and exactly one stable code line; the only pre-handler exception is Go's fixed HTTP 431 parser response:

  | Failure | Status / JSON-RPC | Safe response code |
  |---|---:|---|
  | `/mcp` wrong method | 405 | `http.method_not_allowed` |
  | Cross-origin deny | 403 | `http.origin_denied` |
  | missing/malformed/wrong Bearer | 401 | `auth.invalid_bearer` |
  | malformed allowlisted plain identity Header after valid Bearer | 400 | `identity.headers_invalid` |
  | wrong `Mcp-Protocol-Version` HTTP guard | 400 | `mcp.unsupported_protocol` |
  | POST body over 1 MiB | 413 | `http.request_too_large` |
  | request Header rejected by `http.Server.MaxHeaderBytes` | 431 | Go's fixed standard status body only |
  | initialize protocol mismatch | JSON-RPC -32602 | `mcp.unsupported_protocol` |
  | any SDK HTTP 400 path | 400 | `mcp.bad_request` |
  | any SDK HTTP 403 path | 403 | `mcp.forbidden` |
  | any SDK HTTP 404 path | 404 | `mcp.session_not_found` |
  | any SDK HTTP 405 path | 405 | `http.method_not_allowed` |
  | any SDK HTTP 409 path | 409 | `mcp.stream_conflict` |
  | any SDK HTTP 415 path | 415 | `mcp.unsupported_media_type` |
  | any other SDK HTTP 4xx path | original 4xx | `mcp.request_rejected` |
  | any SDK HTTP 5xx path | original 5xx | `mcp.internal_error` |

  For HTTP 2xx `application/json` JSON-RPC error responses, preserve only `jsonrpc`, `id`, and the numeric `error.code`; remove `error.data` and replace `error.message` by this exact mapping:

  | JSON-RPC code | Safe `error.message` |
  |---:|---|
  | -32700 | `mcp.parse_error` |
  | -32600 | `mcp.invalid_request` |
  | -32601 | `mcp.method_not_found` |
  | -32602 | `mcp.invalid_params` |
  | -32603 | `mcp.internal_error` |
  | any other code | `mcp.request_failed` |

  The one allowlisted application message is `mcp.unsupported_protocol` with code -32602 from `requireProtocolVersion`; preserve that exact message so initialize version rejection remains specific. Never pass through any other SDK/application error message.

  A 2xx JSON-RPC success envelope whose top-level `result.isError` is `true` is also an error representation, not a successful result. Preserve only `jsonrpc`, `id`, and this exact rebuilt result:

  ```json
  {
    "content": [{"type": "text", "text": "mcp.tool_error"}],
    "isError": true
  }
  ```

  Remove `_meta`, `structuredContent`, and every original content item. This specifically covers typed `identity_check` argument/schema validation errors whose SDK-generated text can contain request-controlled details.

  `safeSDKErrorBoundary` must use a private Header map and buffer non-SSE responses up to 1 MiB until the wrapped handler returns. On status 400 or above it discards every SDK Header/body, writes only the mapped HTTP code above with `Content-Type: text/plain; charset=utf-8` and `X-Content-Type-Options: nosniff`, and discards later writes. An empty HTTP 202 notification and any empty 204 response pass through unchanged. On any other 2xx `application/json` body it parses a single JSON-RPC response: ordinary success results are copied byte-for-byte, top-level error envelopes are rebuilt by the code table, and `result.isError=true` is rebuilt as the stable tool error above. For both sanitized 2xx shapes, discard every original response Header and emit only `Content-Type: application/json` plus `X-Content-Type-Options: nosniff`; preserve the original HTTP status. Invalid JSON, an oversized buffered body, or an unexpected non-SSE flush becomes HTTP 500 `mcp.internal_error`. For a successful `text/event-stream` response, `Flush` commits Headers/status and transparently delegates all later bytes; implement both `http.Flusher` and `Unwrap` so GET SSE still works. Unit-test a fake reflected Header/body for every mapped HTTP status and every JSON-RPC code, unknown-method/tool markers, `result.isError` content/metadata/structured-content markers, byte-identical success JSON, notification 202, and flushed SSE passthrough.

  Then integration-test every externally reachable v1.6.0 error family: invalid localhost `Host`, internal CrossOrigin rejection, Content-Type, Accept, missing/unknown/mismatched session, DELETE/GET session shape, malformed and POST `Last-Event-ID`, empty/read-failing/malformed/batched JSON, invalid JSON-RPC request, `Mcp-Method`/`Mcp-Name` mismatch, stream conflict, server-unavailable/internal failures, unknown JSON-RPC method, unknown tool, and invalid `identity_check` params. Inject distinct Host/Origin/Last-Event-ID/protocol/JSON-RPC method/tool/params/Bearer/JWT/PII markers and assert none appears in response Headers or bodies. The outer `bearerShapeGuard` handles missing/malformed Authorization before the SDK middleware; authenticator errors implement `Error() == "auth.invalid_bearer"`, so `auth.RequireBearerToken` returns the same stable code for a wrong token. Preserve the SDK's actual validation, numeric HTTP status, JSON-RPC code/id, successful POST JSON, notification 202, and GET SSE behavior; sanitize only error representations.

- [ ] Confirm RED.

  ```powershell
  cd demo/go-mcp-server
  go test ./internal/server -run 'Test(Health|Bearer|Origin|Protocol|IdentityCheck|SafeSDKErrorBoundary|JSONRPC|ToolError)' -count=1
  ```

- [ ] Implement the official v1.6.0 SDK wiring.

  ```go
  mcpServer := mcp.NewServer(
      &mcp.Implementation{Name: "deeix-identity-demo", Version: "1.0.0"},
      nil,
  )
  mcpServer.AddReceivingMiddleware(requireProtocolVersion)
  mcp.AddTool(mcpServer, &mcp.Tool{
      Name: "identity_check",
      Description: "Return the authenticated and verified DEEIX identity snapshot.",
  }, identityCheck(now))

  origin := http.NewCrossOriginProtection()
  origin.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
      http.Error(w, "http.origin_denied", http.StatusForbidden)
  }))
  streamable := mcp.NewStreamableHTTPHandler(
      func(*http.Request) *mcp.Server { return mcpServer },
      &mcp.StreamableHTTPOptions{
          Stateless:                  false,
          JSONResponse:               true,
          SessionTimeout:             5 * time.Minute,
          DisableLocalhostProtection: false,
          CrossOriginProtection:      origin,
      },
  )

  sdkEndpoint := safeSDKErrorBoundary(
      scrubSensitiveHeaders(cfg.SignedContextHeader, streamable),
  )
  authenticated := auth.RequireBearerToken(
      auth.TokenVerifier(authenticator.Verify),
      nil,
  )(boundedBodyGuard(1<<20, sdkEndpoint))
  endpoint := origin.Handler(
      methodGuard(
          protocolHeaderGuard(
              bearerShapeGuard(authenticated),
          ),
      ),
  )
  ```

  `boundedBodyGuard` runs after Bearer authentication, reads at most 1 MiB plus one byte, returns `http.request_too_large` with 413 when oversized or `mcp.bad_request` with 400 on a read failure, and restores an equivalent request body for the SDK on success. The scrubber clones the request and its Header map, then removes Authorization, the signed Header, and all 10 plain identity Headers before the SDK sees `RequestExtra.Header`; it never mutates the original request. The tool retrieves only the verified Snapshot from `request.Extra.TokenInfo.Extra`.

  If `Mcp-Protocol-Version` is present it must equal `2025-11-25`; any request carrying `Mcp-Session-Id` must also carry that exact protocol Header. Keep the SDK's localhost DNS-rebinding protection enabled.

- [ ] Configure safe HTTP server defaults and run GREEN.

  Use `ReadHeaderTimeout: 5s`, `ReadTimeout: 10s`, `IdleTimeout: 60s`, `WriteTimeout: 0` for SSE, and `MaxHeaderBytes: 32 << 10`. No log line may include request Headers or identity.

  ```powershell
  go test ./internal/server -count=1
  go test -race ./internal/server -count=1
  go vet ./internal/server
  ```

- [ ] Commit, self-review, and run both task review verdicts.

  ```powershell
  git add demo/go-mcp-server/internal/server
  git commit -m "feat: serve mcp demo identity tool"
  ```

---

### Task 10: Prove demo lifecycle/security and document local use

**Files:**

- Create: `demo/go-mcp-server/internal/server/integration_test.go`
- Create: `demo/go-mcp-server/internal/server/security_test.go`
- Modify: `demo/go-mcp-server/internal/server/server.go`
- Create: `demo/go-mcp-server/cmd/server/main.go`
- Create: `demo/go-mcp-server/README.md`

**Interfaces:** Consumes Task 9's handler and server shell. Produces `New`, `Handler`, and cancellation-safe `Serve`, the signal-driven executable, official-client lifecycle/security evidence, and user documentation for all seven environment variables.

- [ ] Add full official-client lifecycle RED tests.

  Use `mcp.NewClient` and `mcp.StreamableClientTransport` with a test Header-injecting `http.Client` to perform initialize, tools/list, tools/call, and DELETE. Also use raw HTTP to prove:

  - identity A cannot reuse identity B's session ID;
  - every session-bound request requires the exact protocol Header;
  - invalid Origin and old protocol are rejected before tool execution;
  - cancellation shuts the server down without a goroutine leak;
  - invalid/missing signed context remains a diagnostic result rather than an authorization bypass.

- [ ] Add security RED tests.

  Seed unique leak markers for Host, Origin, Last-Event-ID, protocol, JSON-RPC method/name/params, Bearer, JWT, secret, unknown Header, and identity values. Exercise every outer and SDK status row frozen in Task 9, then scan response Headers/bodies and a captured `slog` buffer. In successful `identity_check`, allow identity markers only in the contractually intentional `identity` and validated `signedIdentity` fields; assert they never appear in verification reasons, mismatches, error results, metadata, or logs. Plain Headers are diagnostic only and never make `verification.verified` true. Unknown `X-MCP-CLIENT-*` values are never reflected.

- [ ] Confirm RED.

  ```powershell
  cd demo/go-mcp-server
  go test ./internal/server -run 'TestMCP|TestSession|TestServe|TestNoSensitive' -count=1
  ```

- [ ] Implement graceful serving and the executable.

  ```go
  type Server struct {
      httpServer      *http.Server
      handler         http.Handler
      shutdownTimeout time.Duration
      logger          *slog.Logger
  }

  func New(cfg config.Config, logger *slog.Logger, now func() time.Time) (*Server, error)
  func (s *Server) Handler() http.Handler
  func (s *Server) Serve(ctx context.Context, listener net.Listener) error
  ```

  `Serve` owns exactly one serving goroutine. On cancellation, call `Shutdown` with a new `context.WithTimeout(context.Background(), 5*time.Second)`, wait for that goroutine, and ignore only `http.ErrServerClosed`. `main` uses signal cancellation, binds the configured localhost address, and logs only safe lifecycle metadata.

- [ ] Write README usage with synthetic example values only.

  Document all seven environment variables (`MCP_DEMO_ADDR`, `MCP_DEMO_BEARER_TOKEN`, `MCP_DEMO_SIGNED_CONTEXT_HEADER`, `MCP_DEMO_CONTEXT_SECRET`, `MCP_DEMO_CONTEXT_ISSUER`, `MCP_DEMO_CONTEXT_AUDIENCE`, `MCP_DEMO_CONTEXT_KEY_ID`), `/healthz`, `/mcp`, the exact protocol, the 10 plain Header names, signed verification behavior, one `identity_check` tool, and a local run example. State clearly that the static Bearer demo is not a complete MCP OAuth 2.1 resource-server implementation.

- [ ] Run the demo release gates.

  ```powershell
  go mod tidy
  go mod verify
  go test ./...
  go test -race ./... -count=10
  go vet ./...
  go build ./cmd/server
  git diff --check
  ```

  Probe `govulncheck` deterministically and record either PASS or `SKIP govulncheck: command not installed`:

  ```powershell
  $govulncheck = Get-Command govulncheck -ErrorAction SilentlyContinue
  if ($null -eq $govulncheck) { Write-Output 'SKIP govulncheck: command not installed' } else { & $govulncheck.Source ./...; if ($LASTEXITCODE -ne 0) { throw 'govulncheck failed' } }
  ```

- [ ] Commit, self-review, and run both task review verdicts.

  ```powershell
  git add demo/go-mcp-server
  git commit -m "test: harden mcp demo server"
  ```

---

### Task 11: Cross-stack verification, final review, and Docker image

**Files:**

- Update: `.superpowers/sdd/progress.md` as non-product execution evidence.

No product edit is planned in this gate. If a final reviewer finds a defect, stop this task and add a corrective TDD task to the ledger with exact product/test paths, RED/GREEN commands, and its own commit/review range before editing.

**Interfaces:** Consumes the complete feature commit range. Produces final backend/frontend/demo verification evidence, two broad approval verdicts, and the local `deeix-chat:latest` Linux image; it does not merge, push, publish, or start a container.

- [ ] Run focused backend cross-stack tests.

  ```powershell
  Set-Location (git rev-parse --show-toplevel)
  Set-Location backend
  go test ./internal/infra/mcp ./internal/application/mcp ./internal/application/conversation ./internal/infra/persistence/schema ./internal/infra/persistence/postgres/mcp ./internal/transport/http/mcp -count=1
  go test -race ./internal/infra/mcp ./internal/application/mcp ./internal/application/conversation ./internal/infra/persistence/postgres/mcp ./internal/transport/http/mcp -count=1
  go vet ./...
  make build
  ```

  Record the known Windows sqlite-vec CGO result if `go vet ./...` or an implicit full `go test ./...` reaches the native package. Do not call the feature complete based only on partial tests.

- [ ] If the Windows native dependency prevents the full backend test/race/vet gate, run that gate in the Docker Linux builder stage instead.

  ```powershell
  Set-Location (git rev-parse --show-toplevel)
  docker build --target backend-builder -t deeix-chat-backend-test:local .
  docker run --rm deeix-chat-backend-test:local go test ./...
  docker run --rm deeix-chat-backend-test:local go test -race ./...
  docker run --rm deeix-chat-backend-test:local go vet ./...
  ```

  This is the authoritative full-backend test gate when Windows cannot compile sqlite-vec. It does not replace the final runtime-image build below.

- [ ] Run the frontend release gates.

  ```powershell
  Set-Location (git rev-parse --show-toplevel)
  Set-Location frontend
  pnpm test
  pnpm lint
  pnpm build
  ```

- [ ] Run the independent demo release gates again.

  ```powershell
  Set-Location (git rev-parse --show-toplevel)
  Set-Location demo/go-mcp-server
  go mod verify
  go test ./...
  go test -race ./...
  go vet ./...
  go build ./cmd/server
  ```

- [ ] Run contract and leak scans from the repository root.

  ```powershell
  Set-Location (git rev-parse --show-toplevel)
  $legacy = rg -n 'Header\.(Set|Add)\("X-DEEIX-Context"|contextJWTHeader\s*=|prepared\.header|value="X-DEEIX-Context"' backend/internal frontend/features demo --glob '!**/*_test.go'
  if ($LASTEXITCODE -eq 0) { $legacy; throw 'legacy fixed MCP Header injection remains' }
  if ($LASTEXITCODE -gt 1) { throw 'legacy runtime MCP Header scan failed' }
  rg -n 'DEEIX_SIGNED_CONTEXT|X-MCP-CLIENT-SIGNED-CONTEXT|headersEnabled|signedContextHeader' backend frontend demo docs
  git diff --check
  git status --short
  ```

  A blacklist entry or historical test/document may still name `X-DEEIX-Context`; the deterministic scan rejects only production injection and UI-contract patterns. The separate positive scan proves the replacement token/name/switch/binding contracts are present.

- [ ] Request a fresh broad specification/architecture review and a separate broad code-quality/security/concurrency review over the complete feature range. Fix all Critical and Important findings, rerun the affected task tests plus the appropriate full gates, and repeat reviews until both approve.

- [ ] Build the user-requested Linux image from the repository root.

  ```powershell
  Set-Location (git rev-parse --show-toplevel)
  docker build -t deeix-chat:latest .
  docker image inspect deeix-chat:latest --format '{{.Id}} {{.RepoTags}}'
  ```

  Treat a successful Docker build as the cross-platform backend/frontend production-build gate. Do not start or publish a container unless the user separately asks.

- [ ] Use `superpowers:verification-before-completion` and `superpowers:finishing-a-development-branch`. Report the exact successful commands, the known Windows-only limitation, resulting commit range, image tag/ID, and any remaining non-product `.superpowers/` artifacts. Do not merge, push, or open a PR without separate authorization.
