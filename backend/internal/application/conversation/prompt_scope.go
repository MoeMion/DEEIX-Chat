package conversation

import (
	appcompact "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/compact"
	model "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/conversation"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/llm"
)

type promptScope struct {
	FullBranchMessages []model.Message
	CoveredMessages    []model.Message
	RetainedMessages   []model.Message
	Snapshot           *model.ContextSnapshot
	CoveredUntilID     uint
	retainedMessageIDs map[uint]struct{}
}

func buildPromptScope(messages []model.Message, snapshot *model.ContextSnapshot, policy contextCompactionPolicy) promptScope {
	scope := promptScope{
		FullBranchMessages: append([]model.Message(nil), messages...),
		RetainedMessages:   append([]model.Message(nil), messages...),
	}
	if !policy.EffectiveEnabled() {
		return scope
	}
	boundaryIndex, ok := appcompact.SnapshotBoundaryIndex(messages, snapshot)
	if !ok {
		boundaryIndex, ok = appcompact.SnapshotBoundaryAncestorIndex(messages, snapshot)
	}
	if !ok || boundaryIndex+1 >= len(messages) {
		return scope
	}
	scope.Snapshot = snapshot
	scope.CoveredMessages = append([]model.Message(nil), messages[:boundaryIndex+1]...)
	scope.RetainedMessages = append([]model.Message(nil), messages[boundaryIndex+1:]...)
	scope.CoveredUntilID = snapshot.CoveredUntilMessageID
	scope.retainedMessageIDs = messageIDSet(scope.RetainedMessages)
	return scope
}

func (s promptScope) activeMessages() []model.Message {
	if len(s.RetainedMessages) > 0 {
		return s.RetainedMessages
	}
	return s.FullBranchMessages
}

func (s promptScope) filterRecallChunks(chunks []model.MessageChunk) []model.MessageChunk {
	if len(chunks) == 0 || s.CoveredUntilID == 0 {
		return chunks
	}
	result := make([]model.MessageChunk, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk.MessageID > 0 && chunk.MessageID <= s.CoveredUntilID {
			continue
		}
		if len(s.retainedMessageIDs) > 0 && chunk.MessageID > 0 {
			if _, ok := s.retainedMessageIDs[chunk.MessageID]; !ok {
				continue
			}
		}
		result = append(result, chunk)
	}
	return result
}

func (s promptScope) retainedMessageIDSet() map[uint]struct{} {
	if len(s.retainedMessageIDs) == 0 {
		return nil
	}
	result := make(map[uint]struct{}, len(s.retainedMessageIDs))
	for id := range s.retainedMessageIDs {
		result[id] = struct{}{}
	}
	return result
}

func messageIDSet(messages []model.Message) map[uint]struct{} {
	if len(messages) == 0 {
		return nil
	}
	result := make(map[uint]struct{}, len(messages))
	for _, message := range messages {
		if message.ID == 0 {
			continue
		}
		result[message.ID] = struct{}{}
	}
	return result
}

func historyMessagesFromDomain(messages []model.Message) []llm.Message {
	historyMsgs := make([]llm.Message, 0, len(messages))
	for _, item := range messages {
		if item.Role != "user" && item.Role != "assistant" && item.Role != "system" {
			continue
		}
		historyMsgs = append(historyMsgs, llm.Message{
			Role:    item.Role,
			Content: item.Content,
		})
	}
	return historyMsgs
}
