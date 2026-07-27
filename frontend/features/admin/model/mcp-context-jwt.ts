import type {
  MCPContextJWTPolicyPayload,
  MCPContextJWTPrepareResult,
  MCPContextJWTStatus,
} from "@/features/admin/api/mcp.types";

export type MCPContextJWTPolicyDraft = {
  expiresSeconds: string;
  includeName: boolean;
  includeEmail: boolean;
  includeRole: boolean;
};

export type MCPContextJWTPolicyError =
  | "expiresSecondsRequired"
  | "expiresSecondsInteger"
  | "expiresSecondsRange";

export type MCPContextJWTPolicyNormalization =
  | { ok: true; payload: MCPContextJWTPolicyPayload }
  | { ok: false; error: MCPContextJWTPolicyError };

export function normalizeMCPContextJWTPolicy(
  draft: MCPContextJWTPolicyDraft,
): MCPContextJWTPolicyNormalization {
  const raw = draft.expiresSeconds.trim();
  if (!raw) {
    return { ok: false, error: "expiresSecondsRequired" };
  }
  if (!/^[0-9]+$/.test(raw)) {
    return { ok: false, error: "expiresSecondsInteger" };
  }
  const expiresSeconds = Number(raw);
  if (!Number.isSafeInteger(expiresSeconds)) {
    return { ok: false, error: "expiresSecondsInteger" };
  }
  if (expiresSeconds < 60 || expiresSeconds > 900) {
    return { ok: false, error: "expiresSecondsRange" };
  }
  return {
    ok: true,
    payload: {
      expiresSeconds,
      includeName: draft.includeName,
      includeEmail: draft.includeEmail,
      includeRole: draft.includeRole,
    },
  };
}

export function canActivatePreparedContext(
  status: MCPContextJWTStatus,
  prepared: MCPContextJWTPrepareResult,
  nowMS: number = Date.now(),
): boolean {
  const pendingKeyID = status.pendingKeyID?.trim() ?? "";
  const preparedKeyID = prepared.keyID.trim();
  if (
    !pendingKeyID ||
    !preparedKeyID ||
    pendingKeyID !== preparedKeyID ||
    !prepared.secret.trim()
  ) {
    return false;
  }
  const pendingExpiresAt = status.pendingExpiresAt
    ? Date.parse(status.pendingExpiresAt)
    : Number.NaN;
  return Number.isFinite(pendingExpiresAt) && pendingExpiresAt > nowMS;
}
