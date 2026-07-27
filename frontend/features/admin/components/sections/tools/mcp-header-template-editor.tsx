"use client";

import * as React from "react";
import { CircleHelp } from "lucide-react";
import { useTranslations } from "next-intl";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { SpinnerLabel } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import type {
  AdminMCPHeaderTemplatePreviewDTO,
  MCPHeaderTemplateMode,
} from "@/features/admin/api/mcp.types";
import { previewAdminMCPHeaderTemplate } from "@/features/admin/api/mcp";
import {
  DEEIX_SIGNED_CONTEXT_TOKEN,
  buildMCPHeaderPreviewRequestKey,
  getMCPHeaderTemplateControlState,
  getMCPHeaderWarningMessageKey,
  nextMCPHeaderTemplateAfterApplyDefault,
  validateHeaderTemplateJSON,
  type MCPHeaderTemplatePreflightError,
} from "@/features/admin/model/mcp-header-template";
import { resolveAdminErrorMessage } from "@/features/admin/utils/admin-error";
import { JsonCodeEditor } from "@/shared/components/json-code-editor";

type MCPHeaderTemplateEditorProps = {
  accessToken: string;
  value: string;
  mode: MCPHeaderTemplateMode;
  headersEnabled: boolean;
  serverID?: number;
  disabled?: boolean;
  onChange: (value: string) => void;
  onHeadersEnabledChange: (enabled: boolean) => void;
  onModeChange: (mode: MCPHeaderTemplateMode) => void;
  onValidityChange: (valid: boolean) => void;
};

function preflightMessageKey(error: MCPHeaderTemplatePreflightError) {
  switch (error) {
    case "invalidJson":
      return "headerTemplate.preflight.invalidJson";
    case "objectRequired":
      return "headerTemplate.preflight.objectRequired";
    case "stringValuesRequired":
      return "headerTemplate.preflight.stringValuesRequired";
  }
}

function modeHelpMessageKey(mode: MCPHeaderTemplateMode) {
  switch (mode) {
    case "chat":
      return "headerTemplate.modeHelp.chat";
    case "probe":
      return "headerTemplate.modeHelp.probe";
    case "sync":
      return "headerTemplate.modeHelp.sync";
  }
}

function tokenDescriptionMessageKey(token: string) {
  switch (token) {
    case "{{DEEIX_USER_PUBLIC_ID}}":
      return "headerTemplate.tokens.userPublicId";
    case "{{DEEIX_USER_DISPLAY_NAME}}":
      return "headerTemplate.tokens.userDisplayName";
    case "{{DEEIX_USER_EMAIL}}":
      return "headerTemplate.tokens.userEmail";
    case "{{DEEIX_USER_ROLE}}":
      return "headerTemplate.tokens.userRole";
    case "{{DEEIX_CONVERSATION_PUBLIC_ID}}":
      return "headerTemplate.tokens.conversationPublicId";
    case "{{DEEIX_ASSISTANT_MESSAGE_PUBLIC_ID}}":
      return "headerTemplate.tokens.assistantMessagePublicId";
    case "{{DEEIX_USER_MESSAGE_PUBLIC_ID}}":
      return "headerTemplate.tokens.userMessagePublicId";
    case "{{DEEIX_REQUEST_ID}}":
      return "headerTemplate.tokens.requestId";
    case "{{DEEIX_RUN_ID}}":
      return "headerTemplate.tokens.runId";
    case "{{DEEIX_TRACE_ID}}":
      return "headerTemplate.tokens.traceId";
    case DEEIX_SIGNED_CONTEXT_TOKEN:
      return "headerTemplate.tokens.signedContext";
    default:
      return "headerTemplate.tokens.unknown";
  }
}

