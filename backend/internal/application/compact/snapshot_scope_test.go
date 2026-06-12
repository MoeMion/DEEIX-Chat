package compact

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domainconversation "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/conversation"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
)

func TestSnapshotBoundaryIndexRequiresCoverageAnchors(t *testing.T) {
	messages := []domainconversation.Message{{ID: 1, PublicID: "m1", Role: "user"}}
	snapshot := &domainconversation.ContextSnapshot{
		SummaryText: "summary",
	}

	if _, ok := SnapshotBoundaryIndex(messages, snapshot); ok {
		t.Fatal("expected legacy snapshot without anchors to be rejected")
	}
}

func TestSnapshotBoundaryIndexAcceptsMatchingBranchPrefix(t *testing.T) {
	firstID := uint(1)
	messages := []domainconversation.Message{
		{ID: firstID, PublicID: "m1", Role: "user"},
		{ID: 2, PublicID: "m2", Role: "assistant", ParentMessageID: &firstID},
		{ID: 3, PublicID: "m3", Role: "user", ParentMessageID: uintPtr(2)},
	}
	covered := messages[:2]
	snapshot := &domainconversation.ContextSnapshot{
		SummaryText:           "summary",
		CoveredUntilMessageID: 2,
		CoveredUntilPublicID:  "m2",
		CoveredMessageCount:   len(covered),
		CoveragePathHash:      CoveragePathHash(covered),
	}

	index, ok := SnapshotBoundaryIndex(messages, snapshot)
	if !ok {
		t.Fatal("expected snapshot to match branch prefix")
	}
	if index != 1 {
		t.Fatalf("expected boundary index 1, got %d", index)
	}
}

func TestSnapshotBoundaryIndexRejectsDifferentBranchPath(t *testing.T) {
	firstID := uint(1)
	messages := []domainconversation.Message{
		{ID: firstID, PublicID: "m1", Role: "user"},
		{ID: 2, PublicID: "m2", Role: "assistant", ParentMessageID: &firstID},
	}
	otherParent := uint(99)
	otherCovered := []domainconversation.Message{
		{ID: firstID, PublicID: "m1", Role: "user"},
		{ID: 2, PublicID: "m2", Role: "assistant", ParentMessageID: &otherParent},
	}
	snapshot := &domainconversation.ContextSnapshot{
		SummaryText:           "summary",
		CoveredUntilMessageID: 2,
		CoveredUntilPublicID:  "m2",
		CoveredMessageCount:   len(otherCovered),
		CoveragePathHash:      CoveragePathHash(otherCovered),
	}

	if _, ok := SnapshotBoundaryIndex(messages, snapshot); ok {
		t.Fatal("expected snapshot from different branch path to be rejected")
	}
}

func TestSnapshotBoundaryAncestorIndexAcceptsPartialAncestorPath(t *testing.T) {
	firstID := uint(1)
	fullPath := []domainconversation.Message{
		{ID: firstID, PublicID: "m1", Role: "user"},
		{ID: 2, PublicID: "m2", Role: "assistant", ParentMessageID: &firstID},
		{ID: 3, PublicID: "m3", Role: "user", ParentMessageID: uintPtr(2)},
		{ID: 4, PublicID: "m4", Role: "assistant", ParentMessageID: uintPtr(3)},
	}
	snapshot := &domainconversation.ContextSnapshot{
		SummaryText:           "summary",
		CoveredUntilMessageID: 2,
		CoveredUntilPublicID:  "m2",
		CoveredMessageCount:   2,
		CoveragePathHash:      CoveragePathHash(fullPath[:2]),
	}

	index, ok := SnapshotBoundaryAncestorIndex(fullPath[1:], snapshot)
	if !ok {
		t.Fatal("expected partial ancestor path to match snapshot boundary")
	}
	if index != 0 {
		t.Fatalf("expected boundary at partial path start, got %d", index)
	}
}

