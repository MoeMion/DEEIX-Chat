"use client";

import * as React from "react";
import { CircleHelp } from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { Cog } from "@/components/animate-ui/icons/cog";
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
import { InputGroupButton } from "@/components/ui/input-group";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import {
  isReservedConversationOptionKey,
  sanitizeConversationOptions,
} from "@/features/chat/model/conversation-options";
import { cn } from "@/lib/utils";
import type { ConversationOptions } from "@/shared/api/conversation.types";
import { JsonCodeEditor } from "@/shared/components/json-code-editor";
import type { ModelOptionPolicy } from "@/shared/lib/model-option-policy";
import { isModelOptionPathFiltered, isNativeToolTypeAllowed } from "@/shared/lib/model-option-policy";

type EditableOptionValue = string | number | boolean | null;

type VisualOption = {
  key: string;
  path: string[];
  value: EditableOptionValue;
};

type FilteredOption = {
  key: string;
  value: unknown;
};

type NativeToolOption = {
  type: string;
  labelKey: string;
  descriptionKey: string;
  payload?: Record<string, unknown>;
};

type ModelOptionFilterStatus = "passed" | "filtered" | "unknown";

type ChatModelConfigProps = {
  disabled: boolean;
  options: ConversationOptions;
  defaultOptions: ConversationOptions;
  modelOptionPolicy: ModelOptionPolicy | null;
  selectedProtocol: string;
  selectedModelName: string;
  isMediaMode: boolean;
  onOptionsChange: React.Dispatch<React.SetStateAction<ConversationOptions>>;
  onOptionsReset: () => void;
};

const OPTION_LABEL_KEYS = new Set<string>([
  "budget_tokens",
  "cache_timeout",
  "candidate_count",
  "effort",
  "enable_cache",
  "enable_thinking",
  "frequency_penalty",
  "generationConfig.candidateCount",
  "generationConfig.frequencyPenalty",
  "generationConfig.imageConfig.aspectRatio",
  "generationConfig.imageConfig.imageSize",
  "generationConfig.logprobs",
  "generationConfig.maxOutputTokens",
  "generationConfig.mediaResolution",
  "generationConfig.presencePenalty",
  "generationConfig.responseLogprobs",
  "generationConfig.responseMimeType",
  "generationConfig.seed",
  "generationConfig.thinkingConfig.includeThoughts",
  "generationConfig.thinkingConfig.thinkingBudget",
  "generationConfig.thinkingConfig.thinkingLevel",
  "generationConfig.topK",
  "logprobs",
  "max_completion_tokens",
  "max_output_tokens",
  "max_tokens",
  "n",
  "output_config.effort",
  "output_config.format.type",
  "responseFormat.image.aspectRatio",
  "responseFormat.image.imageSize",
  "resolution",
  "presence_penalty",
  "reasoning.summary",
  "reasoning.effort",
  "reasoning_effort",
  "reasoning_summary",
  "response_format",
  "response_format.type",
  "response_logprobs",
  "seed",
  "service_tier",
  "speed",
  "temperature",
  "think",
  "thinking",
  "thinking_display",
  "thinking.budget_tokens",
  "thinking.display",
  "thinking.includeThoughts",
  "thinking.include_thoughts",
  "thinking.thinkingBudget",
  "thinking.thinkingLevel",
  "thinking.thinking_budget",
  "thinking.thinking_level",
  "thinking.type",
  "thinkingConfig.includeThoughts",
  "thinkingConfig.thinkingBudget",
  "thinkingConfig.thinkingLevel",
  "tool_config.functionCallingConfig.mode",
  "toolConfig.functionCallingConfig.mode",
  "tool_choice.type",
  "tool_choice",
  "top_k",
  "top_p",
  "verbosity",
  "web_search",
  "aspect_ratio",
  "aspectRatio",
  "image_size",
  "imageSize",
  "imageConfig.aspectRatio",
  "imageConfig.imageSize",
] as const);

const XAI_NATIVE_TOOL_OPTIONS: NativeToolOption[] = [
  {
    type: "web_search",
    labelKey: "webSearch",
    descriptionKey: "grokWebSearch",
  },
  {
    type: "x_search",
    labelKey: "xSearch",
    descriptionKey: "grokXSearch",
  },
  {
    type: "code_interpreter",
    labelKey: "codeInterpreter",
    descriptionKey: "grokCodeInterpreter",
  },
];

