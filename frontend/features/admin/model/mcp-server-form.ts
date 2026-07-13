import type {
  AdminMCPServerCreatePayload,
  AdminMCPServerDTO,
  AdminMCPServerUpdatePayload,
} from "@/features/admin/api/mcp.types";

export type ServerFormState = {
  name: string;
  baseURL: string;
  authToken: string;
  clearAuthToken: boolean;
  headersEnabled: boolean;
  headersJSON: string;
  status: "active" | "inactive";
};

export const EMPTY_SERVER_FORM: ServerFormState = {
  name: "",
  baseURL: "",
  authToken: "",
  clearAuthToken: false,
  headersEnabled: true,
  headersJSON: "{}",
  status: "active",
};

export function serverFormFromDTO(server: AdminMCPServerDTO): ServerFormState {
  return {
    name: server.name,
    baseURL: server.baseURL,
    authToken: "",
    clearAuthToken: false,
    headersEnabled: server.headersEnabled,
    headersJSON: server.headersJSON || "{}",
    status: server.status === "active" ? "active" : "inactive",
  };
}

export function toServerCreatePayload(form: ServerFormState): AdminMCPServerCreatePayload {
  const authToken = form.authToken.trim();
  return {
    name: form.name.trim(),
    baseURL: form.baseURL.trim(),
    ...(authToken ? { authToken } : {}),
    headersEnabled: form.headersEnabled,
    headersJSON: form.headersJSON.trim() || "{}",
    status: form.status,
  };
}

export function toServerUpdatePayload(
  form: ServerFormState,
  original: AdminMCPServerDTO,
): AdminMCPServerUpdatePayload {
  const payload: AdminMCPServerUpdatePayload = {};
  if (form.name.trim() !== original.name) payload.name = form.name.trim();
  if (form.baseURL.trim() !== original.baseURL) payload.baseURL = form.baseURL.trim();
  if (form.status !== original.status) payload.status = form.status;
  if (form.headersEnabled !== original.headersEnabled) {
    payload.headersEnabled = form.headersEnabled;
  }
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

export type ServerFormSubmitContext = {
  accessTokenPresent: boolean;
  contextPending: boolean;
  previewIsCurrent: boolean;
  original: AdminMCPServerDTO | null;
};

export function canSubmitServerForm(
  form: ServerFormState,
  context: ServerFormSubmitContext,
): boolean {
  const headersReady = !form.headersEnabled || context.previewIsCurrent;
  const formIsReady = context.original
    ? Object.keys(toServerUpdatePayload(form, context.original)).length > 0
    : form.name.trim().length > 0 && form.baseURL.trim().length > 0;

  return context.accessTokenPresent && !context.contextPending && headersReady && formIsReady;
}

export function hasMCPConnectionChanges(
  original: AdminMCPServerDTO | null,
  payload: AdminMCPServerUpdatePayload | null,
): boolean {
  return (
    original === null ||
    (payload !== null &&
      (payload.baseURL !== undefined ||
        payload.authToken !== undefined ||
        payload.clearAuthToken === true ||
        payload.headersEnabled !== undefined ||
        payload.headersJSON !== undefined))
  );
}