func TestExtendCoveragePathHashMatchesFullPath(t *testing.T) {
	firstID := uint(1)
	fullPath := []domainconversation.Message{
		{ID: firstID, PublicID: "m1", Role: "user"},
		{ID: 2, PublicID: "m2", Role: "assistant", ParentMessageID: &firstID},
		{ID: 3, PublicID: "m3", Role: "user", ParentMessageID: uintPtr(2)},
		{ID: 4, PublicID: "m4", Role: "assistant", ParentMessageID: uintPtr(3)},
	}

	prefixHash := CoveragePathHash(fullPath[:2])
	extendedHash := ExtendCoveragePathHash(prefixHash, fullPath[2:])

	if extendedHash != CoveragePathHash(fullPath) {
		t.Fatalf("expected extended hash to match full path hash")
	}
}

func TestSplitMessagesByPreservedTurns(t *testing.T) {
	messages := []domainconversation.Message{
		{ID: 1, Role: "user"},
		{ID: 2, Role: "assistant"},
		{ID: 3, Role: "user"},
		{ID: 4, Role: "assistant"},
		{ID: 5, Role: "user"},
		{ID: 6, Role: "assistant"},
	}

	covered, retained := splitMessagesByPreservedTurns(messages, 1)

	if len(covered) != 4 {
		t.Fatalf("expected 4 covered messages, got %d", len(covered))
	}
	if len(retained) != 2 {
		t.Fatalf("expected 2 retained messages, got %d", len(retained))
	}
	if retained[0].ID != 5 {
		t.Fatalf("expected retained segment to start at newest user turn, got %d", retained[0].ID)
	}
}

func TestMaybeCompactConversationRollsForwardExistingSnapshot(t *testing.T) {
	messages := []domainconversation.Message{
		{ID: 1, PublicID: "m1", Role: "user", Content: "old user"},
		{ID: 2, PublicID: "m2", ParentMessageID: uintPtr(1), Role: "assistant", Content: "old assistant"},
		{ID: 3, PublicID: "m3", ParentMessageID: uintPtr(2), Role: "user", Content: "new user"},
		{ID: 4, PublicID: "m4", ParentMessageID: uintPtr(3), Role: "assistant", Content: "new assistant"},
		{ID: 5, PublicID: "m5", ParentMessageID: uintPtr(4), Role: "user", Content: "latest user"},
		{ID: 6, PublicID: "m6", ParentMessageID: uintPtr(5), Role: "assistant", Content: "latest assistant"},
	}
	repo := &compactRepositoryStub{
		latest: &domainconversation.ContextSnapshot{
			SummaryText:           "previous summary",
			CoveredUntilMessageID: 2,
			CoveredUntilPublicID:  "m2",
			CoveredMessageCount:   2,
			CoveragePathHash:      CoveragePathHash(messages[:2]),
		},
	}
	svc := NewService(config.Config{
		ContextCompactEnabled:           true,
		ContextMaxTurns:                 2,
		ContextCompactPreserve:          1,
		ContextCompactHighlightsPerRole: 4,
		ContextCompactSnippetChars:      200,
	}, repo, nil)

	snapshot, err := svc.MaybeCompactConversation(t.Context(), MaybeCompactConversationInput{
		ConversationID: 9,
		UserID:         7,
		RunID:          "run_1",
		Messages:       messages,
	})
	if err != nil {
		t.Fatalf("expected compaction to succeed, got %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected new snapshot")
	}
	if repo.created == nil {
		t.Fatal("expected snapshot to be persisted")
	}
	if snapshot.CoveredUntilMessageID != 4 || snapshot.CoveredMessageCount != 4 {
		t.Fatalf("expected snapshot to cover first four messages, got %#v", snapshot)
	}
	if !strings.Contains(snapshot.SummaryText, "previous summary") {
		t.Fatalf("expected previous summary to be carried forward, got %q", snapshot.SummaryText)
	}
	if strings.Contains(snapshot.SummaryText, "old user") || strings.Contains(snapshot.SummaryText, "old assistant") {
		t.Fatalf("expected already-covered messages to stay out of new summary source, got %q", snapshot.SummaryText)
	}
	if !strings.Contains(snapshot.SummaryText, "new user") || !strings.Contains(snapshot.SummaryText, "new assistant") {
		t.Fatalf("expected newly covered messages in summary, got %q", snapshot.SummaryText)
	}
	if snapshot.CoveragePathHash != CoveragePathHash(messages[:4]) {
		t.Fatal("expected rolled snapshot hash to match the full covered path")
	}
}

