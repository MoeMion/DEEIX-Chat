"use client";

import * as React from "react";

const CHAT_SCROLL_STORAGE_KEY = "deeix-chat:chat-scroll:v1";
const BOTTOM_THRESHOLD_PX = 96;
const SCROLL_POSITION_PERSIST_DELAY_MS = 180;
const RESTORE_RETRY_FRAMES = 3;
const USER_SCROLL_INTENT_TTL_MS = 700;
const SCROLL_INTENT_KEYS = new Set(["ArrowDown", "ArrowUp", "End", "Home", "PageDown", "PageUp", "Space"]);

type PersistedScrollEntry = {
  mode: "bottom" | "offset";
  scrollTop: number;
  updatedAt: string;
};

type PersistedScrollStore = Record<string, PersistedScrollEntry>;
type RestoreState = "idle" | "pending";

function readScrollStore(): PersistedScrollStore {
  if (typeof window === "undefined") {
    return {};
  }

  try {
    const raw = window.localStorage.getItem(CHAT_SCROLL_STORAGE_KEY);
    if (!raw) {
      return {};
    }
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object") {
      return {};
    }
    return parsed as PersistedScrollStore;
  } catch {
    return {};
  }
}

function writeScrollStore(store: PersistedScrollStore) {
  if (typeof window === "undefined") {
    return;
  }

  try {
    if (Object.keys(store).length === 0) {
      window.localStorage.removeItem(CHAT_SCROLL_STORAGE_KEY);
      return;
    }
    window.localStorage.setItem(CHAT_SCROLL_STORAGE_KEY, JSON.stringify(store));
  } catch {
    // Ignore storage write failures and keep scrolling usable.
  }
}

function readScrollEntry(conversationID: string | null): PersistedScrollEntry | null {
  const key = conversationID?.trim();
  if (!key) {
    return null;
  }
  return readScrollStore()[key] ?? null;
}

function writeScrollEntry(conversationID: string | null, entry: PersistedScrollEntry | null) {
  const key = conversationID?.trim();
  if (!key) {
    return;
  }

  const store = readScrollStore();
  if (!entry) {
    delete store[key];
  } else {
    store[key] = entry;
  }
  writeScrollStore(store);
}

