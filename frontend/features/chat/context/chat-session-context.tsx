"use client";

import * as React from "react";

type ChatSessionContextValue = {
  newConversationRevision: number;
  newConversationProjectID: string;
  requestNewConversation: (options?: { projectID?: string }) => void;
};

const ChatSessionContext = React.createContext<ChatSessionContextValue | null>(null);

export function ChatSessionProvider({ children }: { children: React.ReactNode }) {
  const [state, setState] = React.useState({ revision: 0, projectID: "" });
  const requestNewConversation = React.useCallback((options?: { projectID?: string }) => {
    setState((prev) => ({
      revision: prev.revision + 1,
      projectID: options?.projectID?.trim() ?? "",
    }));
  }, []);

  const value = React.useMemo(
    () => ({
      newConversationRevision: state.revision,
      newConversationProjectID: state.projectID,
      requestNewConversation,
    }),
    [requestNewConversation, state.projectID, state.revision],
  );

  return <ChatSessionContext.Provider value={value}>{children}</ChatSessionContext.Provider>;
}

export function useChatSession() {
  const context = React.useContext(ChatSessionContext);
  if (!context) {
    throw new Error("useChatSession must be used within ChatSessionProvider");
  }
  return context;
}
