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
