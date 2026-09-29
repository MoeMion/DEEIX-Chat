# Phase A+B MCP Protocol Hardening and DEEIX Header Templates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Safely ship DEEIX-native MCP custom Header templates after fixing the existing masked-secret update bug and hardening the Streamable HTTP lifecycle.

**Architecture:** A single pure template kernel owns JSON parsing, Header blacklist enforcement, DEEIX token analysis, and immutable rendering. The MCP application service uses it for administrator preview, probe, and system sync; the conversation service builds an authoritative chat context after both messages are persisted; the transport owns authentication and MCP protocol Headers and closes every per-operation session.

**Tech Stack:** Go 1.26, Gin, Gorm, PostgreSQL/SQLite, `net/http`, `golang.org/x/net/http/httpguts`, OpenTelemetry, Next.js 16, React 19, TypeScript, next-intl, pnpm.

## Global Constraints

- Run every command from the active isolated feature worktree root; `cd backend` and `cd frontend` are intentionally repository-relative.
- Complete this plan before starting phase C or D.
- Do not add an Open WebUI compatibility flag or aliases for Open WebUI tokens.
- Support exactly these case-sensitive tokens: `{{DEEIX_USER_PUBLIC_ID}}`, `{{DEEIX_USER_DISPLAY_NAME}}`, `{{DEEIX_USER_EMAIL}}`, `{{DEEIX_USER_ROLE}}`, `{{DEEIX_CONVERSATION_PUBLIC_ID}}`, `{{DEEIX_ASSISTANT_MESSAGE_PUBLIC_ID}}`, `{{DEEIX_USER_MESSAGE_PUBLIC_ID}}`, `{{DEEIX_REQUEST_ID}}`, `{{DEEIX_RUN_ID}}`, and `{{DEEIX_TRACE_ID}}`.
- Replace tokens in Header values only. A known token with no value becomes an empty string. An unknown token stays unchanged and produces a warning.
- A token candidate is a balanced, non-nested `{{...}}` sequence whose body contains 1 through 128 characters other than `{`, `}`, CR, or LF. Preserve every unknown candidate verbatim and emit one deduplicated `unknown_token` warning per Header/token pair. Preserve unmatched `{{` or `}}` verbatim and emit `malformed_token`; never evaluate expressions, nesting, conditions, or code.
- Do not add a compatibility or strict-evaluation mode; preserve-plus-warning is the only template behavior in this plan set.
- Parse templates strictly as `map[string]string`. Do not coerce JSON values.
- Apply a case-insensitive blacklist, not an allowlist. The complete exact-name blacklist is `Accept`, `Accept-Encoding`, `Authorization`, `Baggage`, `Connection`, `Content-Length`, `Content-Type`, `Cookie`, `Host`, `Keep-Alive`, `Last-Event-ID`, `MCP-Protocol-Version`, `MCP-Session-Id`, `Origin`, `Proxy-Authenticate`, `Proxy-Authorization`, `Proxy-Connection`, `Set-Cookie`, `TE`, `Traceparent`, `Tracestate`, `Trailer`, `Transfer-Encoding`, `Upgrade`, `User-Agent`, and `X-DEEIX-Context`.
- The complete case-insensitive prefix blacklist is `MCP-`, `Proxy-`, `Sec-`, and `X-DEEIX-`. These two in-file lists are the implementation source of truth; do not require an executor to open the roadmap or infer additional names.
- Validate Header syntax with `httpguts.ValidHeaderFieldName` and `httpguts.ValidHeaderFieldValue`. Reject CR, LF, and invalid control bytes from the original value before applying the existing valid-value `TrimSpace` normalization; normalization must never wash an invalid raw value into validity.
- Enforce 32 Headers, 128 bytes per name, 4096 bytes per value, 32768 raw JSON bytes, and 16384 rendered name-plus-value bytes.
- `chat` uses authoritative user/conversation/message values plus DEEIX-admitted request/run/trace correlation values. Existing caller-proposed IDs are normalized, bounded, and scoped before use and are never authorization credentials. `probe` uses the current administrator but leaves conversation/message/run empty. `sync` uses an empty system context and never impersonates the administrator.
- Phase A+B reuses `mcp_servers.headers_json` and performs no schema migration.
- MCP Base URLs require `http`/`https`, host, and path only; reject URL userinfo, query, and fragment at persistence and again before transport so credentials cannot enter URL traces or redirect targets.
- Keep `headers_json` plaintext for rollback compatibility and state clearly in the UI that it is not a credential store.
- Do not add signed JWT context, per-run session reuse, SSE reconnection, or `tools/list` pagination in this plan.
- Never log, trace, audit, store in `last_error`, or expose through a control-plane/error response a rendered Header value derived from real runtime context, bearer token, user email, or raw tool-server body. The fixed synthetic preview may return redacted/synthetic values, and a validated successful `tools/call.result` may flow only through the intended LLM tool-result path.
- Preserve the repository response envelope and regenerate committed Swagger artifacts for every MCP route.

---

## File and Interface Map

**New backend files**

- `backend/internal/infra/mcp/header_template.go`: pure template types, blacklist, parser, renderer, limits, token catalog.
- `backend/internal/infra/mcp/header_template_test.go`: unit and race-safe behavior tests.
- `backend/internal/application/conversation/service_mcp_context.go`: authoritative DEEIX chat context construction.
- `backend/internal/application/conversation/service_mcp_context_test.go`: public-ID and empty-value tests.
- `backend/internal/application/mcp/service_test.go`: control-plane mutation, probe, and sync tests.
- `backend/internal/transport/http/mcp/handler_test.go`: envelope, preview, probe, and secret response tests.

**New frontend files**

- `frontend/features/admin/components/sections/tools/mcp-server-dialog.tsx`: create/edit/probe orchestration.
- `frontend/features/admin/components/sections/tools/mcp-header-template-editor.tsx`: strict JSON validation, token help, warnings, preview.

**Frozen cross-phase interfaces**

```go
package mcp

type ContextMode string

const (
	ContextModeChat  ContextMode = "chat"
	ContextModeProbe ContextMode = "probe"
	ContextModeSync  ContextMode = "sync"
)

type TemplateContext struct {
	Mode                     ContextMode
	UserPublicID             string
	UserDisplayName          string
	UserEmail                string
	UserRole                 string
	ConversationPublicID     string
	AssistantMessagePublicID string
	UserMessagePublicID      string
	RequestID                string
	RunID                    string
	TraceID                  string
}

type CallConfig struct {
	BaseURL       string
	AuthToken     string
	TimeoutMS     int
	CustomHeaders map[string]string
	Context       TemplateContext
}

type CallInput struct {
	ToolName      string
	ArgumentsJSON string
}
```

Later plans may append signed-context or session collaborators, but must not rename or reinterpret these fields.

### Task 1: Fix Partial Server Updates and Masked Header Preservation

**Files:**
- Modify: `backend/internal/shared/security/security.go:15-62`
- Modify: `backend/internal/shared/security/security_test.go`
- Modify: `backend/internal/application/mcp/service.go:20-125`
- Create: `backend/internal/application/mcp/service_test.go`
- Modify: `backend/internal/infra/persistence/postgres/mcp/repository.go`
- Modify: `backend/internal/infra/persistence/postgres/mcp/repository_sqlite_test.go`
- Modify: `backend/internal/transport/http/mcp/dto.go:5-40`
- Modify: `backend/internal/transport/http/mcp/handler.go:50-92`
- Create: `backend/internal/transport/http/mcp/handler_test.go`

**Interfaces:**
- Consumes: existing `security.IsSensitiveHeaderName`, `repository.UpdateMCPServerInput`, and `secretbox` token encryption.
- Produces: strict `security.ParseHeaderStringMapJSON`, `security.RedactedHeaderValue`, `security.MergeRedactedHeadersJSON`, `appmcp.UpdateServerInput`, repository-not-found translation, and a true partial PATCH contract.

- [ ] **Step 1: Write failing masked-value merge tests**

Add these cases to `backend/internal/shared/security/security_test.go`:

```go
func TestMergeRedactedHeadersJSONPreservesExistingSensitiveValue(t *testing.T) {
	t.Parallel()
	got, err := MergeRedactedHeadersJSON(
		`{"X-API-Key":"real-secret","X-Tenant":"old"}`,
		`{"X-API-Key":"********","X-Tenant":"new"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"X-API-Key":"real-secret","X-Tenant":"new"}` {
		t.Fatalf("got %s", got)
	}
}

func TestMergeRedactedHeadersJSONRejectsUnknownSentinel(t *testing.T) {
	t.Parallel()
	_, err := MergeRedactedHeadersJSON(
		`{"X-Tenant":"old"}`,
		`{"X-API-Key":"********","X-Tenant":"new"}`,
	)
	if !errors.Is(err, ErrInvalidRedactedHeaders) {
		t.Fatalf("got %v", err)
	}
}

func TestMergeRedactedHeadersJSONDeletesOmittedSensitiveKey(t *testing.T) {
	t.Parallel()
	got, err := MergeRedactedHeadersJSON(
		`{"X-API-Key":"real-secret","X-Tenant":"old"}`,
		`{"X-Tenant":"new"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"X-Tenant":"new"}` {
		t.Fatalf("got %s", got)
	}
}
```

Add strict-map cases which reject `{"X-A":"1","X-A":"2"}`, `{"X-A":null}`, and a trailing second JSON value. Add repository/application/handler cases proving missing Get/Update/Delete becomes `repository.ErrNotFound` → `appmcp.ErrMCPServerNotFound` → HTTP 404 rather than a Gorm error or false delete success.

Add create/update application cases for `https://user:pass@example.com/mcp`, `https://example.com/mcp?token=x`, and `https://example.com/mcp#fragment`; each returns `ErrInvalidServerBaseURL` and writes nothing.

- [ ] **Step 2: Run the focused tests and confirm the red state**

```powershell
cd backend
go test ./internal/shared/security ./internal/infra/persistence/postgres/mcp ./internal/application/mcp ./internal/transport/http/mcp -count=1
```

Expected: FAIL because the strict merge API, partial inputs, repository translation, and handler sentinel mapping do not exist.

- [ ] **Step 3: Implement the sentinel merge without persisting the sentinel**

Add a streaming `ParseHeaderStringMapJSON(raw string) (map[string]string, error)` to `security.go`. Blank input returns an empty map. Otherwise it requires one JSON object, iterates key tokens so exact duplicate keys cannot be overwritten, requires every value to be a JSON string, and requires decoder EOF. `null` and all other non-string values fail. Both the sentinel helpers below and Task 3's template parser must reuse this function.

Add this contract to `security.go`:

```go
const RedactedHeaderValue = "********"

var ErrInvalidRedactedHeaders = errors.New("invalid redacted headers")

func MergeRedactedHeadersJSON(existingRaw string, incomingRaw string) (string, error) {
	existing, err := ParseHeaderStringMapJSON(existingRaw)
	if err != nil {
		return "", fmt.Errorf("%w: existing headers", ErrInvalidRedactedHeaders)
	}
	incoming, err := ParseHeaderStringMapJSON(incomingRaw)
	if err != nil {
		return "", fmt.Errorf("%w: incoming headers", ErrInvalidRedactedHeaders)
	}
	existingByName := make(map[string]string, len(existing))
	for key, value := range existing {
		existingByName[strings.ToLower(http.CanonicalHeaderKey(strings.TrimSpace(key)))] = value
	}
	for key, value := range incoming {
		if value != RedactedHeaderValue {
			continue
		}
		if !IsSensitiveHeaderName(key) {
			return "", fmt.Errorf("%w: sentinel on non-sensitive header", ErrInvalidRedactedHeaders)
		}
		previous, ok := existingByName[strings.ToLower(http.CanonicalHeaderKey(strings.TrimSpace(key)))]
		if !ok {
			return "", fmt.Errorf("%w: sentinel has no previous value", ErrInvalidRedactedHeaders)
		}
		incoming[key] = previous
	}
	normalized, err := json.Marshal(incoming)
	if err != nil {
		return "", fmt.Errorf("%w: encode headers", ErrInvalidRedactedHeaders)
	}
	return string(normalized), nil
}

func ContainsRedactedHeaderValue(raw string) bool {
	values, err := ParseHeaderStringMapJSON(raw)
	if err != nil {
		return false
	}
	for _, value := range values {
		if value == RedactedHeaderValue {
			return true
		}
	}
	return false
}
```

Update `RedactHeadersJSON` to use `RedactedHeaderValue`.

- [ ] **Step 4: Split create and update application inputs**

Use:

```go
type CreateServerInput struct {
	Name        string
	BaseURL     string
	AuthToken   string
	HeadersJSON string
	Status      string
}

type UpdateServerInput struct {
	Name           *string
	BaseURL        *string
	AuthToken      *string
	ClearAuthToken bool
	HeadersJSON    *string
	Status         *string
}
```

Create rejects `security.ContainsRedactedHeaderValue`. Update first loads the current Server, rejects simultaneous `AuthToken` and `ClearAuthToken`, merges a non-nil `HeadersJSON` with `MergeRedactedHeadersJSON`, validates only non-nil scalars, encrypts only an explicit bearer change, and passes pointer fields to `repository.UpdateMCPServerInput`.

Freeze bearer PATCH semantics:

- `AuthToken == nil` and `ClearAuthToken == false`: preserve ciphertext;
- non-nil `AuthToken` must trim to a non-empty value and replaces ciphertext;
- `ClearAuthToken == true` requires `AuthToken == nil` and writes an explicit empty ciphertext;
- non-nil empty/whitespace `AuthToken`, or clear plus token together, returns `ErrInvalidAuthTokenUpdate` and HTTP 400.

In the MCP repository, translate `gorm.ErrRecordNotFound` to `repository.ErrNotFound`, and return `repository.ErrNotFound` when update/delete affects zero rows. Application maps that sentinel to `ErrMCPServerNotFound`; transport maps it to 404 without importing Gorm above infra.

Extend the persisted BaseURL validator used by create and non-nil update: after URL parsing, require `parsed.User == nil`, `parsed.RawQuery == ""`, and `parsed.Fragment == ""` in addition to the existing scheme/host/SSRF checks. Transport Task 4 repeats the same rule for legacy rows/direct configs.

- [ ] **Step 5: Split HTTP request DTOs and expose configured state**

```go
type CreateServerRequest struct {
	Name        string `json:"name" binding:"required,max=128"`
	BaseURL     string `json:"baseURL" binding:"required,max=512"`
	AuthToken   string `json:"authToken" binding:"max=8192"`
	HeadersJSON string `json:"headersJSON" binding:"max=32768"`
	Status      string `json:"status" binding:"omitempty,oneof=active inactive"`
}

type UpdateServerRequest struct {
	Name           *string `json:"name" binding:"omitempty,max=128"`
	BaseURL        *string `json:"baseURL" binding:"omitempty,max=512"`
	AuthToken      *string `json:"authToken" binding:"omitempty,max=8192"`
	ClearAuthToken bool    `json:"clearAuthToken"`
	HeadersJSON    *string `json:"headersJSON" binding:"omitempty,max=32768"`
	Status         *string `json:"status" binding:"omitempty,oneof=active inactive"`
}
```

Add `AuthTokenConfigured bool `json:"authTokenConfigured"`` to `ServerResponse`. Map `UpdateServerRequest` pointers without synthesizing absent fields.

- [ ] **Step 6: Add an application integration regression test**

Use an in-memory Gorm SQLite database. Insert a Server containing a real `X-API-Key`, update only Status, then assert raw `headers_json` is unchanged. Repeat with redacted JSON plus one changed non-sensitive Header.

- [ ] **Step 7: Run focused backend tests**

```powershell
cd backend
go test ./internal/shared/security ./internal/application/mcp ./internal/transport/http/mcp -count=1
```

Expected: all selected packages pass.

- [ ] **Step 8: Commit the P0 backend contract**

```powershell
git add backend/internal/shared/security backend/internal/application/mcp backend/internal/infra/persistence/postgres/mcp backend/internal/transport/http/mcp
git commit -m "fix: preserve masked mcp header values"
```

### Task 2: Make the Frontend Use True Partial Updates

**Files:**
- Modify: `frontend/features/admin/api/mcp.types.ts`
- Modify: `frontend/features/admin/api/mcp.ts`
- Create: `frontend/features/admin/model/mcp-server-form.ts`
- Create: `frontend/features/admin/model/mcp-server-form.test.mjs`
- Modify: `frontend/features/admin/components/sections/tools/admin-tools.tsx:75-133`
- Modify: `frontend/features/admin/components/sections/tools/admin-tools.tsx:476-550`
- Modify: `frontend/i18n/messages/en-US/admin-tools.json`
- Modify: `frontend/i18n/messages/zh-CN/admin-tools.json`

**Interfaces:**
- Consumes: Task 1 partial PATCH and `authTokenConfigured`.
- Produces: typed create/update payloads, dirty-field omission, status-only PATCH, and password input semantics.

- [ ] **Step 1: Split TypeScript contracts**

```ts
export type AdminMCPServerCreatePayload = {
  name: string;
  baseURL: string;
  authToken?: string;
  headersJSON: string;
  status: "active" | "inactive";
};

export type AdminMCPServerUpdatePayload = {
  name?: string;
  baseURL?: string;
  authToken?: string;
  clearAuthToken?: boolean;
  headersJSON?: string;
  status?: "active" | "inactive";
};
```

Add `authTokenConfigured: boolean` to the DTO and change `updateAdminMCPServer` to accept the update type.

- [ ] **Step 2: Run the build to prove full-update callers remain**

```powershell
cd frontend
pnpm build
```

Expected: TypeScript fails at imports or call sites still using `AdminMCPServerPayload`.

- [ ] **Step 3: Write failing payload-model tests**

Create `frontend/features/admin/model/mcp-server-form.test.mjs` with the complete Node test module below. It covers unchanged form → empty payload, every dirty scalar, Header JSON change, configured-token preservation, replacement, removal, replacement/removal exclusion, and whitespace-only replacement:

```js
import assert from "node:assert/strict";
import test from "node:test";

import { toServerUpdatePayload } from "./mcp-server-form.ts";

const original = {
  id: 7,
  name: "Memory",
  baseURL: "https://mcp.example.test/mcp",
  authTokenConfigured: true,
  headersJSON: '{"X-Tenant":"tenant-a"}',
  status: "active",
  sortOrder: 0,
  toolCount: 1,
  activeToolCount: 1,
  lastSyncedAt: null,
  lastError: "",
  createdAt: "2026-07-10T00:00:00Z",
  updatedAt: "2026-07-10T00:00:00Z",
};

function form(overrides = {}) {
  return {
    name: original.name,
    baseURL: original.baseURL,
    authToken: "",
    clearAuthToken: false,
    headersJSON: original.headersJSON,
    status: original.status,
    ...overrides,
  };
}

test("MCP server form omits every unchanged field", () => {
  assert.deepEqual(toServerUpdatePayload(form(), original), {});
});

test("MCP server form emits one dirty scalar at a time", () => {
  assert.deepEqual(toServerUpdatePayload(form({ name: "  Memory 2  " }), original), {
    name: "Memory 2",
  });
  assert.deepEqual(
    toServerUpdatePayload(form({ baseURL: " https://new.example.test/mcp " }), original),
    { baseURL: "https://new.example.test/mcp" },
  );
  assert.deepEqual(toServerUpdatePayload(form({ status: "inactive" }), original), {
    status: "inactive",
  });
  assert.deepEqual(
    toServerUpdatePayload(form({ headersJSON: '{"X-Tenant":"tenant-b"}' }), original),
    { headersJSON: '{"X-Tenant":"tenant-b"}' },
  );
});

test("MCP server form preserves replaces and removes configured bearer explicitly", () => {
  assert.deepEqual(toServerUpdatePayload(form(), original), {});
  assert.deepEqual(toServerUpdatePayload(form({ authToken: " replacement " }), original), {
    authToken: "replacement",
  });
  assert.deepEqual(toServerUpdatePayload(form({ clearAuthToken: true }), original), {
    clearAuthToken: true,
  });
  assert.deepEqual(
    toServerUpdatePayload(form({ authToken: " replacement ", clearAuthToken: true }), original),
    { authToken: "replacement" },
  );
  assert.deepEqual(toServerUpdatePayload(form({ authToken: "   " }), original), {});
});

test("MCP server form does not request removal when no bearer is configured", () => {
  assert.deepEqual(
    toServerUpdatePayload(form({ clearAuthToken: true }), {
      ...original,
      authTokenConfigured: false,
    }),
    {},
  );
});
```

Run:

```powershell
cd frontend
pnpm test --test-name-pattern="MCP server form"
```

Expected: FAIL because `mcp-server-form.ts` and its exports do not exist.

- [ ] **Step 4: Emit only dirty edit fields**

Create `frontend/features/admin/model/mcp-server-form.ts` with the complete module below, then import `ServerFormState` and `toServerUpdatePayload` from it in `admin-tools.tsx`:

```ts
import type {
  AdminMCPServerDTO,
  AdminMCPServerUpdatePayload,
} from "@/features/admin/api/mcp.types";

export type ServerFormState = {
  name: string;
  baseURL: string;
  authToken: string;
  clearAuthToken: boolean;
  headersJSON: string;
  status: "active" | "inactive";
};

export function toServerUpdatePayload(
  form: ServerFormState,
  original: AdminMCPServerDTO,
): AdminMCPServerUpdatePayload {
  const payload: AdminMCPServerUpdatePayload = {};
  if (form.name.trim() !== original.name) payload.name = form.name.trim();
  if (form.baseURL.trim() !== original.baseURL) payload.baseURL = form.baseURL.trim();
  if (form.status !== original.status) payload.status = form.status;
  if (form.headersJSON !== (original.headersJSON || "{}")) {
    payload.headersJSON = form.headersJSON.trim() || "{}";
  }
  const replacement = form.authToken.trim();
  if (replacement) {
    payload.authToken = replacement;
  } else if (form.clearAuthToken && original.authTokenConfigured) {
    payload.clearAuthToken = true;
  }
  return payload;
}
```

Use the create type only for POST. Disable Save when an edit payload has no keys. The component clears `clearAuthToken` on replacement input and clears `authToken` when removal is selected; the pure helper also emits at most one field, with a non-empty replacement taking precedence as defense in depth.

Run: `cd frontend && pnpm test --test-name-pattern="MCP server form"`

Expected: PASS; the four model tests complete without importing React or browser globals.

- [ ] **Step 5: Make status update one field**

```ts
await updateAdminMCPServer(token, server.id, { status: nextStatus });
```

- [ ] **Step 6: Protect bearer entry**

Set `type="password"` and `autoComplete="new-password"`. Use `authTokenConfigured` to show configured/optional help and an explicit “Remove configured token” control. Selecting removal clears/disables the replacement field; entering a replacement clears the removal flag. Add both locale strings. Do not claim custom Header values are encrypted.

Exercise the UI payload helper manually in the component flow for preserve, replace, and remove; backend Task 1 tests remain the authoritative assertion that empty-string replacement and replace-plus-clear are rejected.

- [ ] **Step 7: Run frontend verification**

```powershell
cd frontend
pnpm test
pnpm lint
pnpm build
```

Expected: all three commands exit 0.

- [ ] **Step 8: Commit**

```powershell
git add frontend/features/admin/api frontend/features/admin/model frontend/features/admin/components/sections/tools/admin-tools.tsx frontend/i18n/messages
git commit -m "fix: use partial mcp server updates"
```

### Task 3: Add the Pure DEEIX Header Template Kernel

**Files:**
- Create: `backend/internal/infra/mcp/header_template.go`
- Create: `backend/internal/infra/mcp/header_template_test.go`
- Modify: `backend/go.mod`
- Modify: `backend/go.sum`

**Interfaces:**
- Consumes: `httpguts` and the frozen token/blacklist/limit contract.
- Produces: `ContextMode`, `TemplateContext`, `HeaderTemplate`, `ParsedHeaderTemplate`, `HeaderTemplateAnalysis`, `ParseHeaderTemplateJSON`, `RenderHeaderTemplate`, and `ValidateRenderedCustomHeaders`.

- [ ] **Step 1: Write table-driven parser and renderer tests**

Cover replacement, repeated tokens, known missing values, lowercase/dotted/vendor unknown-token warnings, unmatched delimiter warnings, Unicode, input-map immutability, preservation of the existing value `TrimSpace` behavior, different-case blacklisted names, `MCP-Anything`, exact duplicate JSON keys, case-variant canonical duplicates, leading/trailing whitespace in a Header name, CR/LF, JSON `null`, every other non-string JSON type, 33 Headers, oversized values, and raw JSON over 32768 bytes.

```go
func TestRenderHeaderTemplateUsesDEEIXValues(t *testing.T) {
	t.Parallel()
	template := HeaderTemplate{
		"X-User": "{{DEEIX_USER_PUBLIC_ID}}",
		"X-Chat": "{{DEEIX_CONVERSATION_PUBLIC_ID}}/{{DEEIX_RUN_ID}}",
	}
	ctx := TemplateContext{
		Mode:                 ContextModeChat,
		UserPublicID:         "user_public",
		ConversationPublicID: "conversation_public",
		RunID:                "run_public",
	}
	got, warnings, err := RenderHeaderTemplate(template, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || got["X-User"] != "user_public" ||
		got["X-Chat"] != "conversation_public/run_public" {
		t.Fatalf("got=%v warnings=%v", got, warnings)
	}
}

func TestParseHeaderTemplateValidatesRawValueBeforeWhitespaceNormalization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
	}{
		{name: "carriage return at edge", raw: `{"X-Test":"\rtenant"}`},
		{name: "line feed at edge", raw: `{"X-Test":"tenant\n"}`},
		{name: "nul", raw: `{"X-Test":"tenant\u0000value"}`},
		{name: "delete", raw: `{"X-Test":"tenant\u007fvalue"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseHeaderTemplateJSON(tt.raw); err == nil {
				t.Fatal("expected invalid raw Header value")
			}
		})
	}
}

