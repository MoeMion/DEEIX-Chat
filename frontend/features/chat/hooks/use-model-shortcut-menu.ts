"use client";

import * as React from "react";

import type { ChatModelOption } from "@/features/chat/types/chat-runtime";

const MODEL_SHORTCUT_MENU_MAX_HEIGHT = 280;
const MODEL_SHORTCUT_MENU_MIN_HEIGHT = 32;
const MODEL_SHORTCUT_MENU_MIN_WIDTH = 232;
const MODEL_SHORTCUT_MENU_MAX_WIDTH = 420;
const MODEL_SHORTCUT_MENU_ROW_HEIGHT = 28;
const MODEL_SHORTCUT_MENU_ROW_GAP = 2;
const MODEL_SHORTCUT_MENU_CHROME_HEIGHT = 12;
const MODEL_SHORTCUT_MENU_TEXT_WIDTH_UNIT = 7;
const MODEL_SHORTCUT_MENU_CONTENT_GAP_WIDTH = 64;
const MODEL_SHORTCUT_MENU_VIEWPORT_GUTTER = 16;
const MODEL_SHORTCUT_MENU_OFFSET = 8;

type ModelShortcutMenuControllerArgs = {
  draft: string;
  modelOptions: ChatModelOption[];
  selectedPlatformModelName: string;
  disabled: boolean;
  textareaRef: React.RefObject<HTMLTextAreaElement | null>;
  onDraftChange: (value: string) => void;
  onModelChange: (platformModelName: string) => void;
};

function resolveModelShortcutQuery(value: string): string | null {
  if (!value.startsWith("@")) {
    return null;
  }
  return value.slice(1).match(/^\S*/)?.[0]?.trim().toLowerCase() ?? "";
}

function removeModelShortcutTrigger(value: string): string {
  return value.replace(/^@\S*\s?/, "");
}

function filterModelShortcutOptions(modelOptions: ChatModelOption[], query: string): ChatModelOption[] {
  const normalizedQuery = query.trim().toLowerCase();
  return normalizedQuery
    ? modelOptions.filter((model) => {
      const platformModelName = model.platformModelName.toLowerCase();
      const vendor = model.vendor.toLowerCase();
      return platformModelName.includes(normalizedQuery) || vendor.includes(normalizedQuery);
    })
    : modelOptions;
}

function resolveModelShortcutMenuWidth(modelOptions: ChatModelOption[], viewportWidth: number): number {
  const availableWidth = Math.max(0, viewportWidth - MODEL_SHORTCUT_MENU_VIEWPORT_GUTTER * 2);
  const longestLabelLength = modelOptions.reduce(
    (maxLength, model) => Math.max(maxLength, model.platformModelName.length),
    0,
  );
  const contentWidth = longestLabelLength * MODEL_SHORTCUT_MENU_TEXT_WIDTH_UNIT + MODEL_SHORTCUT_MENU_CONTENT_GAP_WIDTH;
  return Math.min(
    Math.max(contentWidth, MODEL_SHORTCUT_MENU_MIN_WIDTH),
    MODEL_SHORTCUT_MENU_MAX_WIDTH,
    availableWidth,
  );
}

function resolveModelShortcutMenuContentHeight(modelOptions: ChatModelOption[]): number {
  if (modelOptions.length === 0) {
    return MODEL_SHORTCUT_MENU_MIN_HEIGHT;
  }
  return Math.min(
    MODEL_SHORTCUT_MENU_MAX_HEIGHT,
    modelOptions.length * MODEL_SHORTCUT_MENU_ROW_HEIGHT
      + Math.max(0, modelOptions.length - 1) * MODEL_SHORTCUT_MENU_ROW_GAP
      + MODEL_SHORTCUT_MENU_CHROME_HEIGHT,
  );
}

function resolveModelShortcutMenuLayout(
  textarea: HTMLTextAreaElement,
  modelOptions: ChatModelOption[],
  viewportWidth: number,
  viewportHeight: number,
): React.CSSProperties {
  const textareaRect = textarea.getBoundingClientRect();
  const computed = window.getComputedStyle(textarea);
  const paddingLeft = Number.parseFloat(computed.paddingLeft) || 20;
  const paddingTop = Number.parseFloat(computed.paddingTop) || 16;
  const lineHeight = Number.parseFloat(computed.lineHeight) || 24;
  const preferredTop = textareaRect.top + paddingTop + lineHeight + MODEL_SHORTCUT_MENU_OFFSET;
  const preferredBottom = textareaRect.top + paddingTop - MODEL_SHORTCUT_MENU_OFFSET;
  const desiredHeight = resolveModelShortcutMenuContentHeight(modelOptions);
  const availableBelow = viewportHeight - preferredTop - MODEL_SHORTCUT_MENU_VIEWPORT_GUTTER;
  const availableAbove = preferredBottom - MODEL_SHORTCUT_MENU_VIEWPORT_GUTTER;
  const openBelow =
    availableBelow >= Math.min(desiredHeight, MODEL_SHORTCUT_MENU_MIN_HEIGHT)
    || availableBelow >= availableAbove;
  const availableHeight = Math.max(0, openBelow ? availableBelow : availableAbove);
  const maxHeight = Math.max(
    Math.min(MODEL_SHORTCUT_MENU_MIN_HEIGHT, availableHeight),
    Math.min(desiredHeight, availableHeight),
  );
  const top = openBelow
    ? preferredTop
    : Math.max(MODEL_SHORTCUT_MENU_VIEWPORT_GUTTER, preferredBottom - maxHeight);
  const preferredWidth = resolveModelShortcutMenuWidth(modelOptions, viewportWidth);
  const preferredLeft = textareaRect.left + paddingLeft - MODEL_SHORTCUT_MENU_OFFSET;
  const maxLeft = Math.max(
    MODEL_SHORTCUT_MENU_VIEWPORT_GUTTER,
    viewportWidth - preferredWidth - MODEL_SHORTCUT_MENU_VIEWPORT_GUTTER,
  );
  const left = Math.min(Math.max(preferredLeft, MODEL_SHORTCUT_MENU_VIEWPORT_GUTTER), maxLeft);
  const width = Math.min(
    preferredWidth,
    Math.max(0, viewportWidth - left - MODEL_SHORTCUT_MENU_VIEWPORT_GUTTER),
  );

  return { left, top, width, maxHeight };
}

