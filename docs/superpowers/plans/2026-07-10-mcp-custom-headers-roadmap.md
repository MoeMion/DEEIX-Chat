# MCP Custom Headers Roadmap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver DEEIX-native MCP custom Header templates safely in phases A+B, then add per-server signed context in phase C and lifecycle/protocol optimization in phase D.

**Architecture:** The roadmap is split into three independently reviewable and deployable implementation plans. Phase A+B establishes the immutable DEEIX template context and transport ownership rules; phase C consumes that contract to sign context without changing template semantics; phase D replaces per-operation sessions with run-scoped sessions without changing either control-plane API.

**Tech Stack:** Go 1.26, Gin, Gorm, PostgreSQL/SQLite, `net/http`, OpenTelemetry, `github.com/golang-jwt/jwt/v5`, Next.js 16, React 19, TypeScript, next-intl, pnpm.

## Global Constraints

- Implement phases in this exact order: A+B, then C, then D.
- Do not expose an Open WebUI compatibility mode or aliases for Open WebUI token names.
- The only template tokens are `{{DEEIX_USER_PUBLIC_ID}}`, `{{DEEIX_USER_DISPLAY_NAME}}`, `{{DEEIX_USER_EMAIL}}`, `{{DEEIX_USER_ROLE}}`, `{{DEEIX_CONVERSATION_PUBLIC_ID}}`, `{{DEEIX_ASSISTANT_MESSAGE_PUBLIC_ID}}`, `{{DEEIX_USER_MESSAGE_PUBLIC_ID}}`, `{{DEEIX_REQUEST_ID}}`, `{{DEEIX_RUN_ID}}`, and `{{DEEIX_TRACE_ID}}`.
- Template replacement applies to Header values only, is case-sensitive, replaces a known missing value with the empty string, and leaves an unknown token unchanged while returning a warning.
- Treat any balanced non-nested `{{...}}` candidate of 1 through 128 non-brace/non-newline characters as a token candidate; unknown candidates are preserved with `unknown_token`, and unmatched delimiters are preserved with `malformed_token`. No expression syntax is evaluated.
- Do not add a compatibility or strict-evaluation mode in this delivery: the preserve-plus-warning behavior above is the single DEEIX template contract through phases A+B, C, and D.
- Parse template JSON as `map[string]string`; do not coerce numbers, booleans, arrays, objects, or null into strings.
- Govern custom Header names with a case-insensitive blacklist, not an allowlist.
- The exact-name blacklist is `Accept`, `Accept-Encoding`, `Authorization`, `Baggage`, `Connection`, `Content-Length`, `Content-Type`, `Cookie`, `Host`, `Keep-Alive`, `Last-Event-ID`, `MCP-Protocol-Version`, `MCP-Session-Id`, `Origin`, `Proxy-Authenticate`, `Proxy-Authorization`, `Proxy-Connection`, `Set-Cookie`, `TE`, `Traceparent`, `Tracestate`, `Trailer`, `Transfer-Encoding`, `Upgrade`, `User-Agent`, and `X-DEEIX-Context`.
- The case-insensitive prefix blacklist is `MCP-`, `Proxy-`, `Sec-`, and `X-DEEIX-`.
- Validate Header field names and values with `golang.org/x/net/http/httpguts`; inspect the original value and reject CR, LF, and invalid control characters before any legacy `TrimSpace` normalization, then repeat validation before transport use.
- Enforce at most 32 custom Headers, 128 bytes per name, 4096 bytes per template or rendered value, 32768 bytes of raw JSON, and 16384 bytes across rendered names plus values.
- Browser/API callers never supply final user, conversation, message, role, or email values. Existing run/request/trace inputs may be caller-proposed correlation/idempotency candidates, but DEEIX middleware/application code validates, normalizes, bounds, scopes, and authoritatively associates them before they enter `TemplateContext`; downstream Servers must never treat correlation claims as standalone authorization.
- MCP Base URLs contain only scheme, host/port, and path; userinfo, query, fragment, and redirects are rejected before any sensitive Header is attached.
- `headers_json` remains the existing plaintext JSON column in phase A+B for rollback compatibility; the UI and documentation must state that it is not a credential store.
- Never log, trace, audit, persist in `last_error`, or return in normal control-plane responses a rendered Header value, bearer token, signed-context JWT, signing secret, or user email.
- Preserve the standard API envelope `errorMsg`, `errorCode?`, `details?`, `requestId?`, and `data`; pagination remains `data.total` plus `data.results` where applicable.
- Commit subjects must match `type: subject` and use only the repository-approved English characters.