func TestMaybeCompactConversationRollsForwardFromPartialBoundaryWindow(t *testing.T) {
	fullPath := []domainconversation.Message{
		{ID: 1, PublicID: "m1", Role: "user", Content: "old user"},
		{ID: 2, PublicID: "m2", ParentMessageID: uintPtr(1), Role: "assistant", Content: "old assistant"},
		{ID: 3, PublicID: "m3", ParentMessageID: uintPtr(2), Role: "user", Content: "new user"},
		{ID: 4, PublicID: "m4", ParentMessageID: uintPtr(3), Role: "assistant", Content: "new assistant"},
		{ID: 5, PublicID: "m5", ParentMessageID: uintPtr(4), Role: "user", Content: "latest user"},
		{ID: 6, PublicID: "m6", ParentMessageID: uintPtr(5), Role: "assistant", Content: "latest assistant"},
	}
	repo := &compactRepositoryStub{
		latest: &domainconversation.ContextSnapshot{
			SummaryText:           "previous summary",
			ToTurn:                1,
			CoveredUntilMessageID: 2,
			CoveredUntilPublicID:  "m2",
			CoveredMessageCount:   2,
			CoveragePathHash:      CoveragePathHash(fullPath[:2]),
		},
	}
	svc := NewService(config.Config{
		ContextCompactEnabled:           true,
		ContextMaxTurns:                 1,
		ContextCompactPreserve:          1,
		ContextCompactHighlightsPerRole: 4,
		ContextCompactSnippetChars:      200,
	}, repo, nil)

	partialWindow := fullPath[1:]
	snapshot, err := svc.MaybeCompactConversation(t.Context(), MaybeCompactConversationInput{
		ConversationID: 9,
		UserID:         7,
		RunID:          "run_partial",
		Messages:       partialWindow,
	})
	if err != nil {
		t.Fatalf("expected compaction to succeed, got %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected new snapshot")
	}
	if snapshot.CoveredUntilMessageID != 4 || snapshot.CoveredMessageCount != 4 {
		t.Fatalf("expected snapshot to roll forward to message 4, got %#v", snapshot)
	}
	if snapshot.CoveragePathHash != CoveragePathHash(fullPath[:4]) {
		t.Fatal("expected partial-window snapshot hash to match full covered path")
	}
	if !strings.Contains(snapshot.SummaryText, "previous summary") {
		t.Fatalf("expected previous summary to be carried forward, got %q", snapshot.SummaryText)
	}
	if strings.Contains(snapshot.SummaryText, "old user") || strings.Contains(snapshot.SummaryText, "old assistant") {
		t.Fatalf("expected already-covered messages to stay out of new summary source, got %q", snapshot.SummaryText)
	}
	if !strings.Contains(snapshot.SummaryText, "new user") || !strings.Contains(snapshot.SummaryText, "new assistant") {
		t.Fatalf("expected newly covered messages in summary, got %q", snapshot.SummaryText)
	}
}

func TestMaybeCompactConversationUsesPromptTokenEstimateForTokenTrigger(t *testing.T) {
	messages := []domainconversation.Message{
		{ID: 1, PublicID: "m1", Role: "user", Content: "small user"},
		{ID: 2, PublicID: "m2", ParentMessageID: uintPtr(1), Role: "assistant", Content: "small assistant"},
		{ID: 3, PublicID: "m3", ParentMessageID: uintPtr(2), Role: "user", Content: "latest user"},
		{ID: 4, PublicID: "m4", ParentMessageID: uintPtr(3), Role: "assistant", Content: "latest assistant"},
	}
	repo := &compactRepositoryStub{}
	svc := NewService(config.Config{
		ContextCompactEnabled:           true,
		ContextMaxTurns:                 0,
		ContextCompactTrigger:           100,
		ContextCompactPreserve:          1,
		ContextCompactHighlightsPerRole: 4,
		ContextCompactSnippetChars:      200,
	}, repo, nil)

	snapshot, err := svc.MaybeCompactConversation(t.Context(), MaybeCompactConversationInput{
		ConversationID:      9,
		UserID:              7,
		RunID:               "run_prompt_tokens",
		Messages:            messages,
		PromptTokenEstimate: 200,
	})
	if err != nil {
		t.Fatalf("expected compaction to succeed, got %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected prompt token estimate to trigger compaction")
	}
	if snapshot.Strategy != "token_cap" {
		t.Fatalf("expected token_cap strategy, got %q", snapshot.Strategy)
	}
}

