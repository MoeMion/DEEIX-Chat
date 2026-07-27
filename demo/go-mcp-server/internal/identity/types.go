package identity

type HeaderIdentity struct {
	UserPublicID             string `json:"userPublicID"`
	UserDisplayName          string `json:"userDisplayName"`
	UserEmail                string `json:"userEmail"`
	UserRole                 string `json:"userRole"`
	ConversationPublicID     string `json:"conversationPublicID"`
	AssistantMessagePublicID string `json:"assistantMessagePublicID"`
	UserMessagePublicID      string `json:"userMessagePublicID"`
	RequestID                string `json:"requestID"`
	RunID                    string `json:"runID"`
	TraceID                  string `json:"traceID"`
}