export function useChatScrollController({
  conversationID,
  loading,
  isConversationMode,
  visibleMessageCount,
  latestMessageKey,
  showPendingAssistant,
  streamingText,
  streamingTraceText,
}: {
  conversationID: string | null;
  loading: boolean;
  isConversationMode: boolean;
  visibleMessageCount: number;
  latestMessageKey: string;
  showPendingAssistant: boolean;
  streamingText: string;
  streamingTraceText: string;
}) {
  const messageViewportRef = React.useRef<HTMLDivElement | null>(null);
  const messageContentRef = React.useRef<HTMLDivElement | null>(null);
  const messageEndRef = React.useRef<HTMLDivElement | null>(null);
  const autoFollowRef = React.useRef(true);
  const autoFollowFrameRef = React.useRef<number | null>(null);
  const persistTimerRef = React.useRef<number | null>(null);
  const restoreFrameRef = React.useRef<number | null>(null);
  const programmaticScrollRef = React.useRef(false);
  const restoreStateRef = React.useRef<RestoreState>("idle");
  const currentConversationIDRef = React.useRef<string | null>(conversationID);
  const handledConversationIDRef = React.useRef<string | null | undefined>(undefined);
  const liveGenerationRef = React.useRef(false);
  const wasStreamingRef = React.useRef(false);
  const userScrollIntentRef = React.useRef(false);
  const userScrollIntentTimerRef = React.useRef<number | null>(null);
  const [showScrollToLatestButton, setShowScrollToLatestButton] = React.useState(false);

  const hasLiveStreamingContent = showPendingAssistant || streamingText.length > 0 || streamingTraceText.length > 0;
  const liveContentTick = `${visibleMessageCount}:${streamingText.length}:${streamingTraceText.length}:${showPendingAssistant ? "1" : "0"}`;

  const isNearBottom = React.useCallback((viewport: HTMLDivElement) => {
    const distanceFromBottom = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight;
    return distanceFromBottom <= BOTTOM_THRESHOLD_PX;
  }, []);

  const updateScrollAffordance = React.useCallback(
    (viewport: HTMLDivElement | null) => {
      if (!viewport) {
        setShowScrollToLatestButton(false);
        return true;
      }
      const scrollable = viewport.scrollHeight - viewport.clientHeight > BOTTOM_THRESHOLD_PX;
      const nearBottom = isNearBottom(viewport);
      setShowScrollToLatestButton(scrollable && !nearBottom);
      return nearBottom;
    },
    [isNearBottom],
  );

  const clearUserScrollIntent = React.useCallback(() => {
    if (userScrollIntentTimerRef.current !== null) {
      window.clearTimeout(userScrollIntentTimerRef.current);
      userScrollIntentTimerRef.current = null;
    }
    userScrollIntentRef.current = false;
  }, []);

  const markUserScrollIntent = React.useCallback(() => {
    userScrollIntentRef.current = true;
    if (userScrollIntentTimerRef.current !== null) {
      window.clearTimeout(userScrollIntentTimerRef.current);
    }
    userScrollIntentTimerRef.current = window.setTimeout(() => {
      userScrollIntentTimerRef.current = null;
      userScrollIntentRef.current = false;
    }, USER_SCROLL_INTENT_TTL_MS);
  }, []);

  const persistViewportPosition = React.useCallback(
    (targetConversationID: string | null, viewport: HTMLDivElement | null) => {
      if (!targetConversationID || !viewport) {
        return;
      }

      writeScrollEntry(targetConversationID, {
        mode: isNearBottom(viewport) ? "bottom" : "offset",
        scrollTop: viewport.scrollTop,
        updatedAt: new Date().toISOString(),
      });
    },
    [isNearBottom],
  );

  const schedulePersistViewportPosition = React.useCallback(
    (targetConversationID: string | null) => {
      if (!targetConversationID || persistTimerRef.current !== null) {
        return;
      }

      persistTimerRef.current = window.setTimeout(() => {
        persistTimerRef.current = null;
        persistViewportPosition(targetConversationID, messageViewportRef.current);
      }, SCROLL_POSITION_PERSIST_DELAY_MS);
    },
    [persistViewportPosition],
  );

  const scrollToLatest = React.useCallback((behavior: ScrollBehavior = "auto") => {
    const viewport = messageViewportRef.current;
    if (!viewport) {
      return;
    }

    programmaticScrollRef.current = true;
    clearUserScrollIntent();
    autoFollowRef.current = true;
    const end = messageEndRef.current;
    if (end) {
      end.scrollIntoView({ block: "end", behavior });
    } else {
      viewport.scrollTo({ top: viewport.scrollHeight, behavior });
    }
    setShowScrollToLatestButton(false);

    window.requestAnimationFrame(() => {
      window.requestAnimationFrame(() => {
        programmaticScrollRef.current = false;
        updateScrollAffordance(viewport);
      });
    });
  }, [clearUserScrollIntent, updateScrollAffordance]);

  const scheduleScrollToLatest = React.useCallback(() => {
    if (!autoFollowRef.current || autoFollowFrameRef.current !== null) {
      return;
    }
    autoFollowFrameRef.current = window.requestAnimationFrame(() => {
      autoFollowFrameRef.current = null;
      if (autoFollowRef.current) {
        scrollToLatest();
      }
    });
  }, [scrollToLatest]);

  const restoreViewportPosition = React.useCallback(() => {
    const viewport = messageViewportRef.current;
    if (!viewport) {
      return false;
    }

    const entry = readScrollEntry(currentConversationIDRef.current);
    if (!entry || entry.mode === "bottom") {
      scrollToLatest();
      return true;
    }

    const maxScrollTop = Math.max(0, viewport.scrollHeight - viewport.clientHeight);
    programmaticScrollRef.current = true;
    clearUserScrollIntent();
    autoFollowRef.current = false;
    viewport.scrollTop = Math.min(entry.scrollTop, maxScrollTop);
    window.requestAnimationFrame(() => {
      programmaticScrollRef.current = false;
      updateScrollAffordance(viewport);
    });
    return true;
  }, [clearUserScrollIntent, scrollToLatest, updateScrollAffordance]);

  const onScroll = React.useCallback(() => {
    const viewport = messageViewportRef.current;
    if (!viewport || programmaticScrollRef.current || restoreStateRef.current === "pending") {
      return;
    }

    if (liveGenerationRef.current && !userScrollIntentRef.current) {
      autoFollowRef.current = true;
      updateScrollAffordance(viewport);
      scheduleScrollToLatest();
      return;
    }

    autoFollowRef.current = updateScrollAffordance(viewport);
    schedulePersistViewportPosition(currentConversationIDRef.current);
  }, [schedulePersistViewportPosition, scheduleScrollToLatest, updateScrollAffordance]);

  const onScrollToLatest = React.useCallback(() => {
    scrollToLatest("smooth");
  }, [scrollToLatest]);

  React.useLayoutEffect(() => {
    liveGenerationRef.current = hasLiveStreamingContent;
  }, [hasLiveStreamingContent]);

  React.useLayoutEffect(() => {
    if (handledConversationIDRef.current === conversationID) {
      return;
    }

    const previousConversationID = currentConversationIDRef.current;
    const viewport = messageViewportRef.current;
    handledConversationIDRef.current = conversationID;
    currentConversationIDRef.current = conversationID;
    restoreStateRef.current = Boolean(conversationID) && !liveGenerationRef.current ? "pending" : "idle";
    autoFollowRef.current = true;
    clearUserScrollIntent();
    setShowScrollToLatestButton(false);

    return () => {
      if (persistTimerRef.current !== null) {
        window.clearTimeout(persistTimerRef.current);
        persistTimerRef.current = null;
      }
      persistViewportPosition(previousConversationID, viewport);
    };
  }, [clearUserScrollIntent, conversationID, persistViewportPosition]);

  React.useLayoutEffect(() => {
    if (!hasLiveStreamingContent) {
      return;
    }
    restoreStateRef.current = "idle";
    autoFollowRef.current = true;
    scheduleScrollToLatest();
  }, [hasLiveStreamingContent, scheduleScrollToLatest]);

  React.useLayoutEffect(() => {
    if (restoreStateRef.current !== "pending" || loading) {
      return;
    }

    if (restoreFrameRef.current !== null) {
      window.cancelAnimationFrame(restoreFrameRef.current);
    }

    const restoreWhenReady = (attempt: number) => {
      restoreFrameRef.current = null;
      if (liveGenerationRef.current) {
        restoreStateRef.current = "idle";
        scrollToLatest();
        return;
      }

      const restored = restoreViewportPosition();
      if (restored || attempt >= RESTORE_RETRY_FRAMES) {
        restoreStateRef.current = "idle";
        return;
      }
      restoreFrameRef.current = window.requestAnimationFrame(() => restoreWhenReady(attempt + 1));
    };

    restoreFrameRef.current = window.requestAnimationFrame(() => restoreWhenReady(0));
    return () => {
      if (restoreFrameRef.current !== null) {
        window.cancelAnimationFrame(restoreFrameRef.current);
        restoreFrameRef.current = null;
      }
    };
  }, [loading, restoreViewportPosition, scrollToLatest, visibleMessageCount]);

  React.useLayoutEffect(() => {
    if (!isConversationMode && visibleMessageCount === 0) {
      return;
    }
    if (restoreStateRef.current === "pending" || !autoFollowRef.current) {
      return;
    }
    scheduleScrollToLatest();
  }, [isConversationMode, latestMessageKey, scheduleScrollToLatest, visibleMessageCount]);

  React.useLayoutEffect(() => {
    if (restoreStateRef.current === "pending") {
      return;
    }

    const viewport = messageViewportRef.current;
    if (!viewport) {
      setShowScrollToLatestButton(false);
      return;
    }

    if (hasLiveStreamingContent && autoFollowRef.current) {
      scheduleScrollToLatest();
      return;
    }

    if (wasStreamingRef.current && !hasLiveStreamingContent && autoFollowRef.current) {
      scheduleScrollToLatest();
    }
    wasStreamingRef.current = hasLiveStreamingContent;
    updateScrollAffordance(viewport);
  }, [hasLiveStreamingContent, liveContentTick, scheduleScrollToLatest, updateScrollAffordance]);

  React.useEffect(() => {
    const viewport = messageViewportRef.current;
    if (!viewport) {
      return;
    }

    const markKeyboardScrollIntent = (event: KeyboardEvent) => {
      if (SCROLL_INTENT_KEYS.has(event.code) || SCROLL_INTENT_KEYS.has(event.key)) {
        markUserScrollIntent();
      }
    };

    viewport.addEventListener("wheel", markUserScrollIntent, { passive: true });
    viewport.addEventListener("touchmove", markUserScrollIntent, { passive: true });
    viewport.addEventListener("pointerdown", markUserScrollIntent, { passive: true });
    viewport.addEventListener("keydown", markKeyboardScrollIntent);
    return () => {
      viewport.removeEventListener("wheel", markUserScrollIntent);
      viewport.removeEventListener("touchmove", markUserScrollIntent);
      viewport.removeEventListener("pointerdown", markUserScrollIntent);
      viewport.removeEventListener("keydown", markKeyboardScrollIntent);
    };
  }, [isConversationMode, markUserScrollIntent]);

  React.useEffect(() => {
    const content = messageContentRef.current;
    const viewport = messageViewportRef.current;
    if ((!content && !viewport) || typeof ResizeObserver === "undefined") {
      return;
    }

    const observer = new ResizeObserver(() => {
      const currentViewport = messageViewportRef.current;
      if (!currentViewport) {
        updateScrollAffordance(null);
        return;
      }
      if (restoreStateRef.current === "pending") {
        return;
      }
      if (autoFollowRef.current) {
        scheduleScrollToLatest();
        return;
      }
      updateScrollAffordance(currentViewport);
    });
    if (content) {
      observer.observe(content);
    }
    if (viewport) {
      observer.observe(viewport);
    }
    return () => observer.disconnect();
  }, [scheduleScrollToLatest, updateScrollAffordance]);

  React.useEffect(() => {
    const viewport = messageViewportRef.current;

    return () => {
      if (autoFollowFrameRef.current !== null) {
        window.cancelAnimationFrame(autoFollowFrameRef.current);
        autoFollowFrameRef.current = null;
      }
      if (persistTimerRef.current !== null) {
        window.clearTimeout(persistTimerRef.current);
        persistTimerRef.current = null;
      }
      if (restoreFrameRef.current !== null) {
        window.cancelAnimationFrame(restoreFrameRef.current);
        restoreFrameRef.current = null;
      }
      clearUserScrollIntent();
      persistViewportPosition(currentConversationIDRef.current, viewport);
    };
  }, [clearUserScrollIntent, persistViewportPosition]);

  return {
    messageViewportRef,
    messageContentRef,
    messageEndRef,
    onScroll,
    onScrollToLatest,
    scheduleScrollToLatest,
    showScrollToLatestButton,
  };
}
