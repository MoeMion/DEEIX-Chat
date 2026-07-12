"use client";

import * as React from "react";
import { useTranslations } from "next-intl";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { SpinnerLabel } from "@/components/ui/spinner";
import {
  disableAdminMCPContextJWT,
  updateAdminMCPContextJWTPolicy,
} from "@/features/admin/api/mcp";
import type {
  AdminMCPServerDTO,
  MCPContextJWTStatus,
} from "@/features/admin/api/mcp.types";
import { MCPContextSecretDialog } from "@/features/admin/components/sections/tools/mcp-context-secret-dialog";
import {
  normalizeMCPContextJWTPolicy,
  type MCPContextJWTPolicyDraft,
} from "@/features/admin/model/mcp-context-jwt";
import { resolveAdminErrorMessage } from "@/features/admin/utils/admin-error";

type MCPContextJWTPanelProps = {
  accessToken: string;
  server: AdminMCPServerDTO;
  disabled?: boolean;
  onBusyChange: (busy: boolean) => void;
  onRefreshStatus: () => Promise<void>;
  onStatusChange: (status: MCPContextJWTStatus) => Promise<void>;
};

function policyDraftFromStatus(
  status: MCPContextJWTStatus,
): MCPContextJWTPolicyDraft {
  return {
    expiresSeconds: String(
      status.expiresSeconds >= 60 && status.expiresSeconds <= 900
        ? status.expiresSeconds
        : 300,
    ),
    includeName: status.includeName,
    includeEmail: status.includeEmail,
    includeRole: status.includeRole,
  };
}

