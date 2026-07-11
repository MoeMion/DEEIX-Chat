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