func TestParseHeaderTemplatePreservesLegacyValidWhitespaceNormalization(t *testing.T) {
	t.Parallel()
	parsed, err := ParseHeaderTemplateJSON(`{"X-Test":"  tenant-a\t "}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Template["X-Test"]; got != "tenant-a" {
		t.Fatalf("value = %q", got)
	}
}
```

- [ ] **Step 2: Run tests and confirm missing API failure**

```powershell
cd backend
go test ./internal/infra/mcp -run "Test(Parse|Render)HeaderTemplate" -count=1
```

Expected: compilation fails because the template API does not exist.

- [ ] **Step 3: Define types and token catalog**

```go
type HeaderTemplate map[string]string

type HeaderTemplateWarning struct {
	Code       string
	HeaderName string
	Token      string
}

type HeaderTemplateAnalysis struct {
	Tokens   []string
	Warnings []HeaderTemplateWarning
}

type ParsedHeaderTemplate struct {
	Template HeaderTemplate
	Analysis HeaderTemplateAnalysis
}

type TokenDefinition struct {
	Token string
	Value func(TemplateContext) string
}

const maxHeaderValueBytes = 4096

var (
	ErrInvalidHeaderTemplateValue = errors.New("invalid mcp Header template value")
	ErrHeaderTemplateValueTooLarge = errors.New("mcp Header template value exceeds limit")
)
```

Use one ordered package-level catalog containing exactly the ten tokens. `SupportedHeaderTemplateTokens` returns a copy.

- [ ] **Step 4: Implement blacklist, syntax, and size validation**

Reject a Header name when it differs from `strings.TrimSpace(name)`; do not normalize an invalid original name into validity. Validate the unchanged name with `httpguts`, then compare its case-folded canonical form to the exact/prefix sets. Reject canonical duplicates.

For every decoded template value, preserve this exact security order:

```go
func normalizeTemplateHeaderValue(raw string) (string, error) {
	if len(raw) > maxHeaderValueBytes {
		return "", ErrHeaderTemplateValueTooLarge
	}
	// Validate the decoded raw value first. Do not let TrimSpace erase CR, LF,
	// NUL, DEL, or another invalid control byte and turn it into valid input.
	if !httpguts.ValidHeaderFieldValue(raw) {
		return "", ErrInvalidHeaderTemplateValue
	}
	// Preserve the existing behavior for values which were already legal:
	// leading/trailing spaces and horizontal tabs are normalized away.
	return strings.TrimSpace(raw), nil
}
```

Apply every frozen limit before persistence and the rendered aggregate limit after replacement. Export `ValidateRenderedCustomHeaders(headers map[string]string) error` as the only transport-time revalidator; it repeats name, blacklist, count, per-value, raw-value control-character, canonical-duplicate, and 16384-byte aggregate checks. It must likewise call `httpguts.ValidHeaderFieldValue(value)` before any optional normalization and must never mutate its input map.

- [ ] **Step 5: Implement strict parsing and immutable rendering**

`ParseHeaderTemplateJSON` defaults blank to `{}` and reuses Task 1 `security.ParseHeaderStringMapJSON`, which enforces the logical `map[string]string` contract with a streaming `json.Decoder`, not direct `json.Unmarshal` into a map:

1. require the first token to be `{`;
2. iterate every key token so an exact duplicate can be detected before Go overwrites it;
3. decode each value into `json.RawMessage`, require its first non-space byte to be `"`, then unmarshal it into a string;
4. reject `null`, numbers, booleans, arrays, and objects rather than coercing them;
5. reject exact duplicates and case-insensitive canonical-name duplicates;
6. require the matching `}` and then decoder EOF, rejecting trailing JSON.

After decoding, pass each raw string through `normalizeTemplateHeaderValue` before token analysis. This sequencing is mandatory: illegal edge CR/LF/control bytes fail, while an already valid legacy value still receives the existing `TrimSpace` normalization. A placeholder candidate is any balanced, non-nested `{{...}}` containing 1 through 128 characters other than `{`, `}`, CR, or LF. Only the ten exact case-sensitive DEEIX tokens are known. Preserve every other balanced candidate verbatim and emit one deduplicated `unknown_token` warning per Header/token pair, including lowercase and dotted/vendor forms. Preserve unmatched `{{` or `}}` verbatim and emit `malformed_token`; never evaluate expressions.

`RenderHeaderTemplate` clones input, applies `strings.ReplaceAll` for known tokens, preserves unknown candidates, calls `ValidateRenderedCustomHeaders` on the result, and never mutates input.

- [ ] **Step 6: Run unit/race tests and tidy**

```powershell
cd backend
go test ./internal/infra/mcp -count=1
go test -race ./internal/infra/mcp -run "Test(Parse|Render)HeaderTemplate" -count=1
go mod tidy
```

Expected: tests pass and `golang.org/x/net` is direct because production imports `httpguts`.

- [ ] **Step 7: Commit**

```powershell
git add backend/internal/infra/mcp/header_template.go backend/internal/infra/mcp/header_template_test.go backend/go.mod backend/go.sum
git commit -m "feat: add deeix mcp header templates"
```

### Task 4: Harden the Per-Operation MCP Transport

**Files:**
- Modify: `backend/internal/infra/mcp/client.go:21-220`
- Modify: `backend/internal/infra/mcp/client_test.go`

**Interfaces:**
- Consumes: Task 3 validated/rendered custom Headers.
- Produces: frozen `CallConfig`/`CallInput`, negotiated session state, protocol ownership, redirect denial, sanitized HTTP errors, and DELETE.

- [ ] **Step 1: Write a full lifecycle capture test**

Add this complete test to `client_test.go` (merge the listed imports into the existing import block):

```go
func TestClientCallToolOwnsHeadersAcrossLifecycleAndDeletesSession(t *testing.T) {
	t.Parallel()
	type captured struct {
		method    string
		rpcMethod string
		header    http.Header
	}
	var mu sync.Mutex
	requests := make([]captured, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item := captured{method: r.Method, header: r.Header.Clone()}
		var request struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
		}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			item.rpcMethod = request.Method
		}
		mu.Lock()
		requests = append(requests, item)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case request.Method == "initialize":
			w.Header().Set("MCP-Session-Id", "session-1")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result": map[string]interface{}{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]interface{}{},
					"serverInfo":      map[string]string{"name": "test", "version": "1"},
				},
			})
		case request.Method == "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case request.Method == "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result": map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": "ok"}},
				},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, request.Method)
		}
	}))
	defer server.Close()

	client := NewClient()
	_, err := client.CallTool(context.Background(), CallConfig{
		BaseURL:       server.URL,
		AuthToken:     "bearer-value",
		CustomHeaders: map[string]string{"X-Tenant": "tenant-a"},
	}, CallInput{ToolName: "memory.list", ArgumentsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	got := append([]captured(nil), requests...)
	mu.Unlock()
	if len(got) != 4 {
		t.Fatalf("requests = %#v", got)
	}
	methods := []string{"initialize", "notifications/initialized", "tools/call", ""}
	for index, request := range got {
		if request.rpcMethod != methods[index] {
			t.Fatalf("request %d method = %q", index, request.rpcMethod)
		}
		if request.header.Get("X-Tenant") != "tenant-a" ||
			request.header.Get("Authorization") != "Bearer bearer-value" {
			t.Fatalf("request %d lost business/auth Headers: %#v", index, request.header)
		}
		if index == 0 {
			if request.header.Get("MCP-Session-Id") != "" ||
				request.header.Get("MCP-Protocol-Version") != "" {
				t.Fatalf("initialize has session Headers: %#v", request.header)
			}
			continue
		}
		if request.header.Get("MCP-Session-Id") != "session-1" ||
			request.header.Get("MCP-Protocol-Version") != "2025-06-18" {
			t.Fatalf("request %d protocol Headers = %#v", index, request.header)
		}
	}
}
```

- [ ] **Step 2: Write redirect, bypass-validation, negotiation, cleanup, and error leakage tests**

Add the following executable table and redirect/cleanup tests. These tests use only package APIs introduced in Tasks 3–4 and do not rely on manual inspection:

```go
func TestClientRejectsDirectCustomHeaderBypassBeforeDispatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		headers func() map[string]string
	}{
		{name: "authorization", headers: func() map[string]string {
			return map[string]string{"authorization": "attacker"}
		}},
		{name: "signed context", headers: func() map[string]string {
			return map[string]string{"X-DEEIX-Context": "attacker"}
		}},
		{name: "crlf", headers: func() map[string]string {
			return map[string]string{"X-Test": "ok\r\ninjected: true"}
		}},
		{name: "too many", headers: func() map[string]string {
			result := make(map[string]string, 33)
			for i := 0; i < 33; i++ {
				result[fmt.Sprintf("X-Test-%02d", i)] = "value"
			}
			return result
		}},
		{name: "aggregate overflow", headers: func() map[string]string {
			return map[string]string{
				"X-A": strings.Repeat("a", 4096),
				"X-B": strings.Repeat("b", 4096),
				"X-C": strings.Repeat("c", 4096),
				"X-D": strings.Repeat("d", 4096),
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				hits.Add(1)
			}))
			defer server.Close()
			_, err := NewClient().CallTool(context.Background(), CallConfig{
				BaseURL:       server.URL,
				CustomHeaders: tt.headers(),
			}, CallInput{ToolName: "test", ArgumentsJSON: `{}`})
			if err == nil {
				t.Fatal("expected validation error")
			}
			if hits.Load() != 0 {
				t.Fatalf("server received %d requests", hits.Load())
			}
		})
	}
}

func TestClientDeniesRedirectBeforeSensitiveHeadersReachTarget(t *testing.T) {
	t.Parallel()
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	_, err := NewClient().CallTool(context.Background(), CallConfig{
		BaseURL:       redirect.URL,
		AuthToken:     "redirect-secret",
		CustomHeaders: map[string]string{"X-User": "user-public"},
	}, CallInput{ToolName: "test", ArgumentsJSON: `{}`})
	if err == nil {
		t.Fatal("expected redirect denial")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target received %d requests", targetHits.Load())
	}
}

func TestClientDeletesAssignedSessionAfterProtocolMismatch(t *testing.T) {
	t.Parallel()
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("MCP-Session-Id", "session-mismatch")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      request.ID,
			"result": map[string]interface{}{
				"protocolVersion": "unsupported-version",
			},
		})
	}))
	defer server.Close()

	_, err := NewClient().CallTool(context.Background(), CallConfig{BaseURL: server.URL}, CallInput{
		ToolName: "test", ArgumentsJSON: `{}`,
	})
	if !errors.Is(err, ErrUnsupportedProtocolVersion) {
		t.Fatalf("error = %v", err)
	}
	if deletes.Load() != 1 {
		t.Fatalf("DELETE count = %d", deletes.Load())
	}
}

func TestClientErrorNeverContainsRemoteOrURLSecret(t *testing.T) {
	t.Parallel()
	const secret = "echoed-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(secret))
	}))
	defer server.Close()

	_, err := NewClient().CallTool(context.Background(), CallConfig{
		BaseURL: server.URL + "/" + secret,
	}, CallInput{ToolName: "test", ArgumentsJSON: `{}`})
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(SafeErrorSummary(err), secret) {
		t.Fatalf("secret leaked through error: %v", err)
	}
}
```

```go
func TestClientCleanupAfterInitializedFailureAndSessionlessSuccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		sessionID       string
		failInitialized bool
		wantDeletes     int32
	}{
		{name: "initialized failure closes assigned session", sessionID: "session-1", failInitialized: true, wantDeletes: 1},
		{name: "no session skips delete", sessionID: "", failInitialized: false, wantDeletes: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var deletes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deletes.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var request struct {
					ID     interface{} `json:"id"`
					Method string      `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				w.Header().Set("Content-Type", "application/json")
				switch request.Method {
				case "initialize":
					if tt.sessionID != "" {
						w.Header().Set("MCP-Session-Id", tt.sessionID)
					}
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0", "id": request.ID,
						"result": map[string]interface{}{"protocolVersion": "2025-06-18"},
					})
				case "notifications/initialized":
					if tt.failInitialized {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusAccepted)
				case "tools/call":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0", "id": request.ID,
						"result": map[string]interface{}{"content": []interface{}{}},
					})
				default:
					t.Fatalf("unexpected method %q", request.Method)
				}
			}))
			defer server.Close()

			_, err := NewClient().CallTool(context.Background(), CallConfig{BaseURL: server.URL}, CallInput{
				ToolName: "test", ArgumentsJSON: `{}`,
			})
			if tt.failInitialized && err == nil {
				t.Fatal("expected initialized failure")
			}
			if !tt.failInitialized && err != nil {
				t.Fatal(err)
			}
			if got := deletes.Load(); got != tt.wantDeletes {
				t.Fatalf("DELETE count = %d, want %d", got, tt.wantDeletes)
			}
		})
	}
}
```

- [ ] **Step 3: Run tests and confirm failure**

```powershell
cd backend
go test ./internal/infra/mcp -run "TestClient" -count=1
```

Expected: lifecycle, redirect, and leakage assertions fail.

- [ ] **Step 4: Introduce session and frozen call types**

```go
type session struct {
	ID              string
	ProtocolVersion string
}

type CallConfig struct {
	BaseURL       string
	AuthToken     string
	TimeoutMS     int
	CustomHeaders map[string]string
	Context       TemplateContext
}