const OPENAI_NATIVE_TOOL_OPTIONS: NativeToolOption[] = [
  {
    type: "web_search",
    labelKey: "webSearch",
    descriptionKey: "openaiWebSearch",
    payload: { type: "web_search" },
  },
  {
    type: "shell",
    labelKey: "shell",
    descriptionKey: "openaiShell",
    payload: {
      type: "shell",
      environment: { type: "container_auto" },
    },
  },
  {
    type: "image_generation",
    labelKey: "imageGeneration",
    descriptionKey: "openaiImageGeneration",
    payload: { type: "image_generation" },
  },
  {
    type: "code_interpreter",
    labelKey: "codeInterpreter",
    descriptionKey: "openaiCodeInterpreter",
    payload: {
      type: "code_interpreter",
      container: { type: "auto" },
    },
  },
];

const ANTHROPIC_NATIVE_TOOL_OPTIONS: NativeToolOption[] = [
  {
    type: "web_search_20260209",
    labelKey: "webSearch",
    descriptionKey: "claudeWebSearch",
    payload: { type: "web_search_20260209", name: "web_search", allowed_callers: ["direct"] },
  },
  {
    type: "web_fetch_20260209",
    labelKey: "webFetch",
    descriptionKey: "claudeWebFetch",
    payload: { type: "web_fetch_20260209", name: "web_fetch", allowed_callers: ["direct"] },
  },
  {
    type: "code_execution_20260120",
    labelKey: "codeExecution",
    descriptionKey: "claudeCodeExecution",
    payload: { type: "code_execution_20260120", name: "code_execution" },
  },
  {
    type: "advisor_20260301",
    labelKey: "advisor",
    descriptionKey: "claudeAdvisor",
    payload: { type: "advisor_20260301", name: "advisor", model: "claude-opus-4-7" },
  },
  {
    type: "tool_search_tool_regex_20251119",
    labelKey: "toolSearchRegex",
    descriptionKey: "claudeToolSearchRegex",
    payload: { type: "tool_search_tool_regex_20251119", name: "tool_search_tool_regex" },
  },
  {
    type: "tool_search_tool_bm25_20251119",
    labelKey: "toolSearchBm25",
    descriptionKey: "claudeToolSearchBm25",
    payload: { type: "tool_search_tool_bm25_20251119", name: "tool_search_tool_bm25" },
  },
];

const NATIVE_TOOL_OPTIONS = [...XAI_NATIVE_TOOL_OPTIONS, ...OPENAI_NATIVE_TOOL_OPTIONS, ...ANTHROPIC_NATIVE_TOOL_OPTIONS];
const NATIVE_TOOL_TYPES = new Set(NATIVE_TOOL_OPTIONS.map((item) => item.type));

const OPTION_ORDER = [
  "temperature",
  "top_p",
  "top_k",
  "generationConfig.topK",
  "candidate_count",
  "generationConfig.candidateCount",
  "seed",
  "generationConfig.seed",
  "presence_penalty",
  "generationConfig.presencePenalty",
  "frequency_penalty",
  "generationConfig.frequencyPenalty",
  "generationConfig.imageConfig.aspectRatio",
  "generationConfig.imageConfig.imageSize",
  "response_logprobs",
  "generationConfig.responseLogprobs",
  "logprobs",
  "generationConfig.logprobs",
  "generationConfig.responseMimeType",
  "generationConfig.mediaResolution",
  "service_tier",
  "max_tokens",
  "speed",
  "enable_thinking",
  "thinking_display",
  "effort",
  "enable_cache",
  "cache_timeout",
  "thinking.type",
  "thinking.include_thoughts",
  "thinking.includeThoughts",
  "reasoning_effort",
  "reasoning.effort",
  "reasoning.summary",
  "reasoning_summary",
  "output_config.effort",
  "output_config.format.type",
  "response_format",
  "response_format.type",
  "responseFormat.image.aspectRatio",
  "responseFormat.image.imageSize",
  "resolution",
  "budget_tokens",
  "thinking.budget_tokens",
  "thinking.thinking_budget",
  "thinking.thinkingBudget",
  "thinking.thinking_level",
  "thinking.thinkingLevel",
  "thinking.display",
  "thinkingConfig.includeThoughts",
  "thinkingConfig.thinkingBudget",
  "thinkingConfig.thinkingLevel",
  "generationConfig.thinkingConfig.includeThoughts",
  "generationConfig.thinkingConfig.thinkingBudget",
  "generationConfig.thinkingConfig.thinkingLevel",
  "max_output_tokens",
  "max_completion_tokens",
  "generationConfig.maxOutputTokens",
  "verbosity",
  "tool_config.functionCallingConfig.mode",
  "toolConfig.functionCallingConfig.mode",
  "tool_choice.type",
  "tool_choice",
  "thinking",
  "think",
  "web_search",
  "aspect_ratio",
  "aspectRatio",
  "image_size",
  "imageSize",
  "imageConfig.aspectRatio",
  "imageConfig.imageSize",
];

