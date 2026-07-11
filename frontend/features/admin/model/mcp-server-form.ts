import type {
  AdminMCPServerDTO,
  AdminMCPServerUpdatePayload,
} from "@/features/admin/api/mcp.types";

export type ServerFormState = {
  name: string;
  baseURL: string;
  authToken: string;
  clearAuthToken: boolean;
  headersJSON: string;
  status: "active" | "inactive";
};

export function toServerUpdatePayload(
  form: ServerFormState,
  original: AdminMCPServerDTO,
): AdminMCPServerUpdatePayload {
  const payload: AdminMCPServerUpdatePayload = {};
  if (form.name.trim() !== original.name) payload.name = form.name.trim();
  if (form.baseURL.trim() !== original.baseURL) payload.baseURL = form.baseURL.trim();
  if (form.status !== original.status) payload.status = form.status;
  if (form.headersJSON !== (original.headersJSON || "{}")) {
    payload.headersJSON = form.headersJSON.trim() || "{}";
  }
  const replacement = form.authToken.trim();
  if (replacement) {
    payload.authToken = replacement;
  } else if (form.clearAuthToken && original.authTokenConfigured) {
    payload.clearAuthToken = true;
  }
  return payload;
}