type CallInput struct {
	ToolName      string
	ArgumentsJSON string
}
```

Parse initialize result protocol version. Accept only the exact supported `2025-06-18`; only a legacy omission falls back to that requested version. A different non-empty version is `ErrUnsupportedProtocolVersion`, and any already assigned session is closed before returning.

- [ ] **Step 5: Apply transport-owned Headers last**

Before constructing any HTTP request, revalidate the endpoint (including no userinfo/query/fragment). Use this single Header writer for POST and DELETE so later code cannot change precedence:

```go
func applyRequestHeaders(req *http.Request, cfg CallConfig, current session) error {
	if err := ValidateRenderedCustomHeaders(cfg.CustomHeaders); err != nil {
		return err
	}
	for name, value := range cfg.CustomHeaders {
		req.Header.Set(name, value)
	}
	if token := strings.TrimSpace(cfg.AuthToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if req.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if current.ID != "" {
		req.Header.Set("MCP-Session-Id", current.ID)
		req.Header.Set("MCP-Protocol-Version", current.ProtocolVersion)
	}
	return nil
}
```

Call this only after `buildEndpointURL` has re-run URL/SSRF validation. The custom map is validated and copied first; bearer, content negotiation, and session/protocol are then transport-owned. This protects runtime even if a legacy row or direct `CallConfig` bypasses persistence.

- [ ] **Step 6: Deny redirects and sanitize errors**

Construct the client with redirect denial at the existing constructor boundary:

```go
return &Client{httpClient: &http.Client{
	Transport: platformtracing.NewHTTPTransport(transport),
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}}
```

For every non-2xx response, drain at most 64 KiB for connection reuse, close the body, and return `ClientError`; never retain or format the body:

```go
_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
return nil, newClientError(ClientErrorHTTP, resp.StatusCode, 0, nil)
```

Add:

```go
type ClientErrorKind string

const (
	ClientErrorNetwork     ClientErrorKind = "network"
	ClientErrorHTTP        ClientErrorKind = "http"
	ClientErrorProtocol    ClientErrorKind = "protocol"
	ClientErrorJSONRPC     ClientErrorKind = "json_rpc"
	ClientErrorToolResult  ClientErrorKind = "tool_result"
)

type ClientError struct {
	Kind       ClientErrorKind
	StatusCode int
	RPCCode    int
	cause      error
}

func SafeErrorSummary(err error) string
```

Implement the functions as follows:

```go
func newClientError(kind ClientErrorKind, statusCode int, rpcCode int, cause error) *ClientError {
	return &ClientError{Kind: kind, StatusCode: statusCode, RPCCode: rpcCode, cause: cause}
}

func (e *ClientError) Error() string {
	if e == nil {
		return "mcp client error"
	}
	return fmt.Sprintf("mcp client error: kind=%s status=%d rpc_code=%d", e.Kind, e.StatusCode, e.RPCCode)
}

func (e *ClientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func safeContextCause(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func SafeErrorSummary(err error) string {
	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		return clientErr.Error()
	}
	if errors.Is(err, context.Canceled) {
		return "mcp client error: kind=network canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "mcp client error: kind=network deadline_exceeded"
	}
	return "mcp client error: kind=internal"
}
```

On `http.Client.Do` failure, use `safeContextCause` and discard the raw `url.Error` string. `application/mcp` persists only `SafeErrorSummary(err)` to `LastError`/system event.

- [ ] **Step 7: Terminate sessions**

Change initialize to return `(session, error)` even when protocol decoding/negotiation or the initialized notification fails. In both `ListTools` and `CallTool`, install cleanup before checking the primary error:

```go
current, err := c.initialize(ctx, cfg)
if current.ID != "" {
	defer func() { _ = c.terminateSession(ctx, cfg, current) }()
}
if err != nil {
	return nil, err // CallTool returns "", err at its corresponding site.
}
```

Add the complete terminator:

```go
func (c *Client) terminateSession(ctx context.Context, cfg CallConfig, current session) error {
	if current.ID == "" {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	endpoint, err := buildEndpointURL(cfg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(cleanupCtx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return newClientError(ClientErrorProtocol, 0, 0, err)
	}
	if err = applyRequestHeaders(req, cfg, current); err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return newClientError(ClientErrorNetwork, 0, 0, safeContextCause(cleanupCtx, err))
	}
	defer resp.Body.Close() //nolint:errcheck
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if (resp.StatusCode >= 200 && resp.StatusCode < 300) ||
		resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil
	}
	return newClientError(ClientErrorHTTP, resp.StatusCode, 0, nil)
}
```

`safeContextCause` returns only `context.Canceled`/`context.DeadlineExceeded` when applicable and otherwise nil. Cleanup never replaces the primary list/call result. Skip DELETE only when no session was assigned.

- [ ] **Step 8: Run unit/race tests**

```powershell
cd backend
go test ./internal/infra/mcp -count=1
go test -race ./internal/infra/mcp -count=1
```

Expected: both pass.

- [ ] **Step 9: Commit**

```powershell
git add backend/internal/infra/mcp/client.go backend/internal/infra/mcp/client_test.go
git commit -m "fix: harden mcp streamable http sessions"
```

### Task 5: Reuse the Kernel for Server CRUD, Preview, Probe, and Sync

**Files:**
- Modify: `backend/internal/application/mcp/service.go`
- Modify: `backend/internal/application/mcp/service_test.go`
- Modify: `backend/internal/app/app.go:191-281`

**Interfaces:**
- Consumes: Task 3 parser/renderer and Task 4 `CallConfig`.
- Produces: `UserProfileResolver`, one exported shared `BuildCallConfig`, `PreviewHeaderTemplate`, `ProbeServer`, system `SyncServerTools`, safe summaries, and prompt-preset-style audit recording.

- [ ] **Step 1: Write probe/sync mode tests**

Add this complete application test to `service_test.go`. Embedding `repository.MCPRepository` makes an unexpected repository call panic instead of silently succeeding:

```go
type captureMCPToolLister struct {
	calls []inframcp.CallConfig
}

func (c *captureMCPToolLister) ListTools(_ context.Context, cfg inframcp.CallConfig) ([]inframcp.Tool, error) {
	c.calls = append(c.calls, cfg)
	return []inframcp.Tool{{Name: "memory.list", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}

type mcpApplicationRepoStub struct {
	repository.MCPRepository
	server       domainmcp.Server
	replaced     int
	storedTools  []domainmcp.Tool
}

func (r *mcpApplicationRepoStub) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	copy := r.server
	return &copy, nil
}

func (r *mcpApplicationRepoStub) ReplaceServerTools(_ context.Context, _ uint, tools []domainmcp.Tool) error {
	r.replaced++
	r.storedTools = append([]domainmcp.Tool(nil), tools...)
	return nil
}

func (r *mcpApplicationRepoStub) ListTools(context.Context, uint, bool) ([]domainmcp.Tool, error) {
	return append([]domainmcp.Tool(nil), r.storedTools...), nil
}

type userProfileResolverStub struct {
	user domainuser.User
}

func (r userProfileResolverStub) GetByID(context.Context, uint) (*domainuser.User, error) {
	copy := r.user
	return &copy, nil
}

func TestServiceProbeServerAndSyncServerUseAuthoritativeModes(t *testing.T) {
	t.Parallel()
	repo := &mcpApplicationRepoStub{server: domainmcp.Server{
		ID:          9,
		Name:        "Memory",
		BaseURL:     "https://mcp.example.test/mcp",
		HeadersJSON: `{"X-Subject":"{{DEEIX_USER_PUBLIC_ID}}","X-Run":"{{DEEIX_RUN_ID}}","X-Request":"{{DEEIX_REQUEST_ID}}"}`,
		Status:      "active",
	}}
	lister := &captureMCPToolLister{}
	service := NewServiceWithRuntime(
		config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}),
		repo,
		lister,
	)
	service.SetUserProfileResolver(userProfileResolverStub{user: domainuser.User{
		ID: 3, PublicID: "user-admin", Username: "admin", DisplayName: "Admin",
		Email: "admin@example.test", Role: domainuser.RoleAdmin,
	}})

	probe, err := service.ProbeServer(context.Background(), ProbeServerInput{
		ServerID: 9, ActorUserID: 3, RequestID: "request-probe",
	})
	if err != nil {
		t.Fatal(err)
	}
	if probe.ToolCount != 1 || repo.replaced != 0 {
		t.Fatalf("probe = %#v replaced=%d", probe, repo.replaced)
	}
	_, err = service.SyncServerTools(context.Background(), SyncServerToolsInput{
		ServerID: 9, RequestID: "request-sync",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.replaced != 1 || len(lister.calls) != 2 {
		t.Fatalf("replaced=%d calls=%d", repo.replaced, len(lister.calls))
	}

	probeCfg, syncCfg := lister.calls[0], lister.calls[1]
	if probeCfg.Context.Mode != inframcp.ContextModeProbe ||
		probeCfg.Context.UserPublicID != "user-admin" ||
		probeCfg.Context.ConversationPublicID != "" ||
		probeCfg.Context.AssistantMessagePublicID != "" ||
		probeCfg.Context.UserMessagePublicID != "" ||
		probeCfg.Context.RunID != "" ||
		probeCfg.Context.RequestID != "request-probe" ||
		probeCfg.CustomHeaders["X-Subject"] != "user-admin" ||
		probeCfg.CustomHeaders["X-Run"] != "" {
		t.Fatalf("probe config = %#v", probeCfg)
	}
	if syncCfg.Context.Mode != inframcp.ContextModeSync ||
		syncCfg.Context.UserPublicID != "" ||
		syncCfg.Context.UserEmail != "" ||
		syncCfg.Context.ConversationPublicID != "" ||
		syncCfg.Context.AssistantMessagePublicID != "" ||
		syncCfg.Context.UserMessagePublicID != "" ||
		syncCfg.Context.RunID != "" ||
		syncCfg.Context.RequestID != "request-sync" ||
		syncCfg.CustomHeaders["X-Subject"] != "" ||
		syncCfg.CustomHeaders["X-Run"] != "" {
		t.Fatalf("sync config = %#v", syncCfg)
	}
}
```

- [ ] **Step 2: Run tests and confirm failure**

```powershell
cd backend
go test ./internal/application/mcp -run "TestService(Probe|Sync)Server" -count=1
```

Expected: compilation fails because probe and context-aware configuration do not exist.

- [ ] **Step 3: Add narrow resolver and inputs**

```go
type UserProfileResolver interface {
	GetByID(ctx context.Context, userID uint) (*domainuser.User, error)
}

type ProbeServerInput struct {
	ServerID    uint
	ActorUserID uint
	RequestID   string
}

type ProbeServerResult struct {
	ToolCount int
	Analysis  inframcp.HeaderTemplateAnalysis
}

type MCPToolLister interface {
	ListTools(context.Context, inframcp.CallConfig) ([]inframcp.Tool, error)
}

var (
	ErrInvalidHeaderTemplate     = errors.New("invalid mcp Header template")
	ErrInvalidHeaderTemplateMode = errors.New("invalid mcp Header template mode")
	ErrUnsafeMCPServerTarget     = errors.New("unsafe mcp server target")
	ErrMCPServerProbeFailed      = errors.New("mcp server probe failed")
	ErrMCPServerSyncFailed       = errors.New("mcp server sync failed")
)
```

Change `NewServiceWithRuntime`'s third parameter and the corresponding `Service` field from concrete `*inframcp.Client` to `MCPToolLister`; the existing client satisfies it without an adapter. Inject existing `userRepo` through `SetUserProfileResolver` in `app.go`.

- [ ] **Step 4: Centralize call configuration for every application path**

Implement this exported, stateless method on `application/mcp.Service`:

```go
func (s *Service) BuildCallConfig(
	ctx context.Context,
	server domainmcp.Server,
	templateContext inframcp.TemplateContext,
	timeoutMS int,
) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error)
```

Implement it with this exact data flow:

```go
func (s *Service) BuildCallConfig(
	ctx context.Context,
	server domainmcp.Server,
	templateContext inframcp.TemplateContext,
	timeoutMS int,
) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error) {
	_ = ctx // reserved for phase-C secret/policy resolution; no browser values are read.
	if err := s.validateServerBaseURL(server.BaseURL); err != nil {
		return inframcp.CallConfig{}, inframcp.HeaderTemplateAnalysis{},
			fmt.Errorf("%w", ErrUnsafeMCPServerTarget)
	}
	token, err := s.decryptToken(server.AuthTokenEnc)
	if err != nil {
		return inframcp.CallConfig{}, inframcp.HeaderTemplateAnalysis{}, err
	}
	parsed, err := inframcp.ParseHeaderTemplateJSON(server.HeadersJSON)
	if err != nil {
		return inframcp.CallConfig{}, inframcp.HeaderTemplateAnalysis{},
			fmt.Errorf("%w", ErrInvalidHeaderTemplate)
	}
	headers, warnings, err := inframcp.RenderHeaderTemplate(parsed.Template, templateContext)
	if err != nil {
		return inframcp.CallConfig{}, inframcp.HeaderTemplateAnalysis{},
			fmt.Errorf("%w", ErrInvalidHeaderTemplate)
	}
	analysis := parsed.Analysis
	analysis.Warnings = append([]inframcp.HeaderTemplateWarning(nil), warnings...)
	return inframcp.CallConfig{
		BaseURL:       strings.TrimSpace(server.BaseURL),
		AuthToken:     token,
		TimeoutMS:     timeoutMS,
		CustomHeaders: headers,
		Context:       templateContext,
	}, analysis, nil
}
```

The parser/renderer returns a new map and never mutates stored data. Probe and sync call this method directly. Task 6 injects the same service behind a narrow conversation-local interface; conversation must not duplicate decryption, parsing, blacklist handling, or error sanitization. Phase C extends only this builder to attach `SignedContextConfig`.

- [ ] **Step 5: Implement fixed-data preview**

Add these exact application types/signature:

```go
type HeaderPreviewItem struct {
	Name      string
	Value     string
	Sensitive bool
}

type PreviewHeaderTemplateResult struct {
	Mode            inframcp.ContextMode
	SupportedTokens []string
	Warnings        []inframcp.HeaderTemplateWarning
	Headers         []HeaderPreviewItem
}

func (s *Service) PreviewHeaderTemplate(
	_ context.Context,
	raw string,
	mode inframcp.ContextMode,
) (PreviewHeaderTemplateResult, error) {
	if mode != inframcp.ContextModeChat &&
		mode != inframcp.ContextModeProbe &&
		mode != inframcp.ContextModeSync {
		return PreviewHeaderTemplateResult{}, ErrInvalidHeaderTemplateMode
	}
	previewContext := inframcp.TemplateContext{Mode: mode, RequestID: "request_example"}
	if mode == inframcp.ContextModeChat || mode == inframcp.ContextModeProbe {
		previewContext.UserPublicID = "user_example"
		previewContext.UserDisplayName = "name_example"
		previewContext.UserEmail = "email_example@example.test"
		previewContext.UserRole = "role_example"
	}
	if mode == inframcp.ContextModeChat {
		previewContext.ConversationPublicID = "conversation_example"
		previewContext.AssistantMessagePublicID = "assistant_message_example"
		previewContext.UserMessagePublicID = "user_message_example"
		previewContext.RunID = "run_example"
		previewContext.TraceID = "trace_example"
	}
	parsed, err := inframcp.ParseHeaderTemplateJSON(raw)
	if err != nil {
		return PreviewHeaderTemplateResult{}, ErrInvalidHeaderTemplate
	}
	headers, warnings, err := inframcp.RenderHeaderTemplate(parsed.Template, previewContext)
	if err != nil {
		return PreviewHeaderTemplateResult{}, ErrInvalidHeaderTemplate
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]HeaderPreviewItem, 0, len(names))
	for _, name := range names {
		sensitive := security.IsSensitiveHeaderName(name)
		value := headers[name]
		if sensitive {
			value = security.RedactedHeaderValue
		}
		items = append(items, HeaderPreviewItem{Name: name, Value: value, Sensitive: sensitive})
	}
	return PreviewHeaderTemplateResult{
		Mode: mode, SupportedTokens: inframcp.SupportedHeaderTemplateTokens(),
		Warnings: warnings, Headers: items,
	}, nil
}
```

This method never loads a real user or returns a real rendered sensitive value.

- [ ] **Step 6: Implement probe and system sync**

Implement the context construction at the call sites exactly as follows; both methods then pass the context to the shared `BuildCallConfig` and call `MCPToolLister.ListTools` once:

```go
profile, err := s.userProfileResolver.GetByID(ctx, input.ActorUserID)
if err != nil {
	return ProbeServerResult{}, err
}
displayName := strings.TrimSpace(profile.DisplayName)
if displayName == "" {
	displayName = strings.TrimSpace(profile.Username)
}
probeContext := inframcp.TemplateContext{
	Mode:            inframcp.ContextModeProbe,
	UserPublicID:    strings.TrimSpace(profile.PublicID),
	UserDisplayName: displayName,
	UserEmail:       strings.TrimSpace(profile.Email),
	UserRole:        strings.TrimSpace(profile.Role),
	RequestID:       strings.TrimSpace(input.RequestID),
}