const NUMBER_OPTION_KEYS = new Set([
  "budget_tokens",
  "candidate_count",
  "frequency_penalty",
  "generationConfig.candidateCount",
  "generationConfig.frequencyPenalty",
  "generationConfig.logprobs",
  "generationConfig.maxOutputTokens",
  "generationConfig.presencePenalty",
  "generationConfig.seed",
  "generationConfig.thinkingConfig.thinkingBudget",
  "generationConfig.topK",
  "logprobs",
  "max_completion_tokens",
  "max_output_tokens",
  "max_tokens",
  "n",
  "presence_penalty",
  "seed",
  "temperature",
  "thinking.budget_tokens",
  "thinking.thinking_budget",
  "thinking.thinkingBudget",
  "thinkingConfig.thinkingBudget",
  "top_k",
  "top_p",
]);

const OPTION_SELECT_VALUES: Record<string, string[]> = {
  cache_timeout: ["5m", "1h"],
  effort: ["low", "medium", "high", "xhigh", "max"],
  service_tier: ["default", "priority", "flex"],
  speed: ["fast"],
  "generationConfig.mediaResolution": ["MEDIA_RESOLUTION_UNSPECIFIED", "MEDIA_RESOLUTION_LOW", "MEDIA_RESOLUTION_MEDIUM", "MEDIA_RESOLUTION_HIGH"],
  "generationConfig.responseMimeType": ["text/plain", "application/json", "text/x.enum"],
  "generationConfig.imageConfig.aspectRatio": ["1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"],
  "generationConfig.imageConfig.imageSize": ["1K", "2K", "4K"],
  "generationConfig.thinkingConfig.thinkingLevel": ["low", "medium", "high"],
  "imageConfig.aspectRatio": ["1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"],
  "imageConfig.imageSize": ["1K", "2K", "4K"],
  "output_config.effort": ["low", "medium", "high"],
  "output_config.format.type": ["json_object", "json_schema", "text"],
  "reasoning.effort": ["low", "medium", "high"],
  "reasoning.summary": ["auto", "concise", "detailed"],
  reasoning_effort: ["minimal", "low", "medium", "high", "xhigh"],
  reasoning_summary: ["auto", "concise", "detailed"],
  response_format: ["url", "b64_json"],
  "response_format.type": ["json_object", "json_schema", "text"],
  "responseFormat.image.aspectRatio": ["1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"],
  "responseFormat.image.imageSize": ["1K", "2K", "4K"],
  resolution: ["1k", "2k"],
  aspect_ratio: ["1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"],
  aspectRatio: ["1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"],
  image_size: ["1K", "2K", "4K"],
  imageSize: ["1K", "2K", "4K"],
  "thinking.display": ["summarized", "omitted"],
  "thinking.thinking_level": ["low", "medium", "high"],
  "thinking.thinkingLevel": ["low", "medium", "high"],
  "thinking.type": ["enabled", "adaptive", "disabled"],
  "thinkingConfig.thinkingLevel": ["low", "medium", "high"],
  "tool_config.functionCallingConfig.mode": ["AUTO", "ANY", "NONE"],
  "toolConfig.functionCallingConfig.mode": ["AUTO", "ANY", "NONE"],
  "tool_choice.type": ["auto", "any", "none"],
  verbosity: ["low", "medium", "high"],
};

const NESTED_VISUAL_OPTION_PATHS = [
  ["thinking", "type"],
  ["thinking", "budget_tokens"],
  ["thinking", "include_thoughts"],
  ["thinking", "thinking_budget"],
  ["thinking", "thinking_level"],
  ["thinking", "includeThoughts"],
  ["thinking", "thinkingBudget"],
  ["thinking", "thinkingLevel"],
  ["thinking", "display"],
  ["thinkingConfig", "includeThoughts"],
  ["thinkingConfig", "thinkingBudget"],
  ["thinkingConfig", "thinkingLevel"],
  ["reasoning", "effort"],
  ["reasoning", "summary"],
  ["output_config", "effort"],
  ["output_config", "format", "type"],
  ["response_format", "type"],
  ["tool_choice", "type"],
  ["generationConfig", "maxOutputTokens"],
  ["generationConfig", "temperature"],
  ["generationConfig", "topP"],
  ["generationConfig", "topK"],
  ["generationConfig", "candidateCount"],
  ["generationConfig", "seed"],
  ["generationConfig", "presencePenalty"],
  ["generationConfig", "frequencyPenalty"],
  ["generationConfig", "responseLogprobs"],
  ["generationConfig", "logprobs"],
  ["generationConfig", "responseMimeType"],
  ["generationConfig", "mediaResolution"],
  ["generationConfig", "imageConfig", "aspectRatio"],
  ["generationConfig", "imageConfig", "imageSize"],
  ["imageConfig", "aspectRatio"],
  ["imageConfig", "imageSize"],
  ["responseFormat", "image", "aspectRatio"],
  ["responseFormat", "image", "imageSize"],
  ["generationConfig", "thinkingConfig", "includeThoughts"],
  ["generationConfig", "thinkingConfig", "thinkingBudget"],
  ["generationConfig", "thinkingConfig", "thinkingLevel"],
  ["tool_config", "functionCallingConfig", "mode"],
  ["toolConfig", "functionCallingConfig", "mode"],
];

