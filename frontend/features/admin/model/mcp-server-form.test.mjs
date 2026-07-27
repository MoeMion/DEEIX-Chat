import assert from "node:assert/strict";
import test from "node:test";

import * as serverFormModel from "./mcp-server-form.ts";

const {
  EMPTY_SERVER_FORM,
  canSubmitServerForm,
  hasMCPConnectionChanges,
  serverFormFromDTO,
  toServerCreatePayload,
  toServerUpdatePayload,
} = serverFormModel;

const original = {
  id: 7,
  name: "Memory",
  baseURL: "https://mcp.example.test/mcp",
  authTokenConfigured: true,
  headersEnabled: true,
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
    headersEnabled: original.headersEnabled,
    headersJSON: original.headersJSON,
    status: original.status,
    ...overrides,
  };
}

test("MCP server empty form enables Headers with an empty template", () => {
  assert.deepEqual(EMPTY_SERVER_FORM, {
    name: "",
    baseURL: "",
    authToken: "",
    clearAuthToken: false,
    headersEnabled: true,
    headersJSON: "{}",
    status: "active",
  });
});

test("MCP server form restores a persisted disabled Header switch", () => {
  assert.deepEqual(serverFormFromDTO({ ...original, headersEnabled: false }), {
    name: original.name,
    baseURL: original.baseURL,
    authToken: "",
    clearAuthToken: false,
    headersEnabled: false,
    headersJSON: original.headersJSON,
    status: "active",
  });
});

test("MCP server create payload always sends the Header switch explicitly", () => {
  assert.deepEqual(
    toServerCreatePayload(form({
      name: "  New server  ",
      baseURL: " https://new.example.test/mcp ",
      authToken: " bearer ",
    })),
    {
      name: "New server",
      baseURL: "https://new.example.test/mcp",
      authToken: "bearer",
      headersEnabled: true,
      headersJSON: original.headersJSON,
      status: "active",
    },
  );
  assert.equal(toServerCreatePayload(form({ headersEnabled: false })).headersEnabled, false);
});

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
  assert.deepEqual(toServerUpdatePayload(form({ headersEnabled: false }), original), {
    headersEnabled: false,
  });
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

test("MCP server form gates preview only while custom Headers are enabled", () => {
  const createContext = {
    accessTokenPresent: true,
    contextPending: false,
    previewIsCurrent: false,
    original: null,
  };

  assert.equal(canSubmitServerForm(form(), createContext), false);
  assert.equal(
    canSubmitServerForm(form({ headersEnabled: false }), createContext),
    true,
  );
  assert.equal(
    canSubmitServerForm(form(), { ...createContext, previewIsCurrent: true }),
    true,
  );
});

test("MCP server form preserves access context and edit-change submit gates", () => {
  const editContext = {
    accessTokenPresent: true,
    contextPending: false,
    previewIsCurrent: true,
    original,
  };

  assert.equal(canSubmitServerForm(form(), editContext), false);
  assert.equal(canSubmitServerForm(form({ status: "inactive" }), editContext), true);
  assert.equal(
    canSubmitServerForm(form({ status: "inactive" }), {
      ...editContext,
      accessTokenPresent: false,
    }),
    false,
  );
  assert.equal(
    canSubmitServerForm(form({ status: "inactive" }), {
      ...editContext,
      contextPending: true,
    }),
    false,
  );
});

test("MCP server Header toggle is an MCP connection change even when set false", () => {
  assert.equal(hasMCPConnectionChanges(null, null), true);
  assert.equal(hasMCPConnectionChanges(original, null), false);
  assert.equal(hasMCPConnectionChanges(original, {}), false);
  assert.equal(hasMCPConnectionChanges(original, { status: "inactive" }), false);
  assert.equal(hasMCPConnectionChanges(original, { headersEnabled: false }), true);
});
