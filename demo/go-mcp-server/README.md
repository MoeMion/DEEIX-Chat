# DEEIX MCP identity demo server

This demo runs a loopback-only MCP Streamable HTTP server that exposes one tool, `identity_check`, for proving DEEIX identity Header handling. It is for local integration and security testing.

The static Bearer token in this demo is not a complete MCP OAuth 2.1 resource-server implementation.

## Endpoints and protocol

- `GET /healthz` returns `ok`.
- `/mcp` accepts MCP Streamable HTTP requests.
- Clients must use MCP protocol version `2025-11-25`.
- Supported HTTP methods on `/mcp` are `POST`, `GET`, and `DELETE`.
- The server binds only to `localhost` or an explicit loopback IP address. If you run it inside Docker or another network namespace, callers outside that namespace may not be able to reach the loopback listener.

## Configuration

Set environment variables before running the server:

| Variable | Required | Description |
| --- | --- | --- |
| `MCP_DEMO_ADDR` | No | Listen address. Defaults to `127.0.0.1:8090`. Must be `localhost` or an explicit loopback IP with a valid port. |
| `MCP_DEMO_BEARER_TOKEN` | Yes | Static Bearer token for demo access. Must contain at least 32 bytes. |
| `MCP_DEMO_SIGNED_CONTEXT_HEADER` | No | Header carrying the signed context JWT. Defaults to `X-MCP-CLIENT-SIGNED-CONTEXT`. Must not collide with reserved or plain identity Headers. |
| `MCP_DEMO_CONTEXT_SECRET` | With signed verification | Canonical unpadded raw-base64url encoding of exactly 32 bytes. The server validates the decoded length, then uses the canonical secret string bytes as the HMAC key. |
| `MCP_DEMO_CONTEXT_ISSUER` | With signed verification | Expected signed context JWT issuer. |
| `MCP_DEMO_CONTEXT_AUDIENCE` | With signed verification | Expected signed context JWT audience. |
| `MCP_DEMO_CONTEXT_KEY_ID` | With signed verification | Expected signed context JWT `kid`. |

The four `MCP_DEMO_CONTEXT_*` JWT variables are all-or-none. If none are set, signed context verification is unconfigured and `identity_check` reports that diagnostic state. If any one is set, all four must be set.

## Plain identity Headers

The demo accepts these diagnostic identity Headers:

- `X-MCP-CLIENT-USER-PUBLIC-ID`
- `X-MCP-CLIENT-USER-DISPLAY-NAME`
- `X-MCP-CLIENT-USER-EMAIL`
- `X-MCP-CLIENT-USER-ROLE`
- `X-MCP-CLIENT-CONVERSATION-PUBLIC-ID`
- `X-MCP-CLIENT-ASSISTANT-MESSAGE-PUBLIC-ID`
- `X-MCP-CLIENT-USER-MESSAGE-PUBLIC-ID`
- `X-MCP-CLIENT-REQUEST-ID`
- `X-MCP-CLIENT-RUN-ID`
- `X-MCP-CLIENT-TRACE-ID`

Plain Headers are diagnostic only. They do not make `verification.verified` true by themselves. Unknown `X-MCP-CLIENT-*` Headers are ignored.

## Signed context behavior

When signed context verification is configured, the configured signed context Header must contain an HS256 JWT with `typ: JWT`, the expected `kid`, issuer, audience, subject, mode, `jti`, `iat`, `nbf`, and `exp`. The token lifetime must be at least 1 minute and at most 15 minutes, and `iat` must equal `nbf`.

Valid signed identity fields are compared with matching plain Headers. A mismatch is reported as a diagnostic result rather than an authorization bypass. Missing, duplicate, oversized, invalid, or mismatched signed context values also remain diagnostic results from `identity_check`.

## Tool

`identity_check` returns:

- `authenticated`
- `checkedAt`
- `verification`
- `identity`
- optional `signedIdentity`
- `mismatches`

The tool text content is a fixed safe string; identity values appear only in the structured tool result fields intended for identity diagnostics.

## Local run example

PowerShell:

```powershell
$env:MCP_DEMO_ADDR = "127.0.0.1:8090"
$env:MCP_DEMO_BEARER_TOKEN = "local-demo-token-0123456789abcdef"
go run ./cmd/server
```

Health check:

```powershell
Invoke-WebRequest http://127.0.0.1:8090/healthz
```

MCP clients must send:

```text
Authorization: Bearer local-demo-token-0123456789abcdef
Mcp-Protocol-Version: 2025-11-25
```