const PROTOCOL_LABELS: Record<string, string> = {
  anthropic_messages: "Messages",
  fal_queue: "Queue",
  google_generate_content: "Generate Content",
  google_image_generation: "Image Generation",
  openai_chat_completions: "Chat Completions",
  openai_image_edits: "Images Edits",
  openai_image_generations: "Images Generations",
  openai_responses: "Responses",
  openai_video_generations: "Video Generations",
  replicate_predictions: "Predictions",
  stability_ai_generate: "Image Generation",
  xai_image: "Images Generations",
  xai_image_edits: "Images Edits",
  xai_responses: "xAI Responses",
};

function stringifyOptions(value: ConversationOptions): string {
  if (Object.keys(value).length === 0) {
    return "";
  }
  return JSON.stringify(value, null, 2);
}

function parseOptionsDraft(value: string): {
  filteredOptions: FilteredOption[];
  options: ConversationOptions | null;
  rawOptions: ConversationOptions | null;
  error: string;
} {
  try {
    const parsed = JSON.parse(value.trim() || "{}") as unknown;
    if (parsed === null || Array.isArray(parsed) || typeof parsed !== "object") {
      return { filteredOptions: [], options: null, rawOptions: null, error: "JSON must be an object" };
    }
    const rawOptions = parsed as ConversationOptions;
    return {
      filteredOptions: Object.entries(rawOptions)
        .filter(([key]) => isReservedConversationOptionKey(key))
        .map(([key, optionValue]) => ({ key, value: optionValue })),
      options: sanitizeConversationOptions(rawOptions),
      rawOptions,
      error: "",
    };
  } catch {
    return { filteredOptions: [], options: null, rawOptions: null, error: "" };
  }
}

function isEditableOptionValue(value: unknown): value is EditableOptionValue {
  return value === null || ["string", "number", "boolean"].includes(typeof value);
}

function isPlainOptionObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function providerToolObjectsFromOptions(options: ConversationOptions): Record<string, unknown>[] {
  const rawTools = options.tools;
  if (!Array.isArray(rawTools)) {
    return [];
  }
  return rawTools.filter(isPlainOptionObject);
}

function hasProviderTool(options: ConversationOptions, type: string): boolean {
  return providerToolObjectsFromOptions(options).some((tool) => tool.type === type);
}

function setProviderToolEnabled(
  options: ConversationOptions,
  toolOption: NativeToolOption,
  enabled: boolean,
): ConversationOptions {
  const type = toolOption.type;
  if (!NATIVE_TOOL_TYPES.has(type)) {
    return options;
  }
  const tools = providerToolObjectsFromOptions(options);
  const hasTool = tools.some((tool) => tool.type === type);
  const nextTools = enabled
    ? hasTool
      ? tools
      : [...tools, { ...(toolOption.payload ?? { type }) }]
    : tools.filter((tool) => tool.type !== type);

  if (nextTools.length === 0) {
    const { tools: _tools, ...rest } = options;
    return rest;
  }

  return { ...options, tools: nextTools };
}

function optionPathKey(path: string[]): string {
  return path.join(".");
}

function getOptionAtPath(options: ConversationOptions, path: string[]): unknown {
  let current: unknown = options;
  for (const segment of path) {
    if (!isPlainOptionObject(current)) {
      return undefined;
    }
    current = current[segment];
  }
  return current;
}

function setOptionAtPath(options: ConversationOptions, path: string[], value: unknown): ConversationOptions {
  if (path.length === 0) {
    return options;
  }
  const [segment, ...rest] = path;
  if (rest.length === 0) {
    return { ...options, [segment]: value };
  }
  const current = options[segment];
  return {
    ...options,
    [segment]: setOptionAtPath(isPlainOptionObject(current) ? current : {}, rest, value),
  };
}

