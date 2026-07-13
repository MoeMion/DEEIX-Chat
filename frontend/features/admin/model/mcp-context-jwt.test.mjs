import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  canActivatePreparedContext,
  normalizeMCPContextJWTPolicy,
} from "./mcp-context-jwt.ts";

function policy(expiresSeconds, overrides = {}) {
  return {
    expiresSeconds,
    includeName: false,
    includeEmail: false,
    includeRole: false,
    ...overrides,
  };
}

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

test("MCP signed context accepts TTL boundaries and trims whitespace", () => {
  assert.deepEqual(normalizeMCPContextJWTPolicy(policy(" 60 ")), {
    ok: true,
    payload: {
      expiresSeconds: 60,
      includeName: false,
      includeEmail: false,
      includeRole: false,
    },
  });
  assert.deepEqual(
    normalizeMCPContextJWTPolicy(
      policy("\t900\n", {
        includeName: true,
        includeEmail: true,
        includeRole: true,
      }),
    ),
    {
      ok: true,
      payload: {
        expiresSeconds: 900,
        includeName: true,
        includeEmail: true,
        includeRole: true,
      },
    },
  );
});

test("MCP signed context rejects invalid TTL text and range", () => {
  assert.deepEqual(normalizeMCPContextJWTPolicy(policy("")), {
    ok: false,
    error: "expiresSecondsRequired",
  });
  assert.equal(
    normalizeMCPContextJWTPolicy(policy("60.5")).error,
    "expiresSecondsInteger",
  );
  for (const expiresSeconds of ["59", "901"]) {
    assert.equal(
      normalizeMCPContextJWTPolicy(policy(expiresSeconds)).error,
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
    templateToken: "{{DEEIX_SIGNED_CONTEXT}}",
    recommendedHeader: "X-MCP-CLIENT-SIGNED-CONTEXT",
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

test("MCP signed context rejects incomplete or stale pending rotation state", () => {
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
    templateToken: "{{DEEIX_SIGNED_CONTEXT}}",
    recommendedHeader: "X-MCP-CLIENT-SIGNED-CONTEXT",
    algorithm: "HS256",
    secret: "one-time-secret",
    issuer: status.issuer,
    audience: status.audience,
    keyID: "ctx_pending",
    expiresSeconds: 300,
  };

  for (const candidate of [
    { ...status, pendingKeyID: undefined },
    { ...status, pendingExpiresAt: undefined },
    { ...status, pendingExpiresAt: "not-a-timestamp" },
    { ...status, pendingExpiresAt: "2026-07-10T08:00:00Z" },
    { ...status, pendingKeyID: "" },
    { ...status, pendingKeyID: "ctx_other" },
  ]) {
    assert.equal(canActivatePreparedContext(candidate, prepared, nowMS), false);
  }

  assert.equal(
    canActivatePreparedContext(status, { ...prepared, keyID: "" }, nowMS),
    false,
  );
  assert.equal(
    canActivatePreparedContext(status, { ...prepared, keyID: "ctx_other" }, nowMS),
    false,
  );
});

test("MCP signed context prepare contract exposes template binding guidance", () => {
  const typesSource = readFileSync(
    new URL("../api/mcp.types.ts", import.meta.url),
    "utf8",
  );
  const prepareResult = typesSource.match(
    /export type MCPContextJWTPrepareResult = \{[\s\S]*?\n\};/,
  )?.[0];

  assert.match(
    prepareResult ?? "",
    /templateToken: "\{\{DEEIX_SIGNED_CONTEXT\}\}";/,
  );
  assert.match(
    prepareResult ?? "",
    /recommendedHeader: "X-MCP-CLIENT-SIGNED-CONTEXT";/,
  );
  assert.doesNotMatch(prepareResult ?? "", /\bheader:/);
  assert.doesNotMatch(
    prepareResult ?? "",
    new RegExp(["X-DEEIX-", "Context"].join("")),
  );
});
