import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import * as headerTemplate from "./mcp-header-template.ts";

const EXPECTED_DEFAULT_TEMPLATE = {
  "X-MCP-CLIENT-USER-PUBLIC-ID": "{{DEEIX_USER_PUBLIC_ID}}",
  "X-MCP-CLIENT-USER-DISPLAY-NAME": "{{DEEIX_USER_DISPLAY_NAME}}",
  "X-MCP-CLIENT-USER-EMAIL": "{{DEEIX_USER_EMAIL}}",
  "X-MCP-CLIENT-USER-ROLE": "{{DEEIX_USER_ROLE}}",
  "X-MCP-CLIENT-CONVERSATION-PUBLIC-ID":
    "{{DEEIX_CONVERSATION_PUBLIC_ID}}",
  "X-MCP-CLIENT-ASSISTANT-MESSAGE-PUBLIC-ID":
    "{{DEEIX_ASSISTANT_MESSAGE_PUBLIC_ID}}",
  "X-MCP-CLIENT-USER-MESSAGE-PUBLIC-ID":
    "{{DEEIX_USER_MESSAGE_PUBLIC_ID}}",
  "X-MCP-CLIENT-REQUEST-ID": "{{DEEIX_REQUEST_ID}}",
  "X-MCP-CLIENT-RUN-ID": "{{DEEIX_RUN_ID}}",
  "X-MCP-CLIENT-TRACE-ID": "{{DEEIX_TRACE_ID}}",
  "X-MCP-CLIENT-SIGNED-CONTEXT": "{{DEEIX_SIGNED_CONTEXT}}",
};

const { validateHeaderTemplateJSON } = headerTemplate;

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

test("MCP default header template has the exact safe ordered DEEIX contract", () => {
  assert.equal(
    headerTemplate.DEEIX_SIGNED_CONTEXT_TOKEN,
    "{{DEEIX_SIGNED_CONTEXT}}",
  );
  assert.equal(
    headerTemplate.MCP_RECOMMENDED_SIGNED_CONTEXT_HEADER,
    "X-MCP-CLIENT-SIGNED-CONTEXT",
  );
  assert.deepEqual(
    Object.entries(headerTemplate.DEFAULT_MCP_HEADER_TEMPLATE),
    Object.entries(EXPECTED_DEFAULT_TEMPLATE),
  );
  assert.equal(
    headerTemplate.DEFAULT_MCP_HEADER_TEMPLATE_JSON,
    JSON.stringify(EXPECTED_DEFAULT_TEMPLATE, null, 2),
  );
  assert.equal(
    validateHeaderTemplateJSON(
      headerTemplate.DEFAULT_MCP_HEADER_TEMPLATE_JSON,
    ),
    null,
  );
  assert.doesNotMatch(
    headerTemplate.DEFAULT_MCP_HEADER_TEMPLATE_JSON,
    new RegExp(["X-DEEIX-", "Context|Bearer|\\bJWT\\b|secret"].join(""), "i"),
  );
});

test("MCP preview request key covers JSON, mode, switch, and server identity", () => {
  const base = headerTemplate.buildMCPHeaderPreviewRequestKey({
    headersJSON: '{"X-A":"one"}',
    mode: "chat",
    headersEnabled: true,
    serverID: 17,
  });

  assert.equal(
    base,
    headerTemplate.buildMCPHeaderPreviewRequestKey({
      headersJSON: '{"X-A":"one"}',
      mode: "chat",
      headersEnabled: true,
      serverID: 17,
    }),
  );
  for (const payload of [
    {
      headersJSON: '{"X-A":"two"}',
      mode: "chat",
      headersEnabled: true,
      serverID: 17,
    },
    {
      headersJSON: '{"X-A":"one"}',
      mode: "probe",
      headersEnabled: true,
      serverID: 17,
    },
    {
      headersJSON: '{"X-A":"one"}',
      mode: "chat",
      headersEnabled: false,
      serverID: 17,
    },
    {
      headersJSON: '{"X-A":"one"}',
      mode: "chat",
      headersEnabled: true,
      serverID: 18,
    },
    {
      headersJSON: '{"X-A":"one"}',
      mode: "chat",
      headersEnabled: true,
    },
  ]) {
    assert.notEqual(
      base,
      headerTemplate.buildMCPHeaderPreviewRequestKey(payload),
    );
  }
});