function visualOptionsFromOptions(options: ConversationOptions): VisualOption[] {
  const nestedOptions = NESTED_VISUAL_OPTION_PATHS.flatMap((path): VisualOption[] => {
    if (isReservedConversationOptionKey(path[0] ?? "")) {
      return [];
    }
    const value = getOptionAtPath(options, path);
    if (!isEditableOptionValue(value)) {
      return [];
    }
    return [{ key: optionPathKey(path), path, value }];
  });
  const topLevelOptions = Object.entries(options).flatMap(([key, value]): VisualOption[] => {
    if (isReservedConversationOptionKey(key)) {
      return [];
    }
    if (isEditableOptionValue(value)) {
      return [{ key, path: [key], value }];
    }
    return [];
  });
  const merged = [...nestedOptions, ...topLevelOptions];
  const deduped = merged.filter((item, index) => merged.findIndex((candidate) => candidate.key === item.key) === index);
  return deduped.sort((left, right) => compareOptionKeys(left.key, right.key));
}

function resolveOptionTitle(key: string, translate: (key: string) => string): string {
  if (OPTION_LABEL_KEYS.has(key)) {
    try {
      return translate(key.replaceAll(".", "__"));
    } catch {
      return key;
    }
  }
  return key;
}

function compareOptionKeys(a: string, b: string): number {
  const aIndex = OPTION_ORDER.indexOf(a);
  const bIndex = OPTION_ORDER.indexOf(b);
  if (aIndex >= 0 && bIndex >= 0) {
    return aIndex - bIndex;
  }
  if (aIndex >= 0) {
    return -1;
  }
  if (bIndex >= 0) {
    return 1;
  }
  return a.localeCompare(b);
}

function resolveOptionKind(key: string, value: EditableOptionValue): "boolean" | "number" | "select" | "text" {
  if (typeof value === "boolean") {
    return "boolean";
  }
  const selectValues = OPTION_SELECT_VALUES[key] ?? [];
  if (typeof value === "string" && selectValues.includes(value.trim())) {
    return "select";
  }
  if (typeof value === "number" || (value === null && NUMBER_OPTION_KEYS.has(key))) {
    return "number";
  }
  return "text";
}

function resolveSelectValues(key: string): string[] {
  return Array.from(new Set((OPTION_SELECT_VALUES[key] ?? []).map((item) => item.trim()).filter(Boolean)));
}

function resolveModelOptionFilterStatus(
  policy: ModelOptionPolicy | null,
  protocol: string,
  path: string,
): ModelOptionFilterStatus {
  if (!policy) {
    return "unknown";
  }
  return isModelOptionPathFiltered({ policy, protocol, path }) ? "filtered" : "passed";
}

function ModelOptionFilterBadge({
  status,
  ignoredLabel,
  passedLabel,
}: {
  status: ModelOptionFilterStatus;
  ignoredLabel: string;
  passedLabel: string;
}) {
  if (status === "unknown") {
    return null;
  }
  return (
    <span
      data-filtered={status === "filtered"}
      className="shrink-0 rounded-md bg-emerald-500/10 px-1.5 py-0.5 text-[10px] leading-none text-emerald-700 data-[filtered=true]:bg-muted data-[filtered=true]:text-muted-foreground"
    >
      {status === "filtered" ? ignoredLabel : passedLabel}
    </span>
  );
}

function parseVisualNumberInput(value: string): number | string | null {
  const normalized = value.trim();
  if (!normalized) {
    return null;
  }
  if (/^[+-]?(?:\d+|\d*\.\d+)(?:e[+-]?\d+)?$/i.test(normalized)) {
    return Number(normalized);
  }
  return value;
}

function resolveProtocolLabel(protocol: string): string {
  return PROTOCOL_LABELS[protocol] ?? protocol;
}

