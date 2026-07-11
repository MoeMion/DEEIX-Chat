"use client";

import * as React from "react";
import { useTranslations } from "next-intl";

import { Badge } from "@/components/ui/badge";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type {
  AdminMCPHeaderTemplatePreviewDTO,
  MCPHeaderTemplateMode,
  MCPHeaderTemplateWarningDTO,
} from "@/features/admin/api/mcp.types";
import { previewAdminMCPHeaderTemplate } from "@/features/admin/api/mcp";
import {
  validateHeaderTemplateJSON,
  type MCPHeaderTemplatePreflightError,
} from "@/features/admin/model/mcp-header-template";
import { resolveAdminErrorMessage } from "@/features/admin/utils/admin-error";
import { JsonCodeEditor } from "@/shared/components/json-code-editor";
import { SpinnerLabel } from "@/components/ui/spinner";

type MCPHeaderTemplateEditorProps = {
  accessToken: string;
  value: string;
  mode: MCPHeaderTemplateMode;
  disabled?: boolean;
  onChange: (value: string) => void;
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
    default:
      return "headerTemplate.tokens.unknown";
  }
}

function warningMessageKey(warning: MCPHeaderTemplateWarningDTO) {
  return warning.code === "unknown_token"
    ? "headerTemplate.warnings.unknownToken"
    : "headerTemplate.warnings.malformedToken";
}

export function MCPHeaderTemplateEditor({
  accessToken,
  value,
  mode,
  disabled = false,
  onChange,
  onModeChange,
  onValidityChange,
}: MCPHeaderTemplateEditorProps) {
  const t = useTranslations("adminTools.serverDialog");
  const [preview, setPreview] = React.useState<AdminMCPHeaderTemplatePreviewDTO | null>(null);
  const [previewedText, setPreviewedText] = React.useState("");
  const [previewError, setPreviewError] = React.useState<unknown>(null);
  const preflightError = validateHeaderTemplateJSON(value);

  React.useEffect(() => {
    setPreview(null);
    setPreviewedText("");
    setPreviewError(null);
    if (preflightError) return;

    const controller = new AbortController();
    const exactText = value;
    const timer = window.setTimeout(() => {
      void previewAdminMCPHeaderTemplate(accessToken, exactText, mode, controller.signal)
        .then((result) => {
          if (!controller.signal.aborted) {
            setPreview(result);
            setPreviewedText(exactText);
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
  }, [accessToken, mode, preflightError, value]);

  const previewIsCurrent =
    preflightError === null && preview !== null && previewedText === value && previewError === null;

  React.useEffect(() => {
    onValidityChange(previewIsCurrent);
  }, [onValidityChange, previewIsCurrent]);

  const previewPending = preflightError === null && preview === null && previewError === null;

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <p className="text-xs font-medium text-foreground">{t("headerTemplate.title")}</p>
          <p className="text-[11px] leading-4 text-muted-foreground">{t("headerTemplate.serverExpansion")}</p>
        </div>
        <Select
          value={mode}
          disabled={disabled}
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
      <p className="text-[11px] leading-4 text-muted-foreground">{t(modeHelpMessageKey(mode))}</p>

      <JsonCodeEditor
        value={value}
        disabled={disabled}
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

      <div className="space-y-1 rounded-md border border-amber-200/70 bg-amber-50/70 px-3 py-2 text-[11px] leading-4 text-amber-800 dark:border-amber-900/60 dark:bg-amber-950/20 dark:text-amber-200">
        <p>{t("headerTemplate.backendAuthority")}</p>
        <p>{t("headerTemplate.plaintextWarning")}</p>
        <p>{t("headerTemplate.blacklistWarning")}</p>
        <p>{t("headerTemplate.maskedPreservation")}</p>
      </div>

      {previewPending ? (
        <p className="text-[11px] text-muted-foreground">
          <SpinnerLabel>{t("headerTemplate.preview.loading")}</SpinnerLabel>
        </p>
      ) : null}
      {previewError ? (
        <p className="text-[11px] leading-4 text-destructive">
          {resolveAdminErrorMessage(previewError, t("headerTemplate.preview.failed"))}
        </p>
      ) : null}

      {preview ? (
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
                      {t(warningMessageKey(warning), {
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