export function MCPHeaderTemplateEditor({
  accessToken,
  value,
  mode,
  headersEnabled,
  serverID,
  disabled = false,
  onChange,
  onHeadersEnabledChange,
  onModeChange,
  onValidityChange,
}: MCPHeaderTemplateEditorProps) {
  const t = useTranslations("adminTools.serverDialog");
  const [helpOpen, setHelpOpen] = React.useState(false);
  const [preview, setPreview] = React.useState<AdminMCPHeaderTemplatePreviewDTO | null>(null);
  const [previewedRequestKey, setPreviewedRequestKey] = React.useState("");
  const [previewError, setPreviewError] = React.useState<unknown>(null);
  const preflightError = validateHeaderTemplateJSON(value);
  const {
    switchDisabled,
    editorDisabled,
    modeDisabled,
    previewDisabled,
    defaultTemplateDisabled,
  } = getMCPHeaderTemplateControlState(disabled, headersEnabled);
  const previewPayload = React.useMemo(
    () => ({ headersJSON: value, mode, headersEnabled, serverID }),
    [headersEnabled, mode, serverID, value],
  );
  const previewRequestKey = buildMCPHeaderPreviewRequestKey(previewPayload);

  React.useEffect(() => {
    setPreview(null);
    setPreviewedRequestKey("");
    setPreviewError(null);
    if (preflightError || previewDisabled) return;

    const controller = new AbortController();
    const exactPayload = previewPayload;
    const exactRequestKey = previewRequestKey;
    const timer = window.setTimeout(() => {
      void previewAdminMCPHeaderTemplate(accessToken, exactPayload, controller.signal)
        .then((result) => {
          if (!controller.signal.aborted) {
            setPreview(result);
            setPreviewedRequestKey(exactRequestKey);
          }
        })
        .catch((error: unknown) => {
          if (!controller.signal.aborted) setPreviewError(error);
        });
    }, 300);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [accessToken, preflightError, previewDisabled, previewPayload, previewRequestKey]);

  const previewIsCurrent =
    !previewDisabled &&
    preflightError === null &&
    preview !== null &&
    previewedRequestKey === previewRequestKey &&
    previewError === null;

  React.useEffect(() => {
    onValidityChange(previewIsCurrent);
  }, [onValidityChange, previewIsCurrent]);

  const previewPending =
    !previewDisabled &&
    preflightError === null &&
    preview === null &&
    previewError === null;

  const handleApplyDefault = () => {
    const nextValue = nextMCPHeaderTemplateAfterApplyDefault(
      value,
      defaultTemplateDisabled,
    );
    if (nextValue === value) return;
    onValidityChange(false);
    onChange(nextValue);
  };

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1">
            <p className="text-xs font-medium text-foreground">{t("headerTemplate.title")}</p>
            <Tooltip open={helpOpen} onOpenChange={setHelpOpen}>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="size-6 text-muted-foreground"
                  aria-label={t("headerTemplate.helpLabel")}
                  onClick={() => setHelpOpen(true)}
                >
                  <CircleHelp className="size-3.5" aria-hidden="true" />
                </Button>
              </TooltipTrigger>
              <TooltipContent
                side="bottom"
                align="start"
                className="max-w-sm space-y-1 text-left leading-4"
              >
                <p>{t("headerTemplate.backendAuthority")}</p>
                <p>{t("headerTemplate.plaintextWarning")}</p>
                <p>{t("headerTemplate.blacklistWarning")}</p>
                <p>{t("headerTemplate.maskedPreservation")}</p>
              </TooltipContent>
            </Tooltip>
          </div>
          <p className="text-[11px] leading-4 text-muted-foreground">{t("headerTemplate.serverExpansion")}</p>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <label className="flex items-center gap-2 text-xs text-foreground">
            <Switch
              size="sm"
              checked={headersEnabled}
              disabled={switchDisabled}
              onCheckedChange={(enabled) => {
                onValidityChange(false);
                onHeadersEnabledChange(enabled);
              }}
            />
            <span>{t("headerTemplate.enabled")}</span>
          </label>
          <Select
            value={mode}
            disabled={modeDisabled}
            onValueChange={(nextMode: MCPHeaderTemplateMode) => {
              onValidityChange(false);
              onModeChange(nextMode);
            }}
          >
            <SelectTrigger className="h-8 w-[150px] text-xs" aria-label={t("headerTemplate.mode.label")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="chat">{t("headerTemplate.mode.chat")}</SelectItem>
              <SelectItem value="probe">{t("headerTemplate.mode.probe")}</SelectItem>
              <SelectItem value="sync">{t("headerTemplate.mode.sync")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="space-y-0.5">
          <p className="text-[11px] leading-4 text-muted-foreground">
            {t(headersEnabled ? "headerTemplate.enabledHelp" : "headerTemplate.disabledHelp")}
          </p>
          <p className="text-[11px] leading-4 text-muted-foreground">{t(modeHelpMessageKey(mode))}</p>
        </div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={defaultTemplateDisabled}
          onClick={handleApplyDefault}
        >
          {t("headerTemplate.applyDefault")}
        </Button>
      </div>

      <JsonCodeEditor
        value={value}
        disabled={editorDisabled}
        height={190}
        placeholder={'{"X-Tenant": "value"}'}
        onChange={(nextValue) => {
          onValidityChange(false);
          onChange(nextValue);
        }}
      />

      {preflightError ? (
        <p className="text-[11px] leading-4 text-destructive">{t(preflightMessageKey(preflightError))}</p>
      ) : null}

      {previewPending ? (
        <p className="text-[11px] text-muted-foreground">
          <SpinnerLabel>{t("headerTemplate.preview.loading")}</SpinnerLabel>
        </p>
      ) : null}
      {!previewDisabled && previewError ? (
        <p className="text-[11px] leading-4 text-destructive">
          {resolveAdminErrorMessage(previewError, t("headerTemplate.preview.failed"))}
        </p>
      ) : null}

      {!previewDisabled && preview ? (
        <div className="grid gap-3 lg:grid-cols-2">
          <section className="space-y-2 rounded-md border border-border/70 p-3">
            <div className="flex items-center justify-between gap-2">
              <p className="text-xs font-medium">{t("headerTemplate.tokens.title")}</p>
              <Badge variant="outline">{t("headerTemplate.modeValue", { mode: t(`headerTemplate.mode.${preview.mode}`) })}</Badge>
            </div>
            <div className="space-y-2">
              {preview.supportedTokens.map((token) => (
                <div key={token} className="space-y-0.5">
                  <code className="break-all text-[10px] text-foreground">{token}</code>
                  <p className="text-[10px] leading-4 text-muted-foreground">
                    {t(tokenDescriptionMessageKey(token))}
                  </p>
                </div>
              ))}
            </div>
          </section>

          <div className="space-y-3">
            <section className="space-y-2 rounded-md border border-border/70 p-3">
              <p className="text-xs font-medium">{t("headerTemplate.preview.headers")}</p>
              {preview.headers.length === 0 ? (
                <p className="text-[11px] text-muted-foreground">{t("headerTemplate.preview.emptyHeaders")}</p>
              ) : (
                <div className="space-y-2">
                  {preview.headers.map((header) => (
                    <div key={header.name} className="min-w-0 rounded bg-muted/50 px-2 py-1.5">
                      <div className="flex items-center justify-between gap-2">
                        <code className="truncate text-[10px] font-semibold">{header.name}</code>
                        {header.sensitive ? <Badge variant="secondary">{t("headerTemplate.preview.masked")}</Badge> : null}
                      </div>
                      <code className="mt-1 block break-all text-[10px] text-muted-foreground">
                        {header.value || t("headerTemplate.preview.emptyValue")}
                      </code>
                    </div>
                  ))}
                </div>
              )}
            </section>

            <section className="space-y-2 rounded-md border border-border/70 p-3">
              <p className="text-xs font-medium">{t("headerTemplate.warnings.title")}</p>
              {preview.warnings.length === 0 ? (
                <p className="text-[11px] text-muted-foreground">{t("headerTemplate.warnings.none")}</p>
              ) : (
                <div className="space-y-1">
                  {preview.warnings.map((warning, index) => (
                    <p key={`${warning.code}-${warning.headerName ?? ""}-${warning.token ?? ""}-${index}`} className="text-[11px] leading-4 text-amber-700 dark:text-amber-300">
                      {t(getMCPHeaderWarningMessageKey(warning.code), {
                        header: warning.headerName || t("headerTemplate.warnings.unknownHeader"),
                        token: warning.token || t("headerTemplate.warnings.unknownTokenValue"),
                      })}
                    </p>
                  ))}
                </div>
              )}
            </section>
          </div>
        </div>
      ) : null}
    </div>
  );
}