---

## Plan Set

1. [Phase A+B: Protocol Hardening and DEEIX Header Templates](./2026-07-10-mcp-header-templates-ab.md)
2. [Phase C: Per-Server Signed Context](./2026-07-10-mcp-signed-context-c.md)
3. [Phase D: Run-Scoped MCP Lifecycle and Protocol Upgrade](./2026-07-10-mcp-lifecycle-d.md)

## Cross-Phase Contract

The phase A+B plan owns these types and semantics. Later plans may add fields or collaborators but must not rename them or reinterpret their values:

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

Phase C appends `SignedContext *SignedContextConfig` to `CallConfig` and adds `ContextSigner.Sign(TemplateContext, SignedContextConfig)`. Phase D may invoke that same signer again on each physical HTTP request, but it must retain the same `TemplateContext` and `SignedContextConfig` for the lifetime of a run-scoped session.

## Release Gates

### Gate A+B

- [ ] Complete every task in `2026-07-10-mcp-header-templates-ab.md`.
- [ ] Confirm every previously valid static Header sends the same non-blacklisted value after the existing value `TrimSpace` normalization; newly rejected empty/whitespace names and canonical duplicates are documented as safety hardening.
- [ ] Confirm name edits, status updates, and masked JSON round trips cannot replace a stored sensitive value with `********`.
- [ ] Confirm initialize, initialized, list/call, and DELETE receive the same rendered custom Header values.
- [ ] Confirm chat, probe, and sync use the documented context modes and never accept authoritative values from the browser.
- [ ] Run the backend and frontend verification commands from the A+B plan with zero failures before enabling template help in the UI.

### Gate C

- [ ] Complete every task in `2026-07-10-mcp-signed-context-c.md` after Gate A+B passes.
- [ ] Confirm each MCP Server has an independent audience, key ID, and encrypted secret.
- [ ] Confirm prepare does not change the active signer, activate is atomic, and a secret is returned only by prepare.
- [ ] Confirm a JWT issued for Server A fails Server B audience validation even when a test intentionally reuses the same HMAC bytes.
- [ ] Run the backend and frontend verification commands from the C plan with zero failures before allowing administrators to enable signed context.

### Gate D

- [ ] Complete every task in `2026-07-10-mcp-lifecycle-d.md` after Gates A+B and C pass.
- [ ] Confirm a session cache key includes server identity, user public ID, run ID, authentication identity, and configuration version.
- [ ] Confirm `tools/call` is never automatically replayed after an ambiguous network failure.
- [ ] Confirm JSON, multi-event SSE, cursor pagination, cancellation, 404 session invalidation, and run cleanup pass under the race detector.
- [ ] Run the backend verification commands from the D plan with zero failures before advertising MCP protocol `2025-11-25`.

## Rollback Boundaries

- A+B is rollback-compatible at the database level because it does not add or reinterpret columns; roll back backend and frontend together if the PATCH contract changes.
- C adds columns and a partial unique MCP Server public ID index (`WHERE public_id <> ''`). A rollback may leave columns/index in place because an older A+B backend can still insert blank IDs; disable signed context before running that older backend, and let a later C migration backfill any rollback-created blank rows.
- D does not add database columns or frontend APIs. Rollback drops in-memory run sessions and returns to per-operation sessions; no persisted conversation or MCP configuration data changes.