export function ChatModelConfig({
  disabled,
  options,
  defaultOptions,
  modelOptionPolicy,
  selectedProtocol,
  selectedModelName,
  isMediaMode,
  onOptionsChange,
  onOptionsReset,
}: ChatModelConfigProps) {
  const tCommon = useTranslations("common.actions");
  const tComposer = useTranslations("chat.composer");
  const tOptionLabels = useTranslations("chat.optionLabels");
  const tNativeToolLabels = useTranslations("chat.nativeToolLabels");
  const tNativeToolDescriptions = useTranslations("chat.nativeToolDescriptions");
  const [hovered, setHovered] = React.useState(false);
  const [dialogOpen, setDialogOpen] = React.useState(false);
  const [optionsDraft, setOptionsDraft] = React.useState("");
  const [optionsObject, setOptionsObject] = React.useState<ConversationOptions>({});
  const [filteredOptions, setFilteredOptions] = React.useState<FilteredOption[]>([]);
  const [mobileView, setMobileView] = React.useState<"json" | "visual">("json");
  const optionsObjectRef = React.useRef<ConversationOptions>({});
  const selectedProtocolLabel = selectedProtocol ? resolveProtocolLabel(selectedProtocol) : "";
  const editableOptions = React.useMemo(() => visualOptionsFromOptions(optionsObject), [optionsObject]);
  const nativeToolGroup = React.useMemo(() => {
    if (isMediaMode) {
      return null;
    }
    if (selectedProtocol === "xai_responses") {
      return {
        title: tComposer("nativeTools.grok"),
        options: XAI_NATIVE_TOOL_OPTIONS,
      };
    }
    if (selectedProtocol === "openai_responses") {
      return {
        title: tComposer("nativeTools.openai"),
        options: OPENAI_NATIVE_TOOL_OPTIONS,
      };
    }
    if (selectedProtocol === "anthropic_messages") {
      return {
        title: tComposer("nativeTools.claude"),
        options: ANTHROPIC_NATIVE_TOOL_OPTIONS,
      };
    }
    return null;
  }, [isMediaMode, selectedProtocol, tComposer]);
  const hasRecognizedOptions = Boolean(nativeToolGroup) || editableOptions.length > 0 || filteredOptions.length > 0;

  React.useEffect(() => {
    optionsObjectRef.current = optionsObject;
  }, [optionsObject]);

  const openOptionsDialog = React.useCallback(() => {
    const sanitized = sanitizeConversationOptions(options);
    optionsObjectRef.current = sanitized;
    setOptionsObject(sanitized);
    setFilteredOptions([]);
    setOptionsDraft(stringifyOptions(sanitized));
    setMobileView("json");
    setDialogOpen(true);
  }, [options]);

  const replaceOptionsDraft = React.useCallback((next: ConversationOptions) => {
    const sanitized = sanitizeConversationOptions(next);
    optionsObjectRef.current = sanitized;
    setOptionsObject(sanitized);
    setFilteredOptions([]);
    setOptionsDraft(stringifyOptions(sanitized));
  }, []);

  const replaceRawOptionsDraft = React.useCallback((next: ConversationOptions) => {
    const parsed = parseOptionsDraft(stringifyOptions(next));
    const nextOptions = parsed.rawOptions ?? next;
    optionsObjectRef.current = nextOptions;
    setOptionsObject(nextOptions);
    setFilteredOptions(parsed.filteredOptions);
    setOptionsDraft(stringifyOptions(next));
  }, []);

  const updateOptionValue = React.useCallback(
    (path: string[], value: unknown) => {
      replaceRawOptionsDraft(setOptionAtPath(optionsObjectRef.current, path, value));
    },
    [replaceRawOptionsDraft],
  );

  const updateProviderTool = React.useCallback(
    (tool: NativeToolOption, enabled: boolean) => {
      replaceRawOptionsDraft(setProviderToolEnabled(optionsObjectRef.current, tool, enabled));
    },
    [replaceRawOptionsDraft],
  );

  const handleOptionsJSONChange = React.useCallback((value: string) => {
    setOptionsDraft(value);

    const parsed = parseOptionsDraft(value);
    if (parsed.rawOptions && parsed.options) {
      optionsObjectRef.current = parsed.rawOptions;
      setOptionsObject(parsed.rawOptions);
      setFilteredOptions(parsed.filteredOptions);
    }
  }, []);

  const saveOptionsDraft = React.useCallback(() => {
    const parsed = parseOptionsDraft(optionsDraft);
    if (!parsed.options) {
      setMobileView("json");
      toast.error(tComposer("saveFailed"));
      return;
    }
    if (JSON.stringify(parsed.options) === JSON.stringify(defaultOptions)) {
      onOptionsReset();
      setDialogOpen(false);
      return;
    }
    onOptionsChange(parsed.options);
    setDialogOpen(false);
  }, [defaultOptions, onOptionsChange, onOptionsReset, optionsDraft, tComposer]);

  const renderOptionsViewToggle = () => (
    <Tabs
      value={mobileView}
      onValueChange={(value) => setMobileView(value as "json" | "visual")}
      className="w-fit gap-0"
    >
      <TabsList className="h-7">
        <TabsTrigger value="json">JSON</TabsTrigger>
        <TabsTrigger value="visual">{tComposer("visual")}</TabsTrigger>
      </TabsList>
    </Tabs>
  );

  const renderOptionsEditor = () => (
    <div className="space-y-1.5">
      <div className="flex min-w-0 items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <p className="shrink-0 text-xs text-muted-foreground">{tComposer("jsonConfig")}</p>
        </div>
        {selectedProtocolLabel ? (
          <span
            className="max-w-[70%] shrink-0 truncate rounded-md bg-muted px-1.5 py-0.5 font-mono text-[11px] leading-none text-muted-foreground"
            title={selectedProtocol}
          >
            {selectedProtocolLabel}
          </span>
        ) : null}
      </div>
      <div className="p-0.5">
        <JsonCodeEditor
          value={optionsDraft}
          onChange={handleOptionsJSONChange}
          height="min(52dvh, 420px)"
          className="min-h-56"
          actions={
            <>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-6 px-2 text-[11px]"
                onClick={() => replaceOptionsDraft({})}
              >
                {tComposer("clear")}
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-6 px-2 text-[11px]"
                onClick={() => replaceOptionsDraft(defaultOptions)}
              >
                {tComposer("default")}
              </Button>
            </>
          }
          placeholder={`{
  "temperature": 0.7,
  "thinking": {
    "type": "enabled"
  },
  "reasoning": {
    "effort": "high"
  }
}`}
        />
      </div>
    </div>
  );

  const renderOptionsVisualFields = () => (
    <div className="flex min-h-0 flex-col space-y-1.5 md:border-l md:pl-5">
      <div className="flex min-h-6 items-center justify-between gap-2 px-2 py-0.5">
        <p className="shrink-0 text-xs text-muted-foreground">{tComposer("visualConfig")}</p>
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              className="inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
              aria-label={tComposer("ignoredHelp")}
            >
              <CircleHelp className="size-3.5" />
            </button>
          </TooltipTrigger>
          <TooltipContent side="left" align="end" className="max-w-64">
            <p>{tComposer("ignoredHelp")}</p>
          </TooltipContent>
        </Tooltip>
      </div>
      {hasRecognizedOptions ? (
        <div className="min-h-0 flex-1 overflow-y-auto pr-1 md:h-[min(52dvh,420px)] md:max-h-[min(52dvh,420px)]">
          <div className="space-y-2 md:space-y-2.5">
            {nativeToolGroup ? (
              <div className="space-y-1.5 px-2 py-1.5">
                <div className="min-w-0">
                  <p className="truncate text-xs text-foreground/80">{nativeToolGroup.title}</p>
                </div>
                <div className="space-y-1">
                  {nativeToolGroup.options.map((tool) => {
                    const checked = hasProviderTool(optionsObject, tool.type);
                    const allowed = isNativeToolTypeAllowed(modelOptionPolicy, selectedProtocol, tool.type);
                    const filterStatus: ModelOptionFilterStatus = allowed ? "passed" : "filtered";
                    return (
                      <label
                        key={tool.type}
                        className={cn(
                          "flex min-w-0 items-center gap-2 rounded-md px-2 py-1.5",
                          allowed || checked ? "cursor-pointer hover:bg-muted/50" : "cursor-not-allowed text-muted-foreground",
                        )}
                      >
                        <Checkbox
                          checked={checked}
                          disabled={!allowed && !checked}
                          onCheckedChange={(nextChecked) => {
                            if (!allowed && nextChecked === true) {
                              return;
                            }
                            updateProviderTool(tool, nextChecked === true);
                          }}
                        />
                        <span className="min-w-0 flex flex-1 items-center gap-2 text-xs">
                          <span className={cn("shrink-0 text-foreground/80", !allowed && "text-muted-foreground line-through")}>
                            {tNativeToolLabels(tool.labelKey)}
                          </span>
                          <span className={cn("min-w-0 truncate text-[11px] text-muted-foreground", !allowed && "line-through")}>
                            {tNativeToolDescriptions(tool.descriptionKey)}
                          </span>
                        </span>
                        <ModelOptionFilterBadge
                          status={filterStatus}
                          ignoredLabel={tComposer("ignored")}
                          passedLabel={tComposer("willPass")}
                        />
                      </label>
                    );
                  })}
                </div>
              </div>
            ) : null}
            {editableOptions.map(({ key, path, value }) => {
              const kind = resolveOptionKind(key, value);
              const selectValues = resolveSelectValues(key);
              const title = resolveOptionTitle(key, tOptionLabels);
              const filterStatus = resolveModelOptionFilterStatus(modelOptionPolicy, selectedProtocol, key);
              const ignored = filterStatus === "filtered";

              return (
                <div
                  key={key}
                  className={cn(
                    "grid grid-cols-[minmax(0,1fr)_116px] items-center gap-2 rounded-md px-2 py-1.5 sm:grid-cols-[minmax(0,1fr)_132px] sm:gap-3 md:grid-cols-[minmax(0,1fr)_148px]",
                    ignored && "text-muted-foreground",
                  )}
                >
                  <div className="min-w-0">
                    <div className="flex min-w-0 items-center gap-1.5">
                      <p
                        className={cn(
                          "min-w-0 truncate text-xs text-foreground/80",
                          ignored && "text-muted-foreground line-through",
                        )}
                      >
                        {title}
                      </p>
                      <ModelOptionFilterBadge
                        status={filterStatus}
                        ignoredLabel={tComposer("ignored")}
                        passedLabel={tComposer("willPass")}
                      />
                    </div>
                    {title !== key ? (
                      <p
                        className={cn("truncate text-[11px] text-muted-foreground", ignored && "line-through")}
                      >
                        {key}
                      </p>
                    ) : null}
                  </div>
                  {kind === "boolean" ? (
                    <Select
                      value={value === true ? "true" : "false"}
                      onValueChange={(nextValue) => updateOptionValue(path, nextValue === "true")}
                    >
                      <SelectTrigger size="sm">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="true">{tComposer("booleanOn")}</SelectItem>
                        <SelectItem value="false">{tComposer("booleanOff")}</SelectItem>
                      </SelectContent>
                    </Select>
                  ) : kind === "select" ? (
                    <Select value={String(value)} onValueChange={(nextValue) => updateOptionValue(path, nextValue)}>
                      <SelectTrigger size="sm">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {selectValues.map((item) => (
                          <SelectItem key={item} value={item}>
                            {item}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  ) : (
                    <Input
                      value={value === null ? "" : String(value)}
                      inputMode={kind === "number" ? "decimal" : undefined}
                      placeholder={kind === "number" ? "0.7" : key}
                      onChange={(event) => {
                        const nextValue = event.target.value;
                        if (kind === "number") {
                          updateOptionValue(path, parseVisualNumberInput(nextValue));
                          return;
                        }
                        if (NUMBER_OPTION_KEYS.has(key)) {
                          updateOptionValue(path, parseVisualNumberInput(nextValue));
                          return;
                        }
                        updateOptionValue(path, nextValue);
                      }}
                    />
                  )}
                </div>
              );
            })}
            {filteredOptions.length > 0 ? (
              <div className={cn("space-y-2", editableOptions.length > 0 ? "pt-1" : "")}>
                {filteredOptions.map((item) => (
                  <div
                    key={item.key}
                    className="grid grid-cols-[minmax(0,1fr)_116px] items-center gap-2 rounded-md px-2 py-1.5 text-muted-foreground sm:grid-cols-[minmax(0,1fr)_132px] sm:gap-3 md:grid-cols-[minmax(0,1fr)_148px]"
                  >
                    <div className="min-w-0">
                      <p className="truncate text-xs line-through">{item.key}</p>
                    </div>
                    <span className="w-fit text-[11px] sm:justify-self-end">
                      {tComposer("ignored")}
                    </span>
                  </div>
                ))}
              </div>
            ) : null}
          </div>
        </div>
      ) : (
        <div className="flex h-40 min-h-0 flex-1 items-center justify-center text-xs text-muted-foreground md:h-[min(52dvh,420px)]">
          {tComposer("noVisualFields")}
        </div>
      )}
    </div>
  );

  return (
    <>
      <InputGroupButton
        type="button"
        variant="ghost"
        size="icon-sm"
        className="rounded-md text-muted-foreground hover:text-foreground"
        disabled={disabled}
        onClick={openOptionsDialog}
        aria-label={tComposer("modelOptions")}
        title={tComposer("modelOptions")}
        onMouseEnter={() => setHovered(true)}
        onMouseLeave={() => setHovered(false)}
      >
        <Cog
          size={20}
          strokeWidth={1.4}
          animate={hovered ? "default" : undefined}
        />
      </InputGroupButton>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent
          className="flex max-h-[calc(100dvh-1rem)] w-[calc(100vw-1rem)] flex-col overflow-hidden p-4 sm:max-h-[min(92vh,760px)] sm:w-full sm:max-w-[800px] sm:p-6 md:max-w-[900px]"
        >
          <DialogHeader className="shrink-0">
            <div className="flex min-w-0 items-center justify-between gap-3">
              <DialogTitle className="shrink-0">{tComposer("modelOptions")}</DialogTitle>
              <div className="shrink-0 md:hidden">{renderOptionsViewToggle()}</div>
            </div>
            <DialogDescription className="hidden md:block">
              {tComposer("dialogDescription", { model: selectedModelName || tComposer("currentModel") })}
            </DialogDescription>
          </DialogHeader>

          <form
            className="flex min-h-0 flex-1 flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault();
              saveOptionsDraft();
            }}
          >
            <div className="min-h-0 flex-1 overflow-y-auto sm:pr-1">
              <div className="space-y-3 md:hidden">
                {mobileView === "json" ? renderOptionsEditor() : renderOptionsVisualFields()}
              </div>

              <div className="hidden min-w-0 gap-4 md:grid md:grid-cols-[minmax(0,430px)_minmax(300px,1fr)] md:gap-5">
                {renderOptionsEditor()}
                {renderOptionsVisualFields()}
              </div>
            </div>
            <DialogFooter className="shrink-0">
              <Button type="button" variant="ghost" onClick={() => setDialogOpen(false)}>
                {tCommon("cancel")}
              </Button>
              <Button type="submit">
                {tCommon("save")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
