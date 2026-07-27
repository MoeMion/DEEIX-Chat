import type {
  ContextJWTStatusResponse,
  CreateServerRequest,
  HeaderTemplatePreviewResponse,
  HeaderTemplateWarningResponse,
  PrepareContextJWTRotationResponse,
  PreviewHeaderTemplateRequest,
  ProbeServerResponse,
  ReorderServerOrderItem,
  ServerDataResponse,
  ServerListResponse,
  ServerResponse,
  ServerToolOrderListResponse,
  ServerToolOrderResponse,
  ToolListResponse,
  UpdateContextJWTRequest,
  UpdateServerRequest,
  UpdateToolRequest,
} from "@deeix/api-contract";

export type MCPContextJWTStatus = Omit<ContextJWTStatusResponse, "mode"> & {
  mode: "none" | "hs256";
};

export type MCPContextJWTPolicyPayload = UpdateContextJWTRequest;

export type MCPContextJWTPrepareResult = Omit<
  PrepareContextJWTRotationResponse,
  "templateToken" | "recommendedHeader" | "algorithm"
> & {
  templateToken: "{{DEEIX_SIGNED_CONTEXT}}";
  recommendedHeader: "X-MCP-CLIENT-SIGNED-CONTEXT";
  algorithm: "HS256";
};

export type AdminMCPServerDTO = Omit<
  ServerResponse,
  "contextJWT" | "headerWarnings"
> & {
  headerWarnings: MCPHeaderTemplateWarningDTO[];
  contextJWT: MCPContextJWTStatus;
};

export type AdminMCPServerCreatePayload = Omit<CreateServerRequest, "headersEnabled"> & {
  headersEnabled: boolean;
};

export type AdminMCPServerUpdatePayload = UpdateServerRequest;
export type AdminMCPServerListResponse = Omit<ServerListResponse, "results"> & {
  results: AdminMCPServerDTO[];
};
export type AdminMCPServerDataResponse = Omit<ServerDataResponse, "server"> & {
  server: AdminMCPServerDTO;
};
export type MCPHeaderTemplateMode = "chat" | "probe" | "sync";

export type MCPHeaderTemplateWarningCode =
  | "unknown_token"
  | "malformed_token"
  | "signed_context_not_referenced"
  | "signed_context_not_configured"
  | "signed_context_headers_disabled";

export type MCPHeaderTemplateWarningDTO = Omit<HeaderTemplateWarningResponse, "code"> & {
  code: MCPHeaderTemplateWarningCode;
};

export type AdminMCPHeaderTemplatePreviewPayload = Omit<
  PreviewHeaderTemplateRequest,
  "mode"
> & {
  mode: MCPHeaderTemplateMode;
};

export type AdminMCPHeaderTemplatePreviewDTO = Omit<
  HeaderTemplatePreviewResponse,
  "mode" | "warnings"
> & {
  mode: MCPHeaderTemplateMode;
  warnings: MCPHeaderTemplateWarningDTO[];
};

export type AdminMCPServerProbeDTO = Omit<ProbeServerResponse, "warnings"> & {
  warnings: MCPHeaderTemplateWarningDTO[];
};

export type AdminMCPToolListResponse = ToolListResponse;
export type AdminMCPToolPayload = UpdateToolRequest;
export type AdminMCPOrderItemPayload = ReorderServerOrderItem;
export type AdminMCPOrderGroupDTO = Omit<ServerToolOrderResponse, "server"> & {
  server: AdminMCPServerDTO;
};
export type AdminMCPOrderListResponse = Omit<ServerToolOrderListResponse, "results"> & {
  results: AdminMCPOrderGroupDTO[];
};