export function useModelShortcutMenu({
  draft,
  modelOptions,
  selectedPlatformModelName,
  disabled,
  textareaRef,
  onDraftChange,
  onModelChange,
}: ModelShortcutMenuControllerArgs) {
  const menuRef = React.useRef<HTMLDivElement | null>(null);
  const menuID = React.useId();
  const [inputFocused, setInputFocused] = React.useState(false);
  const [activeIndex, setActiveIndex] = React.useState(0);
  const [dismissedDraft, setDismissedDraft] = React.useState<string | null>(null);
  const [menuStyle, setMenuStyle] = React.useState<React.CSSProperties>({});
  const query = resolveModelShortcutQuery(draft);
  const options = React.useMemo(
    () => (query === null ? [] : filterModelShortcutOptions(modelOptions, query)),
    [modelOptions, query],
  );
  const open = inputFocused && query !== null && dismissedDraft !== draft && !disabled && options.length > 0;
  const activeOption = open ? options[Math.min(activeIndex, options.length - 1)] : null;
  const selectedIndex = React.useMemo(
    () => options.findIndex((model) => model.platformModelName === selectedPlatformModelName),
    [options, selectedPlatformModelName],
  );

  React.useEffect(() => {
    setActiveIndex(selectedIndex >= 0 ? selectedIndex : 0);
  }, [query, selectedIndex]);

  React.useEffect(() => {
    setActiveIndex((current) => (options.length === 0 ? 0 : Math.min(current, options.length - 1)));
  }, [options.length]);

  React.useEffect(() => {
    if (!open) {
      return;
    }
    const activeItem = menuRef.current?.querySelector<HTMLElement>('[data-active="true"]');
    activeItem?.scrollIntoView({ block: "nearest" });
  }, [activeIndex, open]);

  const updateLayout = React.useCallback(() => {
    if (!open || typeof window === "undefined") {
      return;
    }

    const textarea = textareaRef.current;
    if (!textarea) {
      return;
    }

    setMenuStyle(resolveModelShortcutMenuLayout(textarea, options, window.innerWidth, window.innerHeight));
  }, [open, options, textareaRef]);

  React.useLayoutEffect(() => {
    if (!open) {
      return;
    }
    let frameID = window.requestAnimationFrame(updateLayout);
    const update = () => {
      window.cancelAnimationFrame(frameID);
      frameID = window.requestAnimationFrame(updateLayout);
    };
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => {
      window.cancelAnimationFrame(frameID);
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [open, updateLayout]);

  const select = React.useCallback(
    (model: ChatModelOption) => {
      onModelChange(model.platformModelName);
      onDraftChange(removeModelShortcutTrigger(draft));
      setDismissedDraft(null);
      window.requestAnimationFrame(() => {
        textareaRef.current?.focus();
      });
    },
    [draft, onDraftChange, onModelChange, textareaRef],
  );

  const handleChange = React.useCallback(
    (value: string) => {
      if (dismissedDraft !== null && value !== dismissedDraft) {
        setDismissedDraft(null);
      }
      onDraftChange(value);
    },
    [dismissedDraft, onDraftChange],
  );

  const handleKeyDown = React.useCallback(
    (event: React.KeyboardEvent<HTMLTextAreaElement>): boolean => {
      if (!open) {
        return false;
      }
      if (event.key === "ArrowDown") {
        event.preventDefault();
        setActiveIndex((current) => (current + 1) % options.length);
        return true;
      }
      if (event.key === "ArrowUp") {
        event.preventDefault();
        setActiveIndex((current) => (current - 1 + options.length) % options.length);
        return true;
      }
      if ((event.key === "Enter" || event.key === "Tab") && activeOption) {
        event.preventDefault();
        select(activeOption);
        return true;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        setDismissedDraft(draft);
        return true;
      }
      return false;
    },
    [activeOption, draft, open, options.length, select],
  );

  return {
    activeIndex,
    handleBlur: () => setInputFocused(false),
    handleChange,
    handleFocus: () => setInputFocused(true),
    handleKeyDown,
    menuID,
    menuRef,
    menuStyle,
    open,
    options,
    select,
  };
}
