import type { MCPToolDTO } from "@/shared/api/mcp.types";

export type MCPContextJWTStatus = {
  mode: "none" | "hs256";
  configured: boolean;
  issuer: string;
  audience: string;
  keyID: string;
  expiresSeconds: number;
  includeName: boolean;
  includeEmail: boolean;
  includeRole: boolean;
  pendingKeyID?: string;
  pendingExpiresAt?: string;
};

export type MCPContextJWTPolicyPayload = {
  expiresSeconds: number;
  includeName: boolean;
  includeEmail: boolean;
  includeRole: boolean;
};

export type MCPContextJWTPrepareResult = {
  templateToken: "{{DEEIX_SIGNED_CONTEXT}}";
  recommendedHeader: "X-MCP-CLIENT-SIGNED-CONTEXT";
  algorithm: "HS256";
  secret: string;
  issuer: string;
  audience: string;
  keyID: string;
  expiresSeconds: number;
};

export type AdminMCPServerDTO = {
  id: number;
  publicID: string;
  name: string;
  baseURL: string;
  authTokenConfigured: boolean;
  headersEnabled: boolean;
  headersJSON: string;
  headerWarnings: MCPHeaderTemplateWarningDTO[];
  signedContextHeader: string;
  status: string;
  sortOrder: number;
  toolCount: number;
  activeToolCount: number;
  lastSyncedAt?: string | null;
  lastError: string;
  createdAt: string;
  updatedAt: string;
  contextJWT: MCPContextJWTStatus;
};

export type AdminMCPServerCreatePayload = {
  name: string;
  baseURL: string;
  authToken?: string;
  headersEnabled: boolean;
  headersJSON: string;
  status: "active" | "inactive";
};

export type AdminMCPServerUpdatePayload = {
  name?: string;
  baseURL?: string;
  authToken?: string;
  clearAuthToken?: boolean;
  headersEnabled?: boolean;
  headersJSON?: string;
  status?: "active" | "inactive";
};

export type AdminMCPServerListResponse = {
  results: AdminMCPServerDTO[];
};

export type AdminMCPServerDataResponse = {
  server: AdminMCPServerDTO;
};

export type MCPHeaderTemplateMode = "chat" | "probe" | "sync";

export type MCPHeaderTemplateWarningCode =
  | "unknown_token"
  | "malformed_token"
  | "signed_context_not_referenced"
  | "signed_context_not_configured"
  | "signed_context_headers_disabled";

export type MCPHeaderTemplateWarningDTO = {
  code: MCPHeaderTemplateWarningCode;
  headerName?: string;
  token?: string;
};

export type MCPHeaderPreviewItemDTO = {
  name: string;
  value: string;
  sensitive: boolean;
};

export type AdminMCPHeaderTemplatePreviewPayload = {
  headersJSON: string;
  mode: MCPHeaderTemplateMode;
  headersEnabled: boolean;
  serverID?: number;
};

export type AdminMCPHeaderTemplatePreviewDTO = {
  mode: MCPHeaderTemplateMode;
  supportedTokens: string[];
  warnings: MCPHeaderTemplateWarningDTO[];
  headers: MCPHeaderPreviewItemDTO[];
  signedContextHeader: string;
};

export type AdminMCPServerProbeDTO = {
  toolCount: number;
  warnings: MCPHeaderTemplateWarningDTO[];
};

export type AdminMCPToolListResponse = {
  results: MCPToolDTO[];
};

export type AdminMCPOrderItemPayload = {
  serverID: number;
  toolIDs: number[];
};

export type AdminMCPOrderGroupDTO = {
  server: AdminMCPServerDTO;
  tools: MCPToolDTO[];
};

export type AdminMCPOrderListResponse = {
  results: AdminMCPOrderGroupDTO[];
};