syncContext := inframcp.TemplateContext{
	Mode:      inframcp.ContextModeSync,
	RequestID: strings.TrimSpace(input.RequestID),
}
```

`ProbeServer` returns tool count/analysis without calling `ReplaceServerTools`. `SyncServerTools` is the only path that converts the returned tools and calls `ReplaceServerTools`; it must persist only `SafeErrorSummary(err)` on failure, never a remote body.

Use stable wrapping at the client boundary:

```go
tools, err := s.client.ListTools(ctx, callConfig)
if err != nil {
	// Probe path:
	return ProbeServerResult{}, fmt.Errorf("%w", ErrMCPServerProbeFailed)
	// Sync uses the same branch with ErrMCPServerSyncFailed and persists only
	// inframcp.SafeErrorSummary(err) before returning the stable sentinel.
}
```

Do not use `%v`, `err.Error()`, or the remote body in the returned sentinel, audit detail, system event, or `last_error`.

- [ ] **Step 7: Add exact audit support and application wiring**

Follow the prompt-preset pattern exactly:

```go
type AuditInput struct {
	UserID     uint
	RequestID  string
	Action     string
	ResourceID string
	ClientIP   string
	UserAgent  string
	Detail     interface{}
}

func (s *Service) SetAuditWriter(writer auditWriter)
func (s *Service) RecordAudit(ctx context.Context, input AuditInput)
```

`RecordAudit` writes resource `mcp_servers`. In `app.go`, call `mcpService.SetAuditWriter(auditService)`. Task 7 handlers construct the input with `middleware.MustUserID`, `middleware.MustRequestID`, `c.ClientIP()`, and `c.Request.UserAgent()`.

Freeze actions as `mcp.server.create`, `mcp.server.update`, `mcp.server.delete`, `mcp.server.status_update`, `mcp.server.reorder`, `mcp.server.probe`, and `mcp.server.sync`. Detail contains only `outcome`, changed field names, token names, warning codes, Server/tool counts, and a stable error code. Never include template JSON, values, bearer, email, URL query, or remote error text.

- [ ] **Step 8: Run tests**

```powershell
cd backend
go test ./internal/application/mcp -count=1
```

Expected: pass.

- [ ] **Step 9: Commit**

```powershell
git add backend/internal/application/mcp backend/internal/app/app.go
git commit -m "feat: add mcp header preview and probe"
```

### Task 6: Build Authoritative Chat Context and Public MCP Metadata

**Files:**
- Create: `backend/internal/application/conversation/service_mcp_context.go`
- Create: `backend/internal/application/conversation/service_mcp_context_test.go`
- Modify: `backend/internal/application/conversation/service.go`
- Modify: `backend/internal/application/conversation/service_mcp_tools.go:18-242`
- Modify: `backend/internal/application/conversation/service_message_send.go:713`
- Modify: `backend/internal/application/conversation/service_tool.go:13-49`
- Modify: `backend/internal/application/conversation/service_tool_execution.go:28-155`
- Modify: `backend/internal/infra/mcp/client.go:92-115`
- Modify: `backend/internal/transport/http/middleware/request_id.go`
- Modify: `backend/internal/transport/http/middleware/request_id_test.go`
- Modify: `backend/internal/app/app.go`

**Interfaces:**
- Consumes: Task 3 `TemplateContext` and Task 4 `CallConfig`.
- Produces: one context per run, one rendered map per Server, and public-only `_meta`.

- [ ] **Step 1: Write pure constructor tests**

Create `service_mcp_context_test.go` with the executable constructor test below:

```go
func TestNewMCPTemplateContextUsesOnlyAuthoritativePublicValues(t *testing.T) {
	t.Parallel()
	ctx := traceid.WithTraceID(context.Background(), "4bf92f3577b34da6a3ce929d0e0e4736")
	got := newMCPTemplateContext(
		ctx,
		domainuser.User{
			ID: 99, PublicID: "user-public", Username: "fallback-name",
			DisplayName: "", Email: "user@example.test", Role: domainuser.RoleUser,
		},
		domainconversation.Conversation{ID: 88, PublicID: "conversation-public"},
		domainconversation.Message{ID: 77, PublicID: "user-message-public"},
		domainconversation.Message{ID: 66, PublicID: "assistant-message-public"},
		"request-public",
		"run-public",
	)
	want := inframcp.TemplateContext{
		Mode:                     inframcp.ContextModeChat,
		UserPublicID:             "user-public",
		UserDisplayName:          "fallback-name",
		UserEmail:                "user@example.test",
		UserRole:                 domainuser.RoleUser,
		ConversationPublicID:     "conversation-public",
		AssistantMessagePublicID: "assistant-message-public",
		UserMessagePublicID:      "user-message-public",
		RequestID:                "request-public",
		RunID:                    "run-public",
		TraceID:                  "4bf92f3577b34da6a3ce929d0e0e4736",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("context = %#v, want %#v", got, want)
	}
	serialized := fmt.Sprintf("%#v", got)
	for _, internalID := range []string{"99", "88", "77", "66"} {
		if strings.Contains(serialized, internalID) {
			t.Fatalf("internal ID %s leaked into %#v", internalID, got)
		}
	}
}
```

Add table tests to `request_id_test.go` with this complete assertion loop; the middleware's existing router helper may be reused, but the inputs and assertions must remain exact:

```go
func TestNormalizeRequestID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		input      string
		wantExact  string
		wantNewUUID bool
	}{
		{name: "trim valid", input: "  req_1:a-b.c  ", wantExact: "req_1:a-b.c"},
		{name: "blank", input: "   ", wantNewUUID: true},
		{name: "oversized", input: strings.Repeat("a", 129), wantNewUUID: true},
		{name: "unicode", input: "请求", wantNewUUID: true},
		{name: "control", input: "req\nother", wantNewUUID: true},
		{name: "punctuation", input: "req/value", wantNewUUID: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeRequestID(tt.input)
			if tt.wantNewUUID {
				if _, err := uuid.Parse(got); err != nil || got == strings.TrimSpace(tt.input) {
					t.Fatalf("generated request ID = %q, parse error = %v", got, err)
				}
				return
			}
			if got != tt.wantExact {
				t.Fatalf("request ID = %q, want %q", got, tt.wantExact)
			}
		})
	}
}
```

Add this table to `generation_stream_test.go`:

```go
func TestNormalizeRunIDRejectsUnsafeOrOversizedCandidates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "valid", input: " run_client.1 ", want: "run_client.1"},
		{name: "normalizes uuid dashes", input: "run_123e4567-e89b-12d3-a456-426614174000", want: "run_123e4567e89b12d3a456426614174000"},
		{name: "blank", input: "   ", want: ""},
		{name: "slash", input: "run_bad/value", want: ""},
		{name: "control", input: "run_bad\nvalue", want: ""},
		{name: "unicode", input: "run_运行", want: ""},
		{name: "oversized", input: "run_" + strings.Repeat("a", 61), want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeRunID(tt.input); got != tt.want {
				t.Fatalf("normalizeRunID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
```

In the message-send test which exercises blank client run ID, use this assertion; generation remains at the existing caller after `normalizeRunID` returns empty:

```go
if !regexp.MustCompile(`^run_[0-9a-f]{32}$`).MatchString(result.RunID) {
	t.Fatalf("generated run ID = %q", result.RunID)
}
```

Run: `cd backend && go test ./internal/application/conversation ./internal/transport/http/middleware -run "Test(NewMCPTemplateContext|NormalizeRequestID|NormalizeRunID)" -count=1`

Expected: FAIL because `newMCPTemplateContext` and `normalizeRequestID` do not exist and current run-ID normalization does not enforce the frozen bound.

- [ ] **Step 2: Implement constructor**

```go
func newMCPTemplateContext(
	ctx context.Context,
	user domainuser.User,
	conversation domainconversation.Conversation,
	userMessage domainconversation.Message,
	assistantMessage domainconversation.Message,
	requestID string,
	runID string,
) inframcp.TemplateContext {
	displayName := strings.TrimSpace(user.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(user.Username)
	}
	return inframcp.TemplateContext{
		Mode:                     inframcp.ContextModeChat,
		UserPublicID:             strings.TrimSpace(user.PublicID),
		UserDisplayName:          displayName,
		UserEmail:                strings.TrimSpace(user.Email),
		UserRole:                 strings.TrimSpace(user.Role),
		ConversationPublicID:     strings.TrimSpace(conversation.PublicID),
		AssistantMessagePublicID: strings.TrimSpace(assistantMessage.PublicID),
		UserMessagePublicID:      strings.TrimSpace(userMessage.PublicID),
		RequestID:                strings.TrimSpace(requestID),
		RunID:                    strings.TrimSpace(runID),
		TraceID:                  strings.ToLower(strings.TrimSpace(traceid.FromContext(ctx))),
	}
}
```

Use the repository's `backend/internal/pkg/traceid.FromContext(ctx)`. Tests cover both a valid OpenTelemetry span and the middleware/fallback DEEIX trace ID when no valid span exists.

Add these exact normalizers and apply `normalizeRequestID` before setting Gin/request-context/response Header state:

```go
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func normalizeRequestID(raw string) string {
	value := strings.TrimSpace(raw)
	if !requestIDPattern.MatchString(value) {
		return uuid.NewString()
	}
	return value
}
```

Replace the current permissive `normalizeRunID` with:

```go
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func normalizeRunID(raw string) string {
	value := strings.TrimSpace(raw)
	if !runIDPattern.MatchString(value) {
		return ""
	}
	value = strings.ReplaceAll(value, "-", "")
	if !strings.HasPrefix(value, "run_") {
		value = "run_" + value
	}
	if len(value) > 64 {
		return ""
	}
	return value
}
```

Request/run/trace values are signed correlation metadata only; authenticated user/entity ownership remains the authorization source.

- [ ] **Step 3: Run focused tests**

```powershell
cd backend
go test ./internal/application/conversation ./internal/transport/http/middleware -run "Test(NewMCPTemplateContext|NormalizeRequestID|NormalizeRunID)" -count=1
```

Expected: pass after implementation.

- [ ] **Step 4: Make selected-tool resolution fail closed**

Add this test before changing the resolver:

```go
var errMCPCallConfigFixture = errors.New("call config fixture failed")

type conversationMCPRepoStub struct {
	repository.MCPRepository
}

func (conversationMCPRepoStub) ListToolsByIDs(context.Context, []uint) ([]domainmcp.Tool, error) {
	return []domainmcp.Tool{{
		ID: 1, ServerID: 9, Name: "memory.list", DisplayName: "Memory",
		InputSchemaJSON: `{"type":"object"}`, Status: "active",
	}}, nil
}

func (conversationMCPRepoStub) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	return &domainmcp.Server{ID: 9, Name: "Memory", BaseURL: "https://mcp.example.test/mcp", Status: "active"}, nil
}

type failingMCPCallConfigBuilder struct{}

func (failingMCPCallConfigBuilder) BuildCallConfig(
	context.Context,
	domainmcp.Server,
	inframcp.TemplateContext,
	int,
) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error) {
	return inframcp.CallConfig{}, inframcp.HeaderTemplateAnalysis{}, errMCPCallConfigFixture
}

func TestResolveSelectedToolRuntimeFailsClosedOnCallConfigError(t *testing.T) {
	t.Parallel()
	service := &Service{
		cfg:     config.NewRuntime(config.Config{MCPEnable: true}),
		mcpRepo: conversationMCPRepoStub{},
	}
	service.SetMCPCallConfigBuilder(failingMCPCallConfigBuilder{})
	_, err := service.resolveSelectedToolRuntime(
		context.Background(),
		[]uint{1},
		inframcp.TemplateContext{
			Mode: inframcp.ContextModeChat, UserPublicID: "user-public", RunID: "run-public",
		},
	)
	if !errors.Is(err, errMCPCallConfigFixture) {
		t.Fatalf("error = %v", err)
	}
}
```

Run: `cd backend && go test ./internal/application/conversation -run TestResolveSelectedToolRuntimeFailsClosed -count=1`

Expected: FAIL because the setter, three-argument resolver, and `(selectedToolRuntime, error)` result do not exist.

Define this narrow interface in `application/conversation`:

```go
type mcpCallConfigBuilder interface {
	BuildCallConfig(
		context.Context,
		domainmcp.Server,
		inframcp.TemplateContext,
		int,
	) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error)
}
```

Add `SetMCPCallConfigBuilder` to `conversation.Service` and inject `application/mcp.Service` from `app.go`. Change selected-tool resolution to return `(selectedToolRuntime, error)` and accept `TemplateContext`. Invoke the builder once per Server, cache one `CallConfig` per Server, and return typed errors instead of silently dropping selected tools. Do not parse Header JSON or decrypt MCP secrets in conversation.

Run: `cd backend && go test ./internal/application/conversation -run TestResolveSelectedToolRuntimeFailsClosed -count=1`

Expected: PASS; the builder sentinel is returned and no selected tool is silently dropped.

- [ ] **Step 5: Build context after message persistence**

Replace the current one-line tool-runtime construction after message persistence with this exact flow:

```go
toolRuntime := selectedToolRuntime{}
if len(input.SelectedToolIDs) > 0 {
	profile, profileErr := s.repo.GetUserByID(ctx, input.UserID)
	if profileErr != nil {
		retErr = profileErr
		return nil, profileErr
	}
	templateContext := newMCPTemplateContext(
		ctx,
		*profile,
		*conversation,
		*userMessage,
		*assistantMessage,
		strings.TrimSpace(input.RequestID),
		run.RunID,
	)
	var runtimeErr error
	toolRuntime, runtimeErr = s.resolveSelectedToolRuntime(
		ctx,
		input.SelectedToolIDs,
		templateContext,
	)
	if runtimeErr != nil {
		retErr = runtimeErr
		return nil, runtimeErr
	}
}
```

This block runs only after the conversation plus user/assistant messages have their persisted public IDs. It loads the user once, returns any context/config error before the first LLM request, and never reconstructs values from browser-supplied metadata.

- [ ] **Step 6: Replace numeric `_meta`**

First add this focused test to `client_test.go`:

```go
func TestBuildCallToolParamsUsesPublicMetadataOnly(t *testing.T) {
	t.Parallel()
	params, err := buildCallToolParams(CallConfig{Context: TemplateContext{
		UserPublicID:             "user-public",
		ConversationPublicID:     "conversation-public",
		AssistantMessagePublicID: "assistant-public",
		UserMessagePublicID:      "user-message-public",
		RequestID:                "request-public",
		RunID:                    "run-public",
		TraceID:                  "trace-public",
	}}, CallInput{ToolName: "memory.list", ArgumentsJSON: `{"scope":"user"}`})
	if err != nil {
		t.Fatal(err)
	}
	meta, ok := params["_meta"].(map[string]interface{})
	if !ok {
		t.Fatalf("meta = %#v", params["_meta"])
	}
	want := map[string]interface{}{
		"deeix_user_public_id":              "user-public",
		"deeix_conversation_public_id":      "conversation-public",
		"deeix_assistant_message_public_id": "assistant-public",
		"deeix_user_message_public_id":      "user-message-public",
		"deeix_request_id":                  "request-public",
		"deeix_run_id":                      "run-public",
		"deeix_trace_id":                    "trace-public",
	}
	if !reflect.DeepEqual(meta, want) {
		t.Fatalf("meta = %#v, want %#v", meta, want)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"user_id", "conversation_id", `"99"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("forbidden metadata %q in %s", forbidden, encoded)
		}
	}
}
```

Run: `cd backend && go test ./internal/infra/mcp -run TestBuildCallToolParamsUsesPublicMetadataOnly -count=1`

Expected: FAIL because `buildCallToolParams` does not exist and the current `CallInput` still owns numeric IDs.

Extract parameter construction from `CallTool` into this complete helper and have `CallTool` pass its result unchanged to `rpc`:

```go
func buildCallToolParams(cfg CallConfig, input CallInput) (map[string]interface{}, error) {
	toolName := strings.TrimSpace(input.ToolName)
	if toolName == "" {
		return nil, fmt.Errorf("mcp tool name is empty")
	}
	arguments, err := decodeArguments(input.ArgumentsJSON)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"name":      toolName,
		"arguments": arguments,
		"_meta": map[string]interface{}{
			"deeix_user_public_id":              cfg.Context.UserPublicID,
			"deeix_conversation_public_id":      cfg.Context.ConversationPublicID,
			"deeix_assistant_message_public_id": cfg.Context.AssistantMessagePublicID,
			"deeix_user_message_public_id":      cfg.Context.UserMessagePublicID,
			"deeix_request_id":                  cfg.Context.RequestID,
			"deeix_run_id":                      cfg.Context.RunID,
			"deeix_trace_id":                    cfg.Context.TraceID,
		},
	}, nil
}
```

Do not include display name, email, role, or internal IDs.

Run: `cd backend && go test ./internal/infra/mcp -run TestBuildCallToolParamsUsesPublicMetadataOnly -count=1`

Expected: PASS; serialized params contain all seven public correlation keys and none of the removed numeric keys.

- [ ] **Step 7: Add concurrency isolation test**

Add this race-safe builder and test to `service_mcp_context_test.go`:

```go
type echoMCPCallConfigBuilder struct{}

func (echoMCPCallConfigBuilder) BuildCallConfig(
	_ context.Context,
	server domainmcp.Server,
	templateContext inframcp.TemplateContext,
	timeoutMS int,
) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error) {
	return inframcp.CallConfig{
		BaseURL: server.BaseURL,
		TimeoutMS: timeoutMS,
		Context: templateContext,
		CustomHeaders: map[string]string{
			"X-Request": templateContext.RequestID,
			"X-User":    templateContext.UserPublicID,
		},
	}, inframcp.HeaderTemplateAnalysis{}, nil
}

func TestResolveSelectedToolRuntimeDoesNotCrossConcurrentContexts(t *testing.T) {
	service := &Service{
		cfg:     config.NewRuntime(config.Config{MCPEnable: true, MCPToolTimeoutSeconds: 10}),
		mcpRepo: conversationMCPRepoStub{},
	}
	service.SetMCPCallConfigBuilder(echoMCPCallConfigBuilder{})

	type result struct {
		requestID string
		userID    string
		err       error
	}
	results := make(chan result, 2)
	for _, item := range []struct{ requestID, userID string }{
		{requestID: "request-a", userID: "user-a"},
		{requestID: "request-b", userID: "user-b"},
	} {
		item := item
		go func() {
			runtime, err := service.resolveSelectedToolRuntime(
				context.Background(), []uint{1}, inframcp.TemplateContext{
					Mode: inframcp.ContextModeChat, RequestID: item.requestID,
					UserPublicID: item.userID, RunID: "run-" + item.requestID,
				},
			)
			if err != nil {
				results <- result{err: err}
				return
			}
			cfg := runtime.mcpConfigs["memory_list"]
			results <- result{
				requestID: cfg.CustomHeaders["X-Request"],
				userID:    cfg.CustomHeaders["X-User"],
			}
		}()
	}
	seen := map[string]string{}
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		seen[got.requestID] = got.userID
	}
	if !reflect.DeepEqual(seen, map[string]string{
		"request-a": "user-a",
		"request-b": "user-b",
	}) {
		t.Fatalf("crossed contexts: %#v", seen)
	}
}
```

Run: `cd backend && go test -race ./internal/application/conversation -run TestResolveSelectedToolRuntimeDoesNotCrossConcurrentContexts -count=1`

Expected: PASS with no race report and exactly two isolated request/user pairs.

- [ ] **Step 8: Run tests**

```powershell
cd backend
go test ./internal/application/conversation ./internal/infra/mcp ./internal/transport/http/middleware -count=1
go test -race ./internal/application/conversation ./internal/infra/mcp ./internal/transport/http/middleware -run "Test(MCP|Client|RequestID)" -count=1
```

Expected: pass without races.

- [ ] **Step 9: Commit**

```powershell
git add backend/internal/application/conversation backend/internal/infra/mcp backend/internal/transport/http/middleware backend/internal/app/app.go
git commit -m "feat: add authoritative mcp call context"
```

### Task 7: Expose Preview and Probe APIs, Errors, Audit, and Swagger

**Files:**
- Modify: `backend/internal/transport/http/mcp/dto.go`
- Modify: `backend/internal/transport/http/mcp/handler.go`
- Modify: `backend/internal/transport/http/mcp/router.go`
- Modify: `backend/internal/transport/http/mcp/handler_test.go`
- Modify: `backend/internal/shared/response/error_code.go`
- Modify: `backend/docs/docs.go`
- Modify: `backend/docs/swagger.json`
- Modify: `backend/docs/swagger.yaml`

**Interfaces:**
- Consumes: Tasks 1, 3, and 5 contracts.
- Produces: stable preview/probe JSON, safe errors, admin routes, audit calls, and Swagger.

- [ ] **Step 1: Define safe DTOs**

```go
type HeaderTemplateWarningResponse struct {
	Code       string `json:"code"`
	HeaderName string `json:"headerName,omitempty"`
	Token      string `json:"token,omitempty"`
}

type HeaderPreviewItemResponse struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
}

type PreviewHeaderTemplateRequest struct {
	HeadersJSON string `json:"headersJSON" binding:"required,max=32768"`
	Mode        string `json:"mode" binding:"required,oneof=chat probe sync"`
}

type HeaderTemplatePreviewResponse struct {
	Mode            string                          `json:"mode"`
	SupportedTokens []string                        `json:"supportedTokens"`
	Warnings        []HeaderTemplateWarningResponse `json:"warnings"`
	Headers         []HeaderPreviewItemResponse     `json:"headers"`
}

type ProbeServerResponse struct {
	ToolCount int                             `json:"toolCount"`
	Warnings  []HeaderTemplateWarningResponse `json:"warnings"`
}
```

Response contains mode, supported tokens, warnings, and preview items. Probe response contains tool count and warnings only.

Before implementing the routes, add these complete failing tests to `handler_test.go`:

```go
func TestPreviewHeaderTemplateHandlerReturnsAuthoritativeRedactedPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := appmcp.NewServiceWithRuntime(config.NewRuntime(config.Config{}), nil, nil)
	handler := NewHandler(service)
	router := gin.New()
	router.POST("/api/v1/admin/mcp/header-templates/preview", handler.PreviewHeaderTemplate)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/mcp/header-templates/preview",
		strings.NewReader(`{"headersJSON":"{\"X-API-Key\":\"{{DEEIX_USER_PUBLIC_ID}}\"}","mode":"chat"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		ErrorCode string                        `json:"errorCode"`
		Data      HeaderTemplatePreviewResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ErrorCode != "" || envelope.Data.Mode != "chat" ||
		len(envelope.Data.SupportedTokens) != 10 || len(envelope.Data.Headers) != 1 ||
		envelope.Data.Headers[0].Name != "X-API-Key" ||
		envelope.Data.Headers[0].Value != security.RedactedHeaderValue ||
		!envelope.Data.Headers[0].Sensitive {
		t.Fatalf("response = %#v", envelope)
	}
}

type probeHandlerRepoStub struct {
	repository.MCPRepository
}

func (probeHandlerRepoStub) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	return &domainmcp.Server{
		ID: 9, Name: "Memory", BaseURL: "https://mcp.example.test/mcp",
		HeadersJSON: `{}`, Status: "active",
	}, nil
}

type probeHandlerListerStub struct{}

func (probeHandlerListerStub) ListTools(context.Context, inframcp.CallConfig) ([]inframcp.Tool, error) {
	return []inframcp.Tool{{Name: "memory.list"}}, nil
}

type probeHandlerUserStub struct{}

func (probeHandlerUserStub) GetByID(context.Context, uint) (*domainuser.User, error) {
	return &domainuser.User{ID: 3, PublicID: "user-admin", Username: "admin", Role: domainuser.RoleAdmin}, nil
}

type probeAuditRecord struct {
	requestID string
	userID    uint
	action    string
	resource  string
	resourceID string
	detail    interface{}
}

type probeAuditCapture struct {
	records []probeAuditRecord
}

func (a *probeAuditCapture) Write(
	_ context.Context,
	requestID string,
	userID uint,
	action string,
	resource string,
	resourceID string,
	_ string,
	_ string,
	detail interface{},
) {
	a.records = append(a.records, probeAuditRecord{
		requestID: requestID, userID: userID, action: action,
		resource: resource, resourceID: resourceID, detail: detail,
	})
}

func TestProbeServerHandlerUsesAuthenticatedActorAndSafeResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := appmcp.NewServiceWithRuntime(
		config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}),
		probeHandlerRepoStub{},
		probeHandlerListerStub{},
	)
	service.SetUserProfileResolver(probeHandlerUserStub{})
	audit := &probeAuditCapture{}
	service.SetAuditWriter(audit)
	handler := NewHandler(service)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, uint(3))
		c.Set(middleware.ContextKeyRequestID, "request-probe")
		c.Next()
	})
	router.POST("/api/v1/admin/mcp/servers/:id/probe", handler.ProbeServer)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/mcp/servers/9/probe", nil)
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		ErrorCode string              `json:"errorCode"`
		RequestID string              `json:"requestId"`
		Data      ProbeServerResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ErrorCode != "" || envelope.Data.ToolCount != 1 {
		t.Fatalf("response = %#v", envelope)
	}
	if strings.Contains(recorder.Body.String(), "user-admin") ||
		strings.Contains(recorder.Body.String(), "admin") {
		t.Fatalf("identity leaked in response: %s", recorder.Body.String())
	}
	if len(audit.records) != 1 {
		t.Fatalf("audit records = %#v", audit.records)
	}
	record := audit.records[0]
	if record.requestID != "request-probe" || record.userID != 3 ||
		record.action != "mcp.server.probe" || record.resource != "mcp_servers" ||
		record.resourceID != "9" {
		t.Fatalf("audit record = %#v", record)
	}
	detail, err := json.Marshal(record.detail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(detail), "user-admin") || strings.Contains(string(detail), "admin") {
		t.Fatalf("identity leaked in audit detail: %s", detail)
	}
}

