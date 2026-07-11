import type { MCPToolDTO } from "@/shared/api/mcp.types";

export type AdminMCPServerDTO = {
  id: number;
  name: string;
  baseURL: string;
  authTokenConfigured: boolean;
  headersJSON: string;
  status: string;
  sortOrder: number;
  toolCount: number;
  activeToolCount: number;
  lastSyncedAt?: string | null;
  lastError: string;
  createdAt: string;
  updatedAt: string;
};

export type AdminMCPServerCreatePayload = {
  name: string;
  baseURL: string;
  authToken?: string;
  headersJSON: string;
  status: "active" | "inactive";
};

export type AdminMCPServerUpdatePayload = {
  name?: string;
  baseURL?: string;
  authToken?: string;
  clearAuthToken?: boolean;
  headersJSON?: string;
  status?: "active" | "inactive";
};

export type AdminMCPServerListResponse = {
  results: AdminMCPServerDTO[];
};

export type AdminMCPServerDataResponse = {
  server: AdminMCPServerDTO;
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
