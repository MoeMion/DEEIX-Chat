"use client";

import * as React from "react";
import { Copy } from "lucide-react";
import { useTranslations } from "next-intl";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { SpinnerLabel } from "@/components/ui/spinner";
import {
  activateAdminMCPContextJWTRotation,
  cancelAdminMCPContextJWTRotation,
  prepareAdminMCPContextJWTRotation,
} from "@/features/admin/api/mcp";
import type {
  MCPContextJWTPrepareResult,
  MCPContextJWTStatus,
} from "@/features/admin/api/mcp.types";
import { canActivatePreparedContext } from "@/features/admin/model/mcp-context-jwt";
import { resolveAdminErrorMessage } from "@/features/admin/utils/admin-error";
import { writeClipboardText } from "@/shared/lib/clipboard";

type MCPContextSecretDialogProps = {
  accessToken: string;
  serverID: number;
  serverName: string;
  status: MCPContextJWTStatus;
  disabled?: boolean;
  onBusyChange: (busy: boolean) => void;
  onRefreshStatus: () => Promise<void>;
  onStatusChange: (status: MCPContextJWTStatus) => Promise<void>;
};

type RotationAction = "prepare" | "refresh" | "activate" | "cancel" | null;

const MAX_TIMEOUT_MS = 2_147_483_647;