test("MCP Header warnings map all five backend codes to distinct messages", () => {
  const expected = new Map([
    ["unknown_token", "headerTemplate.warnings.unknownToken"],
    ["malformed_token", "headerTemplate.warnings.malformedToken"],
    [
      "signed_context_not_referenced",
      "headerTemplate.warnings.signedContextNotReferenced",
    ],
    [
      "signed_context_not_configured",
      "headerTemplate.warnings.signedContextNotConfigured",
    ],
    [
      "signed_context_headers_disabled",
      "headerTemplate.warnings.signedContextHeadersDisabled",
    ],
  ]);

  const actual = [...expected].map(([code, messageKey]) => {
    assert.equal(headerTemplate.getMCPHeaderWarningMessageKey(code), messageKey);
    return messageKey;
  });
  assert.equal(new Set(actual).size, expected.size);
});

test("MCP Header controls stay visible and obey switch and outer disabled states", () => {
  assert.deepEqual(
    headerTemplate.getMCPHeaderTemplateControlState(false, true),
    {
      switchDisabled: false,
      editorDisabled: false,
      modeDisabled: false,
      previewDisabled: false,
      defaultTemplateDisabled: false,
    },
  );
  assert.deepEqual(
    headerTemplate.getMCPHeaderTemplateControlState(false, false),
    {
      switchDisabled: false,
      editorDisabled: true,
      modeDisabled: true,
      previewDisabled: true,
      defaultTemplateDisabled: true,
    },
  );
  assert.deepEqual(
    headerTemplate.getMCPHeaderTemplateControlState(true, true),
    {
      switchDisabled: true,
      editorDisabled: true,
      modeDisabled: true,
      previewDisabled: true,
      defaultTemplateDisabled: true,
    },
  );
});

test("MCP default template application is a disabled no-op", () => {
  const current = '{"X-CUSTOM":"draft"}';
  assert.equal(
    headerTemplate.nextMCPHeaderTemplateAfterApplyDefault(current, true),
    current,
  );
  assert.equal(
    headerTemplate.nextMCPHeaderTemplateAfterApplyDefault(current, false),
    JSON.stringify(EXPECTED_DEFAULT_TEMPLATE, null, 2),
  );
});

test("MCP preview API contract carries the full payload and signed binding", () => {
  const typesSource = readFileSync(
    new URL("../api/mcp.types.ts", import.meta.url),
    "utf8",
  );
  const generatedSource = readFileSync(
    new URL("../../../../packages/api-contract/src/types.generated.ts", import.meta.url),
    "utf8",
  );
  const apiSource = readFileSync(new URL("../api/mcp.ts", import.meta.url), "utf8");
  const previewAPI = apiSource.match(
    /export async function previewAdminMCPHeaderTemplate\([\s\S]*?\n\}/,
  )?.[0];

  assert.match(typesSource, /PreviewHeaderTemplateRequest/);
  assert.match(typesSource, /HeaderTemplatePreviewResponse/);
  assert.match(typesSource, /mode: MCPHeaderTemplateMode;/);
  assert.match(generatedSource, /export interface PreviewHeaderTemplateRequest \{[\s\S]*?headersJSON: string;/);
  assert.match(generatedSource, /export interface PreviewHeaderTemplateRequest \{[\s\S]*?headersEnabled: boolean;/);
  assert.match(generatedSource, /export interface PreviewHeaderTemplateRequest \{[\s\S]*?serverID\?: number;/);
  assert.match(generatedSource, /export interface HeaderTemplatePreviewResponse \{[\s\S]*?signedContextHeader: string;/);
  assert.match(
    previewAPI ?? "",
    /payload: AdminMCPHeaderTemplatePreviewPayload/,
  );
  assert.match(previewAPI ?? "", /body: payload/);
});
