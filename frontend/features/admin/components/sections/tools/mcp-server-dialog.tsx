"use client";

import * as React from "react";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { SpinnerLabel } from "@/components/ui/spinner";
import type {
  AdminMCPServerCreatePayload,
  AdminMCPServerDTO,
  AdminMCPServerProbeDTO,
  AdminMCPServerUpdatePayload,
  MCPHeaderTemplateMode,
} from "@/features/admin/api/mcp.types";
import { MCPHeaderTemplateEditor } from "@/features/admin/components/sections/tools/mcp-header-template-editor";
import { toServerUpdatePayload, type ServerFormState } from "@/features/admin/model/mcp-server-form";

type MCPServerDialogProps = {
  accessToken: string;
  open: boolean;
  original: AdminMCPServerDTO | null;
  onOpenChange: (open: boolean) => void;
  onCreated: (payload: AdminMCPServerCreatePayload) => Promise<AdminMCPServerDTO>;
  onUpdated: (
    serverID: number,
    payload: AdminMCPServerUpdatePayload,
  ) => Promise<AdminMCPServerDTO>;
  onProbe: (serverID: number) => Promise<AdminMCPServerProbeDTO>;
  onSync: (serverID: number) => Promise<void>;
  onSaved: (server: AdminMCPServerDTO) => Promise<void>;
};

const EMPTY_SERVER_FORM: ServerFormState = {
  name: "",
  baseURL: "",
  authToken: "",
  clearAuthToken: false,
  headersJSON: "{}",
  status: "active",
};

function toServerForm(server: AdminMCPServerDTO): ServerFormState {
  return {
    name: server.name,
    baseURL: server.baseURL,
    authToken: "",
    clearAuthToken: false,
    headersJSON: server.headersJSON || "{}",
    status: server.status === "active" ? "active" : "inactive",
  };
}