func TestPublicMCPErrorCodeMappings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		code string
	}{
		{appmcp.ErrInvalidHeaderTemplate, response.CodeMCPHeaderTemplateInvalid},
		{appmcp.ErrInvalidHeaderTemplateMode, response.CodeMCPHeaderTemplateInvalidMode},
		{appmcp.ErrInvalidAuthTokenUpdate, response.CodeMCPServerInvalidAuthUpdate},
		{appmcp.ErrMCPServerNotFound, response.CodeMCPServerNotFound},
		{appmcp.ErrUnsafeMCPServerTarget, response.CodeMCPServerUnsafeTarget},
		{appmcp.ErrMCPServerProbeFailed, response.CodeMCPServerProbeFailed},
		{appmcp.ErrMCPServerSyncFailed, response.CodeMCPServerSyncFailed},
	}
	for _, tt := range tests {
		if got := publicMCPErrorCode(tt.err); got != tt.code {
			t.Fatalf("error %v mapped to %q, want %q", tt.err, got, tt.code)
		}
	}
}
```

Run: `cd backend && go test ./internal/transport/http/mcp -run "Test(PreviewHeaderTemplateHandler|ProbeServerHandler|PublicMCPErrorCode)" -count=1`

Expected: FAIL because the DTOs, two handler methods, application sentinels, and public mapper do not exist.

- [ ] **Step 2: Register admin routes**

```go
group.POST("/header-templates/preview", m.Handler.PreviewHeaderTemplate)
group.POST("/servers/:id/probe", m.Handler.ProbeServer)
```

Implement both handlers in `handler.go`:

```go
func (h *Handler) PreviewHeaderTemplate(c *gin.Context) {
	var req PreviewHeaderTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidRequestBody(c, err)
		return
	}
	result, err := h.service.PreviewHeaderTemplate(
		c.Request.Context(),
		req.HeadersJSON,
		inframcp.ContextMode(req.Mode),
	)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	warnings := make([]HeaderTemplateWarningResponse, 0, len(result.Warnings))
	for _, item := range result.Warnings {
		warnings = append(warnings, HeaderTemplateWarningResponse{
			Code: item.Code, HeaderName: item.HeaderName, Token: item.Token,
		})
	}
	headers := make([]HeaderPreviewItemResponse, 0, len(result.Headers))
	for _, item := range result.Headers {
		headers = append(headers, HeaderPreviewItemResponse{
			Name: item.Name, Value: item.Value, Sensitive: item.Sensitive,
		})
	}
	response.Success(c, HeaderTemplatePreviewResponse{
		Mode: string(result.Mode), SupportedTokens: result.SupportedTokens,
		Warnings: warnings, Headers: headers,
	})
}