func TestMaybeCompactConversationKeepsFullHistoryWhenPreserveCoversAllTurns(t *testing.T) {
	messages := []domainconversation.Message{
		{ID: 1, PublicID: "m1", Role: "user", Content: "small user"},
		{ID: 2, PublicID: "m2", ParentMessageID: uintPtr(1), Role: "assistant", Content: "small assistant"},
		{ID: 3, PublicID: "m3", ParentMessageID: uintPtr(2), Role: "user", Content: "latest user"},
		{ID: 4, PublicID: "m4", ParentMessageID: uintPtr(3), Role: "assistant", Content: "latest assistant"},
	}
	repo := &compactRepositoryStub{}
	svc := NewService(config.Config{
		ContextCompactEnabled:  true,
		ContextCompactTrigger:  100,
		ContextCompactPreserve: 8,
	}, repo, nil)

	snapshot, err := svc.MaybeCompactConversation(t.Context(), MaybeCompactConversationInput{
		ConversationID:      9,
		UserID:              7,
		RunID:               "run_preserve_all",
		Messages:            messages,
		PromptTokenEstimate: 1000,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if snapshot != nil {
		t.Fatalf("expected no compaction when preserve window covers all turns, got %#v", snapshot)
	}
	if repo.created != nil {
		t.Fatalf("expected no snapshot persisted, got %#v", repo.created)
	}
}

func TestBuildCompactionSummaryLiteFallbackKeepsPreviousSummary(t *testing.T) {
	svc := NewService(config.Config{CompactLLMEnabled: true}, nil, nil)
	var calls [][]domainconversation.Message
	svc.SetLLMSummarizer(func(ctx context.Context, platformModelName string, messages []domainconversation.Message, prompt string) (string, error) {
		cloned := append([]domainconversation.Message(nil), messages...)
		calls = append(calls, cloned)
		if len(calls) == 1 {
			return "", errors.New("force full summary fallback")
		}
		return "merged summary", nil
	})

	summary := svc.buildCompactionSummary(
		t.Context(),
		[]domainconversation.Message{
			{ID: 3, Role: "user", Content: "new user"},
			{ID: 4, Role: "assistant", Content: "new assistant"},
		},
		"previous summary",
		"turn_cap",
		1,
		2,
		1,
		"model",
	)

	if summary != "merged summary" {
		t.Fatalf("expected lite LLM summary, got %q", summary)
	}
	if len(calls) != 2 {
		t.Fatalf("expected full and lite summarizer calls, got %d", len(calls))
	}
	if len(calls[1]) == 0 || !strings.Contains(calls[1][0].Content, "previous summary") {
		t.Fatalf("expected lite fallback to keep previous summary, got %#v", calls[1])
	}
}

func uintPtr(value uint) *uint {
	return &value
}

type compactRepositoryStub struct {
	latest      *domainconversation.ContextSnapshot
	created     *domainconversation.ContextSnapshot
	compactedAt time.Time
}

func (r *compactRepositoryStub) CreateContextSnapshot(ctx context.Context, item *domainconversation.ContextSnapshot) error {
	if item == nil {
		return repository.ErrInvalidInput
	}
	cloned := *item
	cloned.ID = 99
	r.created = &cloned
	*item = cloned
	return nil
}

func (r *compactRepositoryStub) GetContextSnapshotByRunID(ctx context.Context, runID string) (*domainconversation.ContextSnapshot, error) {
	if r.created != nil && r.created.RunID == runID {
		return r.created, nil
	}
	return nil, repository.ErrNotFound
}

func (r *compactRepositoryStub) GetLatestContextSnapshot(ctx context.Context, conversationID uint) (*domainconversation.ContextSnapshot, error) {
	if r.latest == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.latest
	return &item, nil
}

func (r *compactRepositoryStub) UpdateConversationCompactedAt(ctx context.Context, conversationID uint, compactedAt time.Time) error {
	r.compactedAt = compactedAt
	return nil
}
