package conversation

import (
	"context"
	"strings"

	domainconversation "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/conversation"
	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	domainuser "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/user"
	inframcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/pkg/traceid"
)

type mcpCallConfigBuilder interface {
	BuildCallConfig(
		context.Context,
		domainmcp.Server,
		inframcp.TemplateContext,
		int,
	) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error)
}

func newMCPTemplateContext(
	ctx context.Context,
	user domainuser.User,
	conversation domainconversation.Conversation,
	userMessage domainconversation.Message,
	assistantMessage domainconversation.Message,
	requestID string,
	runID string,
) inframcp.TemplateContext {
	displayName := strings.TrimSpace(user.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(user.Username)
	}
	return inframcp.TemplateContext{
		Mode:                     inframcp.ContextModeChat,
		UserPublicID:             strings.TrimSpace(user.PublicID),
		UserDisplayName:          displayName,
		UserEmail:                strings.TrimSpace(user.Email),
		UserRole:                 strings.TrimSpace(user.Role),
		ConversationPublicID:     strings.TrimSpace(conversation.PublicID),
		AssistantMessagePublicID: strings.TrimSpace(assistantMessage.PublicID),
		UserMessagePublicID:      strings.TrimSpace(userMessage.PublicID),
		RequestID:                strings.TrimSpace(requestID),
		RunID:                    strings.TrimSpace(runID),
		TraceID:                  strings.ToLower(strings.TrimSpace(traceid.FromContext(ctx))),
	}
}