export function MCPContextJWTPanel({
  accessToken,
  server,
  disabled = false,
  onBusyChange,
  onRefreshStatus,
  onStatusChange,
}: MCPContextJWTPanelProps) {
  const t = useTranslations("adminTools.serverDialog.contextJwt");
  const tActions = useTranslations("common.actions");
  const [draft, setDraft] = React.useState<MCPContextJWTPolicyDraft>(() =>
    policyDraftFromStatus(server.contextJWT),
  );
  const [policyPending, setPolicyPending] = React.useState(false);
  const [rotationPending, setRotationPending] = React.useState(false);
  const [disablePending, setDisablePending] = React.useState(false);
  const [disableOpen, setDisableOpen] = React.useState(false);
  const [policyError, setPolicyError] = React.useState<string | null>(null);
  const [disableError, setDisableError] = React.useState<string | null>(null);
  const [policySaved, setPolicySaved] = React.useState(false);
  const busy = policyPending || rotationPending || disablePending;

  const status = server.contextJWT;

  React.useEffect(() => {
    onBusyChange(busy);
  }, [busy, onBusyChange]);

  React.useEffect(
    () => () => {
      onBusyChange(false);
    },
    [onBusyChange],
  );

  const handleSavePolicy = React.useCallback(async () => {
    if (
      disabled ||
      policyPending ||
      rotationPending ||
      disablePending ||
      !accessToken
    ) {
      return;
    }

    const normalized = normalizeMCPContextJWTPolicy(draft);
    if (normalized.ok === false) {
      setPolicySaved(false);
      setPolicyError(t(`policy.errors.${normalized.error}`));
      return;
    }

    setPolicyPending(true);
    setPolicySaved(false);
    setPolicyError(null);
    try {
      const nextStatus = await updateAdminMCPContextJWTPolicy(
        accessToken,
        server.id,
        normalized.payload,
      );
      await onStatusChange(nextStatus);
      setPolicySaved(true);
    } catch (error) {
      setPolicyError(
        resolveAdminErrorMessage(error, t("policy.updateFailed")),
      );
    } finally {
      setPolicyPending(false);
    }
  }, [
    accessToken,
    disabled,
    disablePending,
    draft,
    onStatusChange,
    policyPending,
    rotationPending,
    server.id,
    t,
  ]);

  const handleDisable = React.useCallback(async () => {
    if (
      disabled ||
      disablePending ||
      policyPending ||
      rotationPending ||
      !accessToken
    ) {
      return;
    }

    setDisablePending(true);
    setDisableError(null);
    setPolicySaved(false);
    try {
      const nextStatus = await disableAdminMCPContextJWT(
        accessToken,
        server.id,
      );
      setDraft(policyDraftFromStatus(nextStatus));
      setDisableOpen(false);
      await onStatusChange(nextStatus);
    } catch (error) {
      setDisableError(
        resolveAdminErrorMessage(error, t("disable.failed")),
      );
    } finally {
      setDisablePending(false);
    }
  }, [
    accessToken,
    disabled,
    disablePending,
    onStatusChange,
    policyPending,
    rotationPending,
    server.id,
    t,
  ]);

  return (
    <section className="space-y-4 rounded-md border border-border/70 p-4">
      <div className="space-y-1">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-sm font-medium text-foreground">{t("title")}</h3>
          <span className="rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground">
            {status.configured ? t("state.configured") : t("state.notConfigured")}
          </span>
        </div>
        <p className="text-xs leading-5 text-muted-foreground">
          {t("description")}
        </p>
      </div>

      <dl className="grid grid-cols-1 gap-2 text-xs sm:grid-cols-2">
        <StatusValue label={t("state.header")} value="X-DEEIX-Context" />
        <StatusValue label={t("state.mode")} value={status.mode} />
        <StatusValue label={t("state.issuer")} value={status.issuer} />
        <StatusValue label={t("state.audience")} value={status.audience} />
        <StatusValue label={t("state.currentKey")} value={status.keyID} />
        <StatusValue
          label={t("state.pendingKey")}
          value={status.pendingKeyID ?? ""}
        />
        <StatusValue
          label={t("state.pendingExpiresAt")}
          value={status.pendingExpiresAt ?? ""}
        />
      </dl>

      <div className="space-y-3 border-t pt-4">
        <div className="space-y-1">
          <h4 className="text-xs font-medium text-foreground">
            {t("policy.title")}
          </h4>
          <p className="text-xs leading-5 text-muted-foreground">
            {t("policy.description")}
          </p>
          <p className="text-xs leading-5 text-muted-foreground">
            {t("policy.piiOptIn")}
          </p>
        </div>

        <div className="max-w-[220px] space-y-1">
          <label
            className="text-xs text-muted-foreground"
            htmlFor={`mcp-context-ttl-${server.id}`}
          >
            {t("policy.expiresSeconds")}
          </label>
          <Input
            id={`mcp-context-ttl-${server.id}`}
            type="number"
            inputMode="numeric"
            min={60}
            max={900}
            step={1}
            value={draft.expiresSeconds}
            disabled={disabled || policyPending || rotationPending || disablePending}
            onChange={(event) => {
              setPolicySaved(false);
              setPolicyError(null);
              setDraft((previous) => ({
                ...previous,
                expiresSeconds: event.target.value,
              }));
            }}
          />
          <p className="text-[11px] text-muted-foreground">
            {t("policy.expiresHelp")}
          </p>
        </div>

        <div className="space-y-2">
          <ClaimCheckbox
            label={t("policy.includeName")}
            help={t("policy.includeNameHelp")}
            checked={draft.includeName}
            disabled={disabled || policyPending || rotationPending || disablePending}
            onCheckedChange={(checked) => {
              setPolicySaved(false);
              setPolicyError(null);
              setDraft((previous) => ({ ...previous, includeName: checked }));
            }}
          />
          <ClaimCheckbox
            label={t("policy.includeEmail")}
            help={t("policy.includeEmailHelp")}
            checked={draft.includeEmail}
            disabled={disabled || policyPending || rotationPending || disablePending}
            onCheckedChange={(checked) => {
              setPolicySaved(false);
              setPolicyError(null);
              setDraft((previous) => ({ ...previous, includeEmail: checked }));
            }}
          />
          <ClaimCheckbox
            label={t("policy.includeRole")}
            help={t("policy.includeRoleHelp")}
            checked={draft.includeRole}
            disabled={disabled || policyPending || rotationPending || disablePending}
            onCheckedChange={(checked) => {
              setPolicySaved(false);
              setPolicyError(null);
              setDraft((previous) => ({ ...previous, includeRole: checked }));
            }}
          />
          <p className="text-xs leading-5 text-amber-700">
            {t("policy.roleNotAuthorization")}
          </p>
        </div>

        <Button
          type="button"
          size="sm"
          disabled={
            disabled ||
            policyPending ||
            rotationPending ||
            disablePending ||
            !accessToken
          }
          onClick={() => void handleSavePolicy()}
        >
          {policyPending ? (
            <SpinnerLabel>{t("policy.saving")}</SpinnerLabel>
          ) : (
            t("policy.save")
          )}
        </Button>
        {policySaved ? (
          <p className="text-xs text-emerald-600" role="status">
            {t("policy.saved")}
          </p>
        ) : null}
        {policyError ? (
          <p className="text-xs text-destructive" role="alert">
            {policyError}
          </p>
        ) : null}
      </div>

      <div className="space-y-3 border-t pt-4">
        <div className="space-y-1">
          <h4 className="text-xs font-medium text-foreground">
            {t("rotation.title")}
          </h4>
          <p className="text-xs leading-5 text-muted-foreground">
            {t("rotation.description")}
          </p>
          <p className="text-xs leading-5 text-muted-foreground">
            {t("rotation.dualKeyWindow")}
          </p>
          <p className="text-xs leading-5 text-muted-foreground">
            {t("rotation.noAutomaticRollback")}
          </p>
        </div>

        <MCPContextSecretDialog
          accessToken={accessToken}
          serverID={server.id}
          serverName={server.name}
          status={status}
          disabled={disabled || policyPending || disablePending}
          onBusyChange={setRotationPending}
          onRefreshStatus={onRefreshStatus}
          onStatusChange={onStatusChange}
        />

        <Button
          type="button"
          size="sm"
          variant="destructive"
          disabled={
            disabled ||
            disablePending ||
            policyPending ||
            rotationPending ||
            !accessToken
          }
          onClick={() => {
            setDisableError(null);
            setDisableOpen(true);
          }}
        >
          {t("disable.button")}
        </Button>
      </div>

      <AlertDialog
        open={disableOpen}
        onOpenChange={(nextOpen) => {
          if (!nextOpen && disablePending) return;
          setDisableOpen(nextOpen);
          if (!nextOpen) setDisableError(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("disable.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("disable.description", { serverName: server.name })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {disableError ? (
            <p className="text-xs text-destructive" role="alert">
              {disableError}
            </p>
          ) : null}
          <AlertDialogFooter>
            <AlertDialogCancel
              disabled={disabled || disablePending || policyPending || rotationPending}
            >
              {tActions("cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={disabled || disablePending || policyPending || rotationPending}
              onClick={(event) => {
                event.preventDefault();
                void handleDisable();
              }}
            >
              {disablePending ? (
                <SpinnerLabel>{t("disable.disabling")}</SpinnerLabel>
              ) : (
                t("disable.confirm")
              )}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}

function StatusValue({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-md bg-muted/40 px-3 py-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="break-all font-mono text-foreground">{value || "-"}</dd>
    </div>
  );
}

function ClaimCheckbox({
  label,
  help,
  checked,
  disabled,
  onCheckedChange,
}: {
  label: string;
  help: string;
  checked: boolean;
  disabled: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <label className="flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2">
      <Checkbox
        checked={checked}
        disabled={disabled}
        onCheckedChange={(value) => onCheckedChange(value === true)}
      />
      <span className="space-y-0.5">
        <span className="block text-xs text-foreground">{label}</span>
        <span className="block text-[11px] leading-4 text-muted-foreground">
          {help}
        </span>
      </span>
    </label>
  );
}