func (h *Handler) ProbeServer(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	audit := appmcp.AuditInput{
		UserID: middleware.MustUserID(c), RequestID: middleware.MustRequestID(c),
		Action: "mcp.server.probe", ResourceID: strconv.FormatUint(uint64(serverID), 10),
		ClientIP: c.ClientIP(), UserAgent: c.Request.UserAgent(),
	}
	result, err := h.service.ProbeServer(c.Request.Context(), appmcp.ProbeServerInput{
		ServerID: serverID, ActorUserID: audit.UserID, RequestID: audit.RequestID,
	})
	if err != nil {
		audit.Detail = map[string]interface{}{
			"outcome": "error", "errorCode": publicMCPErrorCode(err),
		}
		h.service.RecordAudit(c.Request.Context(), audit)
		writeServiceError(c, err)
		return
	}
	warnings := make([]HeaderTemplateWarningResponse, 0, len(result.Analysis.Warnings))
	for _, item := range result.Analysis.Warnings {
		warnings = append(warnings, HeaderTemplateWarningResponse{
			Code: item.Code, HeaderName: item.HeaderName, Token: item.Token,
		})
	}
	audit.Detail = map[string]interface{}{
		"outcome": "success", "toolCount": result.ToolCount,
		"warningCodes": warningCodes(result.Analysis.Warnings),
	}
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, ProbeServerResponse{ToolCount: result.ToolCount, Warnings: warnings})
}
```

Add this package-local helper; it accepts no Header values:

```go
func warningCodes(items []inframcp.HeaderTemplateWarning) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		code := strings.TrimSpace(item.Code)
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		result = append(result, code)
	}
	sort.Strings(result)
	return result
}
```

- [ ] **Step 3: Map stable errors**

Freeze these additions and statuses:

| Sentinel | `errorCode` | HTTP |
| --- | --- | --- |
| invalid preview JSON/value/name/blacklist/size | `mcp.header_template.invalid` | 400 |
| invalid preview mode | `mcp.header_template.invalid_mode` | 400 |
| invalid bearer PATCH combination | `mcp.server.invalid_auth_token_update` | 400 |
| Server repository not found | `mcp.server.not_found` | 404 |
| SSRF/unsafe target | `mcp.server.unsafe_target` | 400 |
| remote probe failure | `mcp.server.probe_failed` | 502 |
| remote sync failure | `mcp.server.sync_failed` | 502 |
| MCP client unavailable | existing `mcp.client_unavailable` | 503 |

Create/update invalid Header templates continue to use existing `mcp.invalid_server_headers` with 400. Never include remote body, URL, rendered values, or a `ClientError.cause`.

Add these constants to `internal/shared/response/error_code.go`:

```go
const (
	CodeMCPHeaderTemplateInvalid     = "mcp.header_template.invalid"
	CodeMCPHeaderTemplateInvalidMode = "mcp.header_template.invalid_mode"
	CodeMCPServerInvalidAuthUpdate   = "mcp.server.invalid_auth_token_update"
	CodeMCPServerNotFound            = "mcp.server.not_found"
	CodeMCPServerUnsafeTarget        = "mcp.server.unsafe_target"
	CodeMCPServerProbeFailed         = "mcp.server.probe_failed"
	CodeMCPServerSyncFailed          = "mcp.server.sync_failed"
)
```

Add one pure mapper and use it from `writeServiceError`; this is also the only value stored in error audit detail:

```go
func publicMCPErrorCode(err error) string {
	switch {
	case errors.Is(err, appmcp.ErrInvalidHeaderTemplate):
		return response.CodeMCPHeaderTemplateInvalid
	case errors.Is(err, appmcp.ErrInvalidHeaderTemplateMode):
		return response.CodeMCPHeaderTemplateInvalidMode
	case errors.Is(err, appmcp.ErrInvalidAuthTokenUpdate):
		return response.CodeMCPServerInvalidAuthUpdate
	case errors.Is(err, appmcp.ErrMCPServerNotFound):
		return response.CodeMCPServerNotFound
	case errors.Is(err, appmcp.ErrUnsafeMCPServerTarget):
		return response.CodeMCPServerUnsafeTarget
	case errors.Is(err, appmcp.ErrMCPServerProbeFailed):
		return response.CodeMCPServerProbeFailed
	case errors.Is(err, appmcp.ErrMCPServerSyncFailed):
		return response.CodeMCPServerSyncFailed
	default:
		return response.CodeInternal
	}
}

func writeMCPPublicError(c *gin.Context, err error) {
	code := publicMCPErrorCode(err)
	switch code {
	case response.CodeMCPHeaderTemplateInvalid,
		response.CodeMCPHeaderTemplateInvalidMode,
		response.CodeMCPServerInvalidAuthUpdate,
		response.CodeMCPServerUnsafeTarget:
		response.ErrorWithCode(c, http.StatusBadRequest, code, "invalid mcp request")
	case response.CodeMCPServerNotFound:
		response.ErrorWithCode(c, http.StatusNotFound, code, "mcp server not found")
	case response.CodeMCPServerProbeFailed, response.CodeMCPServerSyncFailed:
		response.ErrorWithCode(c, http.StatusBadGateway, code, "mcp remote operation failed")
	default:
		if errors.Is(err, appmcp.ErrMCPClientUnavailable) {
			response.ErrorWithCode(c, http.StatusServiceUnavailable, "mcp.client_unavailable", "mcp client unavailable")
			return
		}
		response.ErrorWithCode(c, http.StatusInternalServerError, response.CodeInternal, "internal server error")
	}
}
```

Have the existing `writeServiceError` call `writeMCPPublicError` for these sentinels before its pre-existing validation/default branches. Reuse `ErrInvalidAuthTokenUpdate`/`ErrMCPServerNotFound` from Task 1 and the five template/target/probe/sync sentinels defined in Task 5; do not redeclare them. Wrap underlying parser/SSRF/client errors by stable sentinel only—never append a remote message or URL.

- [ ] **Step 4: Write handler tests**

Keep the preview/probe/audit tests from Step 1 and add this executable status/envelope table:

```go
func TestWriteMCPPublicErrorUsesStableEnvelopeWithoutCauseText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "remote-secret-must-not-leak"
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid template", err: appmcp.ErrInvalidHeaderTemplate, status: 400, code: response.CodeMCPHeaderTemplateInvalid},
		{name: "invalid mode", err: appmcp.ErrInvalidHeaderTemplateMode, status: 400, code: response.CodeMCPHeaderTemplateInvalidMode},
		{name: "invalid auth update", err: appmcp.ErrInvalidAuthTokenUpdate, status: 400, code: response.CodeMCPServerInvalidAuthUpdate},
		{name: "not found", err: appmcp.ErrMCPServerNotFound, status: 404, code: response.CodeMCPServerNotFound},
		{name: "unsafe target", err: appmcp.ErrUnsafeMCPServerTarget, status: 400, code: response.CodeMCPServerUnsafeTarget},
		{name: "probe", err: fmt.Errorf("%w: %s", appmcp.ErrMCPServerProbeFailed, secret), status: 502, code: response.CodeMCPServerProbeFailed},
		{name: "sync", err: fmt.Errorf("%w: %s", appmcp.ErrMCPServerSyncFailed, secret), status: 502, code: response.CodeMCPServerSyncFailed},
		{name: "client unavailable", err: appmcp.ErrMCPClientUnavailable, status: 503, code: "mcp.client_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set(middleware.ContextKeyRequestID, "request-error")
			writeMCPPublicError(ctx, tt.err)
			if recorder.Code != tt.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var envelope response.Envelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.ErrorCode != tt.code || envelope.RequestID != "request-error" || envelope.Data != nil {
				t.Fatalf("envelope = %#v", envelope)
			}
			if strings.Contains(recorder.Body.String(), secret) {
				t.Fatalf("remote cause leaked: %s", recorder.Body.String())
			}
		})
	}
}
```

Retain Task 1's complete create/update/delete/status regression cases in this same `handler_test.go`. For create/update/delete/status/reorder/sync handlers, use the exact `AuditInput` construction shown for `ProbeServer`; assert action, actor, request, IP, user agent, resource ID, outcome, changed field names/counts, and stable error code. Serialize every captured detail and assert fixtures containing a template value, bearer, email, URL query secret, and remote error text have zero matches.

- [ ] **Step 5: Run handler tests**

```powershell
cd backend
go test ./internal/transport/http/mcp -count=1
```

Expected: pass.

- [ ] **Step 6: Annotate and regenerate Swagger**

Annotate all existing/new MCP handlers with Bearer security and exact contracts.

```powershell
cd backend
make swagger
rg -n "/admin/mcp/servers|/admin/mcp/header-templates/preview|/mcp/tools" docs/swagger.yaml
```

Expected: generator exits 0 and all routes are found.

- [ ] **Step 7: Commit**

```powershell
git add backend/internal/transport/http/mcp backend/internal/shared/response/error_code.go backend/docs
git commit -m "feat: document mcp header control plane"
```

### Task 8: Build the Header Template Editor and Probe Flow

**Files:**
- Modify: `frontend/features/admin/api/mcp.types.ts`
- Modify: `frontend/features/admin/api/mcp.ts`
- Modify: `frontend/shared/api/http-client.ts`
- Create: `frontend/features/admin/model/mcp-header-template.ts`
- Create: `frontend/features/admin/model/mcp-header-template.test.mjs`
- Create: `frontend/features/admin/components/sections/tools/mcp-header-template-editor.tsx`
- Create: `frontend/features/admin/components/sections/tools/mcp-server-dialog.tsx`
- Modify: `frontend/features/admin/components/sections/tools/admin-tools.tsx`
- Modify: `frontend/i18n/messages/en-US/admin-tools.json`
- Modify: `frontend/i18n/messages/zh-CN/admin-tools.json`
- Modify: `frontend/i18n/messages/en-US/errors.json`
- Modify: `frontend/i18n/messages/zh-CN/errors.json`

**Interfaces:**
- Consumes: Task 7 preview/probe and Task 2 partial APIs.
- Produces: strict editor, token help, warnings, preview, save-probe-sync, and no chat changes.

- [ ] **Step 1: Add typed preview/probe clients**

Add these contracts to `mcp.types.ts`:

```ts
export type MCPHeaderTemplateMode = "chat" | "probe" | "sync";