export function MCPServerDialog({
  accessToken,
  open,
  original,
  onOpenChange,
  onCreated,
  onUpdated,
  onProbe,
  onSync,
  onSaved,
}: MCPServerDialogProps) {
  const t = useTranslations("adminTools");
  const tActions = useTranslations("common.actions");
  const [form, setForm] = React.useState<ServerFormState>(EMPTY_SERVER_FORM);
  const [mode, setMode] = React.useState<MCPHeaderTemplateMode>("chat");
  const [previewIsCurrent, setPreviewIsCurrent] = React.useState(false);
  const [pending, setPending] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setForm(original ? toServerForm(original) : EMPTY_SERVER_FORM);
    setMode("chat");
    setPreviewIsCurrent(false);
  }, [open, original]);

  const updatePayload = React.useMemo(
    () => (original ? toServerUpdatePayload(form, original) : null),
    [form, original],
  );
  const createFieldsAreValid = form.name.trim().length > 0 && form.baseURL.trim().length > 0;
  const editHasChanges = updatePayload !== null && Object.keys(updatePayload).length > 0;
  const canSubmit =
    Boolean(accessToken) &&
    previewIsCurrent &&
    (original ? editHasChanges : createFieldsAreValid);

  const submit = React.useCallback(async () => {
    if (!canSubmit || pending) return;
    setPending(true);
    try {
      const saved = original
        ? await onUpdated(original.id, updatePayload ?? {})
        : await onCreated({
            name: form.name.trim(),
            baseURL: form.baseURL.trim(),
            ...(form.authToken.trim() ? { authToken: form.authToken.trim() } : {}),
            headersJSON: form.headersJSON.trim() || "{}",
            status: form.status,
          });

      const connectionChanged =
        original === null ||
        updatePayload?.baseURL !== undefined ||
        updatePayload?.authToken !== undefined ||
        updatePayload?.clearAuthToken === true ||
        updatePayload?.headersJSON !== undefined;

      try {
        if (connectionChanged) {
          await onProbe(saved.id);
          await onSync(saved.id);
        }
      } catch (error) {
        await onSaved(saved);
        throw error;
      }
      await onSaved(saved);
      onOpenChange(false);
    } catch {
      // Parent callbacks own localized, safe API error toasts.
    } finally {
      setPending(false);
    }
  }, [canSubmit, form, onCreated, onOpenChange, onProbe, onSaved, onSync, onUpdated, original, pending, updatePayload]);

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!pending || nextOpen) onOpenChange(nextOpen);
      }}
    >
      <DialogContent className="flex max-h-[min(92vh,900px)] w-[calc(100vw-2rem)] flex-col gap-0 overflow-hidden p-0 sm:max-w-[760px]">
        <DialogHeader className="shrink-0 px-4 py-4">
          <DialogTitle>{original ? t("serverDialog.editTitle") : t("serverDialog.createTitle")}</DialogTitle>
          <DialogDescription>{t("serverDialog.description")}</DialogDescription>
        </DialogHeader>

        <form
          className="flex min-h-0 flex-1 flex-col"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 py-2">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div className="space-y-1">
                <p className="text-xs text-muted-foreground">
                  {t("serverDialog.name")} <span className="text-destructive">*</span>
                </p>
                <Input
                  value={form.name}
                  placeholder={t("serverDialog.namePlaceholder")}
                  disabled={pending}
                  onChange={(event) => setForm((previous) => ({ ...previous, name: event.target.value }))}
                  required
                />
              </div>
              <div className="space-y-1">
                <p className="text-xs text-muted-foreground">{t("serverDialog.status")}</p>
                <Select
                  value={form.status}
                  disabled={pending}
                  onValueChange={(status: "active" | "inactive") => setForm((previous) => ({ ...previous, status }))}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="active">{t("status.active")}</SelectItem>
                    <SelectItem value="inactive">{t("status.inactive")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="space-y-1">
              <p className="text-xs text-muted-foreground">
                {t("serverDialog.url")} <span className="text-destructive">*</span>
              </p>
              <Input
                value={form.baseURL}
                placeholder="https://example.com/mcp"
                disabled={pending}
                onChange={(event) => setForm((previous) => ({ ...previous, baseURL: event.target.value }))}
                required
              />
            </div>

            <div className="space-y-1">
              <p className="text-xs text-muted-foreground">{t("serverDialog.authToken")}</p>
              <Input
                type="password"
                autoComplete="new-password"
                value={form.authToken}
                placeholder={original ? t("serverDialog.authTokenEditPlaceholder") : t("serverDialog.authTokenCreatePlaceholder")}
                disabled={pending || form.clearAuthToken}
                onChange={(event) => setForm((previous) => ({
                  ...previous,
                  authToken: event.target.value,
                  clearAuthToken: false,
                }))}
              />
              <p className="text-xs text-muted-foreground">
                {original?.authTokenConfigured
                  ? t("serverDialog.authTokenConfiguredHelp")
                  : t("serverDialog.authTokenOptionalHelp")}
              </p>
              {original?.authTokenConfigured ? (
                <label className="flex w-fit cursor-pointer items-center gap-2 text-xs text-muted-foreground">
                  <Checkbox
                    checked={form.clearAuthToken}
                    disabled={pending}
                    onCheckedChange={(checked) => setForm((previous) => ({
                      ...previous,
                      authToken: checked === true ? "" : previous.authToken,
                      clearAuthToken: checked === true,
                    }))}
                  />
                  <span>{t("serverDialog.removeConfiguredAuthToken")}</span>
                </label>
              ) : null}
            </div>

            <MCPHeaderTemplateEditor
              accessToken={accessToken}
              value={form.headersJSON}
              mode={mode}
              disabled={pending}
              onChange={(headersJSON) => setForm((previous) => ({ ...previous, headersJSON }))}
              onModeChange={setMode}
              onValidityChange={setPreviewIsCurrent}
            />
          </div>

          <DialogFooter className="shrink-0 px-4 py-3">
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)} disabled={pending}>
              {tActions("cancel")}
            </Button>
            <Button type="submit" disabled={pending || !canSubmit}>
              {pending ? (
                <SpinnerLabel>{original ? tActions("saving") : t("serverDialog.creating")}</SpinnerLabel>
              ) : original ? tActions("save") : tActions("create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