export function MCPContextSecretDialog({
  accessToken,
  serverID,
  serverName,
  status,
  disabled = false,
  onBusyChange,
  onRefreshStatus,
  onStatusChange,
}: MCPContextSecretDialogProps) {
  const t = useTranslations("adminTools.serverDialog.contextJwt.rotation");
  const [open, setOpen] = React.useState(false);
  const [prepared, setPrepared] =
    React.useState<MCPContextJWTPrepareResult | null>(null);
  const [action, setAction] = React.useState<RotationAction>(null);
  const [targetConfirmed, setTargetConfirmed] = React.useState(false);
  const [copied, setCopied] = React.useState(false);
  const [refreshFailed, setRefreshFailed] = React.useState(false);
  const [localError, setLocalError] = React.useState<string | null>(null);
  const [eligibilityEpoch, setEligibilityEpoch] = React.useState(0);

  const pendingKeyID = status.pendingKeyID?.trim() ?? "";
  const preparedKeyID = prepared?.keyID.trim() ?? "";
  const preparedIsActivatable =
    prepared !== null && canActivatePreparedContext(status, prepared);
  const canActivate =
    preparedIsActivatable && targetConfirmed;

  React.useEffect(() => {
    onBusyChange(action !== null);
    return () => onBusyChange(false);
  }, [action, onBusyChange]);

  React.useEffect(() => {
    if (!open || !preparedKeyID || !status.pendingExpiresAt) return;

    const expiresAt = Date.parse(status.pendingExpiresAt);
    if (!Number.isFinite(expiresAt)) return;

    const remainingMS = expiresAt - Date.now();
    if (remainingMS <= 0) return;

    const timeoutID = window.setTimeout(
      () => setEligibilityEpoch((current) => current + 1),
      Math.min(remainingMS + 1, MAX_TIMEOUT_MS),
    );
    return () => window.clearTimeout(timeoutID);
  }, [
    eligibilityEpoch,
    open,
    pendingKeyID,
    preparedKeyID,
    status.pendingExpiresAt,
  ]);

  const clearPrepared = React.useCallback(() => {
    setPrepared(null);
    setTargetConfirmed(false);
    setCopied(false);
    setRefreshFailed(false);
  }, []);

  const handleOpenChange = React.useCallback(
    (nextOpen: boolean) => {
      if (!nextOpen) {
        clearPrepared();
        setLocalError(null);
      }
      setOpen(nextOpen);
    },
    [clearPrepared],
  );

  const handlePrepare = React.useCallback(async () => {
    if (disabled || action !== null || pendingKeyID || !accessToken) return;

    setAction("prepare");
    setLocalError(null);
    setTargetConfirmed(false);
    setCopied(false);
    setRefreshFailed(false);
    try {
      const result = await prepareAdminMCPContextJWTRotation(
        accessToken,
        serverID,
      );
      setPrepared(result);
      setOpen(true);

      try {
        await onRefreshStatus();
        setRefreshFailed(false);
      } catch (error) {
        setRefreshFailed(true);
        setLocalError(resolveAdminErrorMessage(error, t("refreshFailed")));
      }
    } catch (error) {
      setLocalError(resolveAdminErrorMessage(error, t("prepareFailed")));
    } finally {
      setAction(null);
    }
  }, [accessToken, action, disabled, onRefreshStatus, pendingKeyID, serverID, t]);

  const handleRetryStatus = React.useCallback(async () => {
    if (!prepared || action !== null) return;

    setAction("refresh");
    setLocalError(null);
    try {
      await onRefreshStatus();
      setRefreshFailed(false);
    } catch (error) {
      setRefreshFailed(true);
      setLocalError(resolveAdminErrorMessage(error, t("refreshFailed")));
    } finally {
      setAction(null);
    }
  }, [action, onRefreshStatus, prepared, t]);

  const handleCopy = React.useCallback(async () => {
    if (!prepared) return;
    setLocalError(null);
    try {
      await writeClipboardText(prepared.secret);
      setCopied(true);
    } catch {
      setCopied(false);
      setLocalError(t("copyFailed"));
    }
  }, [prepared, t]);

  const handleActivate = React.useCallback(async () => {
    const candidate = prepared;
    if (!candidate || action !== null || !targetConfirmed) {
      return;
    }
    if (!canActivatePreparedContext(status, candidate)) {
      setLocalError(t("notReady"));
      return;
    }

    setAction("activate");
    setLocalError(null);
    try {
      const nextStatus = await activateAdminMCPContextJWTRotation(
        accessToken,
        serverID,
        candidate.keyID,
      );
      clearPrepared();
      setOpen(false);
      try {
        await onStatusChange(nextStatus);
      } catch (error) {
        setLocalError(resolveAdminErrorMessage(error, t("refreshFailed")));
      }
    } catch (error) {
      setLocalError(resolveAdminErrorMessage(error, t("activateFailed")));
    } finally {
      setAction(null);
    }
  }, [
    accessToken,
    action,
    clearPrepared,
    onStatusChange,
    prepared,
    serverID,
    status,
    t,
    targetConfirmed,
  ]);

  const handleCancel = React.useCallback(async () => {
    const keyID = status.pendingKeyID?.trim() ?? "";
    if (!keyID || action !== null) return;

    setAction("cancel");
    setLocalError(null);
    try {
      const nextStatus = await cancelAdminMCPContextJWTRotation(
        accessToken,
        serverID,
        keyID,
      );
      clearPrepared();
      setOpen(false);
      try {
        await onStatusChange(nextStatus);
      } catch (error) {
        setLocalError(resolveAdminErrorMessage(error, t("refreshFailed")));
      }
    } catch (error) {
      setLocalError(resolveAdminErrorMessage(error, t("cancelFailed")));
    } finally {
      setAction(null);
    }
  }, [
    accessToken,
    action,
    clearPrepared,
    onStatusChange,
    serverID,
    status.pendingKeyID,
    t,
  ]);

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={disabled || action !== null || Boolean(pendingKeyID) || !accessToken}
          onClick={() => void handlePrepare()}
        >
          {action === "prepare" ? (
            <SpinnerLabel>{t("preparing")}</SpinnerLabel>
          ) : (
            t("prepare")
          )}
        </Button>
        {pendingKeyID ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={disabled || action !== null || !accessToken}
            onClick={() => void handleCancel()}
          >
            {action === "cancel" ? (
              <SpinnerLabel>{t("canceling")}</SpinnerLabel>
            ) : (
              t("cancelPending")
            )}
          </Button>
        ) : null}
      </div>

      {pendingKeyID ? (
        <p className="text-xs text-muted-foreground">
          {t("pendingHelp", {
            keyID: pendingKeyID,
            expiresAt: status.pendingExpiresAt || t("unknownExpiry"),
          })}
        </p>
      ) : null}
      {localError && !open ? (
        <p className="text-xs text-destructive" role="alert">
          {localError}
        </p>
      ) : null}

      <Dialog open={open} onOpenChange={handleOpenChange}>
        <DialogContent className="sm:max-w-[620px]">
          <DialogHeader>
            <DialogTitle>{t("dialogTitle")}</DialogTitle>
            <DialogDescription>{t("dialogDescription")}</DialogDescription>
          </DialogHeader>

          {prepared ? (
            <div className="space-y-4">
              <div className="rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-xs leading-5">
                <p className="font-medium text-foreground">{t("oneTime")}</p>
                <p className="text-muted-foreground">{t("secretManager")}</p>
                <p className="text-muted-foreground">{t("noRecovery")}</p>
              </div>

              <div className="space-y-1.5">
                <p className="text-xs font-medium text-foreground">{t("secret")}</p>
                <div className="flex gap-2">
                  <Input
                    value={prepared.secret}
                    readOnly
                    aria-label={t("secret")}
                    className="font-mono text-xs"
                  />
                  <Button
                    type="button"
                    size="icon"
                    variant="outline"
                    aria-label={t("copy")}
                    onClick={() => void handleCopy()}
                  >
                    <Copy className="size-4" />
                  </Button>
                </div>
                {copied ? (
                  <p className="text-xs text-emerald-600" role="status">
                    {t("copied")}
                  </p>
                ) : null}
              </div>

              <dl className="grid grid-cols-1 gap-2 text-xs sm:grid-cols-2">
                <PreparedValue label={t("header")} value={prepared.header} />
                <PreparedValue label={t("algorithm")} value={prepared.algorithm} />
                <PreparedValue label={t("issuer")} value={prepared.issuer} />
                <PreparedValue label={t("audience")} value={prepared.audience} />
                <PreparedValue label={t("keyId")} value={prepared.keyID} />
                <PreparedValue
                  label={t("expiresSeconds")}
                  value={String(prepared.expiresSeconds)}
                />
              </dl>

              <div className="space-y-2 rounded-md border p-3">
                <p className="text-xs text-muted-foreground">{t("dualKeyWindow")}</p>
                <p className="text-xs text-muted-foreground">{t("noAutomaticRollback")}</p>
                <label className="flex cursor-pointer items-start gap-2 text-xs leading-5">
                  <Checkbox
                    checked={targetConfirmed}
                    disabled={action !== null}
                    onCheckedChange={(checked) => setTargetConfirmed(checked === true)}
                  />
                  <span>{t("confirmation", { serverName })}</span>
                </label>
              </div>

              {!preparedIsActivatable ? (
                <p className="text-xs text-amber-700" role="status">
                  {t("notReady")}
                </p>
              ) : null}
              {localError ? (
                <div className="space-y-2">
                  <p className="text-xs text-destructive" role="alert">
                    {localError}
                  </p>
                  {refreshFailed ? (
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={action !== null}
                      onClick={() => void handleRetryStatus()}
                    >
                      {action === "refresh" ? (
                        <SpinnerLabel>{t("retryingStatus")}</SpinnerLabel>
                      ) : (
                        t("retryStatus")
                      )}
                    </Button>
                  ) : null}
                </div>
              ) : null}
            </div>
          ) : null}

          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => handleOpenChange(false)}
            >
              {t("close")}
            </Button>
            {pendingKeyID ? (
              <Button
                type="button"
                variant="outline"
                disabled={action !== null}
                onClick={() => void handleCancel()}
              >
                {action === "cancel" ? (
                  <SpinnerLabel>{t("canceling")}</SpinnerLabel>
                ) : (
                  t("cancelPending")
                )}
              </Button>
            ) : null}
            <Button
              type="button"
              disabled={!canActivate || action !== null}
              onClick={() => void handleActivate()}
            >
              {action === "activate" ? (
                <SpinnerLabel>{t("activating")}</SpinnerLabel>
              ) : (
                t("activate")
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function PreparedValue({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-md bg-muted/40 px-3 py-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="break-all font-mono text-foreground">{value || "-"}</dd>
    </div>
  );
}