export type MCPHeaderTemplateWarningDTO = {
  code: "unknown_token" | "malformed_token";
  headerName?: string;
  token?: string;
};

export type MCPHeaderPreviewItemDTO = {
  name: string;
  value: string;
  sensitive: boolean;
};

export type AdminMCPHeaderTemplatePreviewDTO = {
  mode: MCPHeaderTemplateMode;
  supportedTokens: string[];
  warnings: MCPHeaderTemplateWarningDTO[];
  headers: MCPHeaderPreviewItemDTO[];
};

export type AdminMCPServerProbeDTO = {
  toolCount: number;
  warnings: MCPHeaderTemplateWarningDTO[];
};
```

Add these functions and their type imports to `mcp.ts`:

```ts
export async function previewAdminMCPHeaderTemplate(
  accessToken: string,
  headersJSON: string,
  mode: MCPHeaderTemplateMode,
  signal?: AbortSignal,
): Promise<AdminMCPHeaderTemplatePreviewDTO> {
  return authedRequest<AdminMCPHeaderTemplatePreviewDTO>(
    "/api/v1/admin/mcp/header-templates/preview",
    {
      method: "POST",
      accessToken,
      body: { headersJSON, mode },
      signal,
    },
    true,
  );
}

export async function probeAdminMCPServer(
  accessToken: string,
  serverID: number,
): Promise<AdminMCPServerProbeDTO> {
  return authedRequest<AdminMCPServerProbeDTO>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}/probe`,
    { method: "POST", accessToken },
    true,
  );
}
```

Add `signal?: AbortSignal` to the existing exported `ApiRequestOptions` in `frontend/shared/api/http-client.ts` and set `signal: options.signal` on its existing `fetch` `RequestInit`. `AuthedRequestOptions` already derives from that type, so no parallel client or cast is needed.

- [ ] **Step 2: Write failing local preflight model tests**

Create `frontend/features/admin/model/mcp-header-template.test.mjs`:

```js
import assert from "node:assert/strict";
import test from "node:test";

import { validateHeaderTemplateJSON } from "./mcp-header-template.ts";

test("MCP header template rejects invalid JSON and non-object roots", () => {
  assert.equal(validateHeaderTemplateJSON("{"), "invalidJson");
  assert.equal(validateHeaderTemplateJSON("null"), "objectRequired");
  assert.equal(validateHeaderTemplateJSON("[]"), "objectRequired");
});

test("MCP header template requires string values", () => {
  for (const raw of [
    '{"X-A":null}',
    '{"X-A":1}',
    '{"X-A":true}',
    '{"X-A":[]}',
    '{"X-A":{}}',
  ]) {
    assert.equal(validateHeaderTemplateJSON(raw), "stringValuesRequired");
  }
});

test("MCP header template accepts valid object shape", () => {
  assert.equal(validateHeaderTemplateJSON("{}"), null);
  assert.equal(validateHeaderTemplateJSON('{"X-A":"value"}'), null);
});

test("MCP header template leaves duplicate detection to the backend", () => {
  assert.equal(validateHeaderTemplateJSON('{"X-A":"1","X-A":"2"}'), null);
});
```

Run:

```powershell
cd frontend
pnpm test --test-name-pattern="MCP header template"
```

Expected: FAIL because the model helper does not exist.

- [ ] **Step 3: Implement local JSON shape preflight**

Create `frontend/features/admin/model/mcp-header-template.ts` exactly as follows:

```ts
export type MCPHeaderTemplatePreflightError =
  | "invalidJson"
  | "objectRequired"
  | "stringValuesRequired";

export function validateHeaderTemplateJSON(
  raw: string,
): MCPHeaderTemplatePreflightError | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return "invalidJson";
  }
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") {
    return "objectRequired";
  }
  if (Object.values(parsed).some((value) => typeof value !== "string")) {
    return "stringValuesRequired";
  }
  return null;
}
```

Use `JsonCodeEditor` and block preview/save on this local shape error. `JSON.parse` cannot detect exact duplicate keys, so do not call this check authoritative.

Run: `cd frontend && pnpm test --test-name-pattern="MCP header template"`

Expected: PASS; all four preflight tests complete and duplicate JSON remains explicitly backend-authoritative.

- [ ] **Step 4: Render server-authoritative preview**

In `mcp-header-template-editor.tsx`, use the exported preflight and this exact request-state effect. The component renders `JsonCodeEditor`, `preview?.supportedTokens`, `preview?.warnings`, and `preview?.headers`; it never renders locally substituted values:

```tsx
const [preview, setPreview] = React.useState<AdminMCPHeaderTemplatePreviewDTO | null>(null);
const [previewedText, setPreviewedText] = React.useState("");
const [previewError, setPreviewError] = React.useState<unknown>(null);
const preflightError = validateHeaderTemplateJSON(value);

React.useEffect(() => {
  setPreview(null);
  setPreviewedText("");
  setPreviewError(null);
  if (preflightError) return;

  const controller = new AbortController();
  const exactText = value;
  const timer = window.setTimeout(() => {
    void previewAdminMCPHeaderTemplate(accessToken, exactText, mode, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) {
          setPreview(result);
          setPreviewedText(exactText);
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) setPreviewError(error);
      });
  }, 300);
  return () => {
    window.clearTimeout(timer);
    controller.abort();
  };
}, [accessToken, mode, preflightError, value]);

const previewIsCurrent =
  preflightError === null && preview !== null && previewedText === value && previewError === null;
```

Expose `previewIsCurrent` through `onValidityChange`; the dialog disables Save when false. The backend parser remains authoritative for duplicates, canonical conflicts, blacklist, syntax, and limits.

- [ ] **Step 5: Extract `MCPServerDialog`**

Use this exact ownership boundary; keep list refresh and toast in the parent:

```tsx
type MCPServerDialogProps = {
  accessToken: string;
  open: boolean;
  original: AdminMCPServerDTO | null;
  onOpenChange: (open: boolean) => void;
  onCreated: (payload: AdminMCPServerCreatePayload) => Promise<AdminMCPServerDTO>;
  onUpdated: (
    serverID: number,
    payload: AdminMCPServerUpdatePayload,
  ) => Promise<AdminMCPServerDTO>;
  onProbe: (serverID: number) => Promise<AdminMCPServerProbeDTO>;
  onSync: (serverID: number) => Promise<void>;
  onSaved: (server: AdminMCPServerDTO) => Promise<void>;
};
```

The dialog owns `ServerFormState`, calls `toServerUpdatePayload` for edits, and passes `headersJSON` plus mode to `MCPHeaderTemplateEditor`. It never copies `authToken` from the original DTO and initializes that field to `""`; `authTokenConfigured` controls help/removal only.

- [ ] **Step 6: Save, probe, then sync**

Implement the submit branch with this order:

```tsx
const updatePayload = original
  ? toServerUpdatePayload(form, original)
  : null;
const saved = original
  ? await onUpdated(original.id, updatePayload ?? {})
  : await onCreated({
      name: form.name.trim(),
      baseURL: form.baseURL.trim(),
      ...(form.authToken.trim() ? { authToken: form.authToken.trim() } : {}),
      headersJSON: form.headersJSON.trim() || "{}",
      status: form.status,
    });

const connectionChanged =
  original === null ||
  updatePayload?.baseURL !== undefined ||
  updatePayload?.authToken !== undefined ||
  updatePayload?.clearAuthToken === true ||
  updatePayload?.headersJSON !== undefined;

try {
  if (connectionChanged) {
    await onProbe(saved.id);
    await onSync(saved.id);
  }
} catch (error) {
  await onSaved(saved); // refresh persisted state even though probe failed
  throw error; // existing safe ApiError toast path; sync was not reached
}
await onSaved(saved);
onOpenChange(false);
```

Disable submit unless create fields are valid or the edit payload is non-empty, and unless `previewIsCurrent` is true for the exact current Header text. Probe failure keeps the Server saved, prevents sync, refreshes the list, and leaves the dialog open with the safe API error.

- [ ] **Step 7: Add English/Chinese copy**

Cover ten tokens, server expansion, modes, warnings, blacklist, plaintext storage warning, masked preservation, bearer removal, and probe outcomes. Add `errors.json` keys matching Task 7 after underscore-to-camel conversion: `mcp.headerTemplate.invalid`, `mcp.headerTemplate.invalidMode`, `mcp.server.invalidAuthTokenUpdate`, `mcp.server.notFound`, `mcp.server.unsafeTarget`, `mcp.server.probeFailed`, and `mcp.server.syncFailed`.

- [ ] **Step 8: Run verification**

```powershell
cd frontend
pnpm test
pnpm lint
pnpm build
```

Expected: all three commands exit 0.

- [ ] **Step 9: Commit**

```powershell
git add frontend/features/admin/api frontend/features/admin/model frontend/features/admin/components/sections/tools frontend/shared/api/http-client.ts frontend/i18n/messages
git commit -m "feat: add mcp header template editor"
```

### Task 9: Run Cross-Stack Security and Compatibility Acceptance

**Files:**
- Modify only files already listed in Tasks 1-8 if a documented assertion fails.

**Interfaces:**
- Consumes: every A+B deliverable.
- Produces: fresh release-gate evidence without starting C or D.

- [ ] **Step 1: Run focused race suites**

```powershell
cd backend
go test -race ./internal/shared/security ./internal/infra/mcp ./internal/application/mcp ./internal/application/conversation ./internal/transport/http/mcp ./internal/transport/http/middleware -count=1
```

Expected: pass with no race report.

- [ ] **Step 2: Run full backend gate**

```powershell
cd backend
make test
go vet ./...
make swagger
make build
```

Expected: every command exits 0.

- [ ] **Step 3: Run full frontend gate**

```powershell
cd frontend
pnpm test
pnpm lint
pnpm build
```

Expected: all three commands exit 0.

- [ ] **Step 4: Verify persisted-secret matrix**

Status-only and name-only updates preserve; redacted-sensitive plus changed non-sensitive preserves; omitted key deletes; real new value replaces; create sentinel rejects.

- [ ] **Step 5: Verify outbound lifecycle matrix**

Capture initialize, initialized, list/call, DELETE. Confirm identical rendered values, correct post-initialize protocol/session, no redirect, and no captured value in error/trace/audit/event/`last_error`.

- [ ] **Step 6: Confirm phase boundary**

```powershell
rg -n "ContextJWT|SessionManager" backend frontend
```

Expected: no phase C model/API/UI and no phase D manager. `X-DEEIX-Context` appears only in blacklist/tests.

- [ ] **Step 7: Commit only corrections**

If acceptance required corrections:

```powershell
git add backend frontend
git commit -m "fix: close mcp header acceptance gaps"
```

If no correction was required, do not create an empty commit.
