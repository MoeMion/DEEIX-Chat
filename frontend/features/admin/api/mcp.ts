import { authedRequest } from "@/shared/api/authed-client";
import { pathParam } from "@/shared/api/http-client";
import type { MCPToolDTO } from "@/shared/api/mcp.types";
import type {
  AdminMCPHeaderTemplatePreviewDTO,
  AdminMCPServerDTO,
  AdminMCPServerDataResponse,
  AdminMCPServerListResponse,
  AdminMCPServerProbeDTO,
  AdminMCPOrderItemPayload,
  AdminMCPOrderListResponse,
  AdminMCPOrderGroupDTO,
  AdminMCPServerCreatePayload,
  AdminMCPServerUpdatePayload,
  AdminMCPToolListResponse,
  MCPContextJWTPolicyPayload,
  MCPContextJWTPrepareResult,
  MCPContextJWTStatus,
  MCPHeaderTemplateMode,
} from "@/features/admin/api/mcp.types";

export async function listAdminMCPServers(accessToken: string): Promise<AdminMCPServerDTO[]> {
  const data = await authedRequest<AdminMCPServerListResponse>(
    "/api/v1/admin/mcp/servers",
    {
      method: "GET",
      accessToken,
    },
    true,
  );
  return data.results ?? [];
}

export async function createAdminMCPServer(
  accessToken: string,
  payload: AdminMCPServerCreatePayload,
): Promise<AdminMCPServerDTO> {
  const data = await authedRequest<AdminMCPServerDataResponse>(
    "/api/v1/admin/mcp/servers",
    {
      method: "POST",
      accessToken,
      body: payload,
    },
    true,
  );
  return data.server;
}

export async function updateAdminMCPServer(
  accessToken: string,
  serverID: number,
  payload: AdminMCPServerUpdatePayload,
): Promise<AdminMCPServerDTO> {
  const data = await authedRequest<AdminMCPServerDataResponse>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}`,
    {
      method: "PATCH",
      accessToken,
      body: payload,
    },
    true,
  );
  return data.server;
}

export function updateAdminMCPContextJWTPolicy(
  accessToken: string,
  serverID: number,
  payload: MCPContextJWTPolicyPayload,
): Promise<MCPContextJWTStatus> {
  return authedRequest<MCPContextJWTStatus>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}/context-jwt`,
    {
      method: "PATCH",
      accessToken,
      body: payload,
    },
    true,
  );
}

export function prepareAdminMCPContextJWTRotation(
  accessToken: string,
  serverID: number,
): Promise<MCPContextJWTPrepareResult> {
  return authedRequest<MCPContextJWTPrepareResult>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}/context-jwt/rotations`,
    {
      method: "POST",
      accessToken,
    },
    true,
  );
}

export function activateAdminMCPContextJWTRotation(
  accessToken: string,
  serverID: number,
  kid: string,
): Promise<MCPContextJWTStatus> {
  return authedRequest<MCPContextJWTStatus>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}/context-jwt/rotations/${pathParam(kid)}/activate`,
    {
      method: "POST",
      accessToken,
    },
    true,
  );
}

export function cancelAdminMCPContextJWTRotation(
  accessToken: string,
  serverID: number,
  kid: string,
): Promise<MCPContextJWTStatus> {
  return authedRequest<MCPContextJWTStatus>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}/context-jwt/rotations/${pathParam(kid)}`,
    {
      method: "DELETE",
      accessToken,
    },
    true,
  );
}

export function disableAdminMCPContextJWT(
  accessToken: string,
  serverID: number,
): Promise<MCPContextJWTStatus> {
  return authedRequest<MCPContextJWTStatus>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}/context-jwt`,
    {
      method: "DELETE",
      accessToken,
    },
    true,
  );
}

export async function previewAdminMCPHeaderTemplate(
  accessToken: string,
  headersJSON: string,
  mode: MCPHeaderTemplateMode,
  signal?: AbortSignal,
): Promise<AdminMCPHeaderTemplatePreviewDTO> {
  return authedRequest<AdminMCPHeaderTemplatePreviewDTO>(
    "/api/v1/admin/mcp/header-templates/preview",
    {
      method: "POST",
      accessToken,
      body: { headersJSON, mode },
      signal,
    },
    true,
  );
}

export async function probeAdminMCPServer(
  accessToken: string,
  serverID: number,
): Promise<AdminMCPServerProbeDTO> {
  return authedRequest<AdminMCPServerProbeDTO>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}/probe`,
    { method: "POST", accessToken },
    true,
  );
}

export async function deleteAdminMCPServer(accessToken: string, serverID: number): Promise<{ deleted: boolean }> {
  return authedRequest<{ deleted: boolean }>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}`,
    {
      method: "DELETE",
      accessToken,
    },
    true,
  );
}

export async function listAdminMCPServerTools(accessToken: string, serverID: number): Promise<MCPToolDTO[]> {
  const data = await authedRequest<AdminMCPToolListResponse>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}/tools`,
    {
      method: "GET",
      accessToken,
    },
    true,
  );
  return data.results ?? [];
}

export async function syncAdminMCPServerTools(accessToken: string, serverID: number): Promise<MCPToolDTO[]> {
  const data = await authedRequest<AdminMCPToolListResponse>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}/sync`,
    {
      method: "POST",
      accessToken,
    },
    true,
  );
  return data.results ?? [];
}

export type AdminMCPToolPayload = {
  displayName?: string;
  description?: string;
  status?: "active" | "inactive";
};

export async function updateAdminMCPTool(
  accessToken: string,
  toolID: number,
  payload: AdminMCPToolPayload,
): Promise<MCPToolDTO> {
  return authedRequest<MCPToolDTO>(
    `/api/v1/admin/mcp/tools/${pathParam(String(toolID))}`,
    {
      method: "PATCH",
      accessToken,
      body: payload,
    },
    true,
  );
}

export async function updateAdminMCPServerToolsStatus(
  accessToken: string,
  serverID: number,
  status: "active" | "inactive",
  toolIDs: number[],
): Promise<MCPToolDTO[]> {
  const data = await authedRequest<AdminMCPToolListResponse>(
    `/api/v1/admin/mcp/servers/${pathParam(String(serverID))}/tools/status`,
    {
      method: "PATCH",
      accessToken,
      body: { status, toolIDs },
    },
    true,
  );
  return data.results ?? [];
}

export async function reorderAdminMCPServers(
  accessToken: string,
  servers: AdminMCPOrderItemPayload[],
): Promise<AdminMCPOrderGroupDTO[]> {
  const data = await authedRequest<AdminMCPOrderListResponse>(
    "/api/v1/admin/mcp/servers/order",
    {
      method: "PATCH",
      accessToken,
      body: { servers },
    },
    true,
  );
  return data.results ?? [];
}
