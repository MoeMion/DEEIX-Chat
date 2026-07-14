# Identity Provider TLS Verification Override Design

## Context

DEEIX Chat must normally validate HTTPS certificates for every outbound authentication request. Some privately operated OAuth2 or OIDC providers may temporarily use an expired or privately signed certificate, which prevents the backend from completing discovery, token exchange, or user-info retrieval.

The override must be scoped to one configured identity provider. It must not weaken certificate verification for other identity providers, remote provider logos, Turnstile, or any other outbound integration.

## Goals

- Add an administrator-controlled `tlsInsecureSkipVerify` option to each OAuth2/OIDC identity provider.
- Keep the option disabled by default for existing and newly created providers.
- Apply the option immediately to backend protocol requests for that provider without restarting the service.
- Preserve the existing outbound timeout, SSRF protection, redirect policy, and tracing behavior.
- Make the risk visible in the administrator UI and cover the security boundary with automated tests.

## Non-goals

- The option does not bypass certificate checks performed by a user's browser or Electron client when opening the OAuth authorization URL.
- The option does not affect remote provider-logo fetching.
- The option does not affect Turnstile or any non-provider outbound request.
- The change does not disable SSRF protections or relax provider URL validation.
- The change does not introduce a global authentication-module or deployment-wide TLS override.

## Data Model and API Contract

Add `TLSInsecureSkipVerify bool` to the identity-provider domain type and `tls_insecure_skip_verify` to the `identity_providers` persistence model. The database column is non-null with a default of `false`, so existing rows retain secure behavior. The normal schema migration path must add and validate the column on both PostgreSQL and SQLite-compatible test databases.

The create/update management request accepts:

```json
{
  "tlsInsecureSkipVerify": false
}
```

The management response returns the same Boolean so editing or using quick provider controls cannot accidentally reset it. The public login-options response must omit this administrative security setting. Swagger source annotations and committed generated Swagger artifacts must stay synchronized with the management contract.

## Backend HTTP Client Design

The authentication service constructs two immutable outbound HTTP clients at startup:

1. A secure client using the current transport.
2. A provider-only insecure client whose cloned TLS configuration sets `InsecureSkipVerify` to `true`.

Both clients retain the existing request timeout, SSRF-aware dialer, redirect behavior, and tracing wrapper. No request mutates a shared `http.Transport` or `tls.Config`; this prevents concurrent requests from leaking the insecure setting into Turnstile, logos, or another provider.

A small selector chooses the client from the loaded provider record for each provider protocol request. The insecure client is used only when that record has `TLSInsecureSkipVerify == true`.

The selector applies to:

- OIDC discovery;
- OAuth/OIDC token exchange;
- user-info retrieval;
- provider-specific supplemental profile requests such as GitHub email retrieval;
- future JWKS retrieval when that network path is implemented.

Remote provider-logo retrieval and Turnstile verification always use the secure client, regardless of provider configuration.

## Request Flow

1. An administrator creates or updates an identity provider through the existing protected management API.
2. The Boolean is validated, persisted, audited through the existing provider-management path, and returned only in the management representation.
3. A login or account-binding flow loads the current provider record from the repository.
4. Each backend provider protocol call selects the secure or provider-only insecure client from that record.
5. Changing the provider option affects the next flow because provider configuration is loaded from persistence; no process restart or mutable global client is required.

## Administrator UI

Add a switch at the top of the identity-provider dialog's Advanced Settings section for both OAuth2 and OIDC providers.

- Label: `忽略 TLS 证书校验` / `Skip TLS certificate verification`
- Default: off
- Description: use only for a trusted identity provider whose certificate cannot be fixed immediately.
- Enabled warning: the server will not verify this identity provider's HTTPS certificate, which permits man-in-the-middle attacks; enable it only temporarily and repair the certificate as soon as possible.

The provider form model must preserve the value when opening an existing provider, building create/update payloads, and using existing quick login/registration controls.

## Security and Error Handling

Skipping verification exposes OAuth client credentials, authorization codes, access tokens, and user profile data to interception. Secure verification therefore remains the default and the enabled state is visually prominent.

The implementation must document the intentional `InsecureSkipVerify` use at its single construction point. It must not log secrets, tokens, provider responses, or user profile data. Existing public error-envelope behavior remains unchanged. TLS failures continue to fail the provider flow when the option is disabled.

Administrators who can already manage provider endpoints and credentials retain access to this setting through the existing provider-management authorization and audit path.

## Testing and Verification

Backend tests must be written first and demonstrate:

- default and explicit `false` configurations reject a self-signed TLS provider;
- `true` permits the same provider protocol request;
- the selection applies to discovery, token, user-info, and supplemental profile requests;
- remote logos and Turnstile still reject invalid certificates;
- SSRF protection remains active for the insecure client;
- create, update, and read persistence round trips preserve the Boolean;
- schema migration gives existing rows a secure `false` default;
- management DTOs expose the value while public login options omit it.

Frontend model tests must demonstrate:

- a new provider defaults to `false`;
- editing a provider preserves `true`;
- create/update payload construction preserves the selected value.

Required final verification:

```bash
(cd backend && make test && go vet ./... && make build && make swagger)
(cd frontend && pnpm test && pnpm lint && pnpm build)
```

## Acceptance Criteria

- An administrator can enable or disable certificate verification bypass independently for each identity provider.
- The setting takes effect for the next backend provider protocol request without a restart.
- A provider with the option enabled can complete backend calls against a self-signed or otherwise untrusted certificate.
- Every other identity provider, remote logo, Turnstile request, and outbound subsystem continues to validate certificates normally.
- Existing installations remain secure after migration because all existing provider rows default to `false`.
