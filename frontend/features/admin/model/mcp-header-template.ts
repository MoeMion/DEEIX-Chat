import type {
  AdminMCPHeaderTemplatePreviewPayload,
  MCPHeaderTemplateWarningCode,
} from "@/features/admin/api/mcp.types";

export const DEEIX_SIGNED_CONTEXT_TOKEN =
  "{{DEEIX_SIGNED_CONTEXT}}" as const;

export const MCP_RECOMMENDED_SIGNED_CONTEXT_HEADER =
  "X-MCP-CLIENT-SIGNED-CONTEXT" as const;

export const DEFAULT_MCP_HEADER_TEMPLATE: Readonly<Record<string, string>> =
  Object.freeze({
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
    [MCP_RECOMMENDED_SIGNED_CONTEXT_HEADER]: DEEIX_SIGNED_CONTEXT_TOKEN,
  });

export const DEFAULT_MCP_HEADER_TEMPLATE_JSON = JSON.stringify(
  DEFAULT_MCP_HEADER_TEMPLATE,
  null,
  2,
);

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

export function buildMCPHeaderPreviewRequestKey(
  payload: AdminMCPHeaderTemplatePreviewPayload,
): string {
  return JSON.stringify([
    payload.headersJSON,
    payload.mode,
    payload.headersEnabled,
    payload.serverID ?? null,
  ]);
}

export function getMCPHeaderWarningMessageKey(
  code: MCPHeaderTemplateWarningCode,
):
  | "headerTemplate.warnings.unknownToken"
  | "headerTemplate.warnings.malformedToken"
  | "headerTemplate.warnings.signedContextNotReferenced"
  | "headerTemplate.warnings.signedContextNotConfigured"
  | "headerTemplate.warnings.signedContextHeadersDisabled" {
  switch (code) {
    case "unknown_token":
      return "headerTemplate.warnings.unknownToken";
    case "malformed_token":
      return "headerTemplate.warnings.malformedToken";
    case "signed_context_not_referenced":
      return "headerTemplate.warnings.signedContextNotReferenced";
    case "signed_context_not_configured":
      return "headerTemplate.warnings.signedContextNotConfigured";
    case "signed_context_headers_disabled":
      return "headerTemplate.warnings.signedContextHeadersDisabled";
    default: {
      const exhaustive: never = code;
      return exhaustive;
    }
  }
}

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
): MCPHeaderTemplateControlState {
  const featureControlsDisabled = outerDisabled || !headersEnabled;
  return {
    switchDisabled: outerDisabled,
    editorDisabled: featureControlsDisabled,
    modeDisabled: featureControlsDisabled,
    previewDisabled: featureControlsDisabled,
    defaultTemplateDisabled: featureControlsDisabled,
  };
}

export function nextMCPHeaderTemplateAfterApplyDefault(
  current: string,
  defaultTemplateDisabled: boolean,
): string {
  return defaultTemplateDisabled ? current : DEFAULT_MCP_HEADER_TEMPLATE_JSON;
}
