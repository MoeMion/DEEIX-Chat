package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	model "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/conversation"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type runFinalizationRecorder struct {
	repository.ConversationRepository
	events []string
}

func (r *runFinalizationRecorder) UpdateMessageState(context.Context, uint, string, string, string) error {
	r.events = append(r.events, "message")
	return nil
}

func (r *runFinalizationRecorder) CreateConversationRun(context.Context, *model.Run) error {
	r.events = append(r.events, "run")
	return nil
}

type finalizationSessionManager struct {
	events        *[]string
	closeUserID   string
	closeRunID    string
	closeRunCalls int
	closeErr      error
}

func (*finalizationSessionManager) Acquire(context.Context, mcp.AcquireInput) (mcp.Operation, error) {
	return nil, nil
}

func (*finalizationSessionManager) OpenEphemeral(context.Context, mcp.CallConfig, int) (mcp.Operation, func(context.Context) error, error) {
	return nil, nil, nil
}

func (m *finalizationSessionManager) CloseRun(_ context.Context, userPublicID string, runID string) error {
	m.closeRunCalls++
	m.closeUserID = userPublicID
	m.closeRunID = runID
	*m.events = append(*m.events, "cleanup")
	return m.closeErr
}

func (*finalizationSessionManager) CloseAll(context.Context) error { return nil }

func TestMessageSendRunStateFinalizesLocallyBeforeClosingMCPRun(t *testing.T) {
	tests := []struct {
		name   string
		ctx    func() context.Context
		retErr error
	}{
		{name: "success", ctx: context.Background},
		{name: "ordinary error", ctx: context.Background, retErr: errors.New("generation failed")},
		{name: "canceled", ctx: canceledContext, retErr: ErrMessageGenerationCanceled},
		{name: "deadline exceeded", ctx: expiredContext, retErr: context.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &runFinalizationRecorder{}
			manager := &finalizationSessionManager{events: &repo.events, closeErr: errors.New("cleanup failed")}
			service := &Service{repo: repo, mcpSessions: manager, logger: zap.NewNop()}
			state := &messageSendRunState{
				service:   service,
				run:       &model.Run{RunID: "run-public", UserID: 901234},
				startedAt: time.Now(),
			}
			userMessage := &model.Message{ID: 1}
			assistantMessage := &model.Message{ID: 2}
			state.bind(&userMessage, &assistantMessage, nil, nil, test.ctx())
			state.bindMCPContext(mcp.TemplateContext{
				Mode:         mcp.ContextModeChat,
				UserPublicID: "user-public",
				RunID:        "run-public",
			})

			state.finalize(test.ctx(), test.retErr)

			if len(repo.events) < 2 || repo.events[len(repo.events)-2] != "run" || repo.events[len(repo.events)-1] != "cleanup" {
				t.Fatalf("finalization events = %#v, want local run before cleanup", repo.events)
			}
			if manager.closeRunCalls != 1 || manager.closeUserID != "user-public" || manager.closeRunID != "run-public" {
				t.Fatalf("CloseRun calls=%d user=%q run=%q", manager.closeRunCalls, manager.closeUserID, manager.closeRunID)
			}
			if manager.closeUserID == "901234" {
				t.Fatal("CloseRun reconstructed a public identity from the numeric user ID")
			}
			if strings.Contains(state.run.ErrorMessage, "cleanup failed") {
				t.Fatalf("cleanup error replaced primary run error: %q", state.run.ErrorMessage)
			}
			if test.retErr != nil && !strings.Contains(state.run.ErrorMessage, test.retErr.Error()) {
				t.Fatalf("run error = %q, want primary %q", state.run.ErrorMessage, test.retErr.Error())
			}
		})
	}
}

func TestMessageSendRunStateWithoutBoundMCPContextDoesNotCloseRun(t *testing.T) {
	events := []string{}
	manager := &finalizationSessionManager{events: &events}
	repo := &runFinalizationRecorder{events: events}
	service := &Service{repo: repo, mcpSessions: manager, logger: zap.NewNop()}
	state := &messageSendRunState{
		service:   service,
		run:       &model.Run{RunID: "run-public", UserID: 901234},
		startedAt: time.Now(),
	}
	state.finalize(context.Background(), nil)
	if manager.closeRunCalls != 0 {
		t.Fatalf("CloseRun calls = %d, want 0", manager.closeRunCalls)
	}
}

type orderedLifecycleRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *orderedLifecycleRecorder) append(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *orderedLifecycleRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type orderedLifecycleRepo struct {
	repository.ConversationRepository
	recorder *orderedLifecycleRecorder
}

func (r orderedLifecycleRepo) UpdateMessageState(context.Context, uint, string, string, string) error {
	r.recorder.append("message")
	return nil
}

func (r orderedLifecycleRepo) CreateConversationRun(context.Context, *model.Run) error {
	r.recorder.append("run")
	return nil
}

func TestMessageSendRunStateDeletesEachOpenedServerOnceAndSkipsNeverOpened(t *testing.T) {
	recorder := &orderedLifecycleRecorder{}
	var initialized atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodDelete {
			recorder.append("delete:" + request.Header.Get("MCP-Session-Id"))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var envelope struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			t.Errorf("decode MCP request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch envelope.Method {
		case "initialize":
			sessionID := fmt.Sprintf("session-%d", initialized.Add(1))
			w.Header().Set("MCP-Session-Id", sessionID)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      envelope.ID,
				"result": map[string]any{
					"protocolVersion": "2025-11-25",
					"capabilities":    map[string]any{},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      envelope.ID,
				"result":  map[string]any{"tools": []any{}},
			})
		default:
			t.Errorf("unexpected MCP method %q", envelope.Method)
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	manager := mcp.NewSessionManager(mcp.NewClient())
	contextSnapshot := mcp.TemplateContext{
		Mode:         mcp.ContextModeChat,
		UserPublicID: "user-public",
		RunID:        "run-public",
	}
	operations := make([]mcp.Operation, 0, 3)
	for serverID := uint(1); serverID <= 3; serverID++ {
		operation, err := manager.Acquire(t.Context(), mcp.AcquireInput{
			ServerID:        serverID,
			ServerUpdatedAt: time.Date(2026, time.July, 12, int(serverID), 0, 0, 0, time.UTC),
			CallConfig: mcp.CallConfig{
				BaseURL:   server.URL,
				TimeoutMS: 1000,
				Context:   contextSnapshot,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		operations = append(operations, operation)
	}
	for _, operation := range operations[:2] {
		if _, err := operation.ListTools(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	repo := orderedLifecycleRepo{recorder: recorder}
	service := &Service{repo: repo, mcpSessions: manager, logger: zap.NewNop()}
	state := &messageSendRunState{
		service:   service,
		run:       &model.Run{RunID: "run-public", UserID: 901234},
		startedAt: time.Now(),
	}
	userMessage := &model.Message{ID: 1}
	assistantMessage := &model.Message{ID: 2}
	state.bind(&userMessage, &assistantMessage, nil, nil, t.Context())
	state.bindMCPContext(contextSnapshot)
	state.finalize(t.Context(), nil)

	events := recorder.snapshot()
	if len(events) != 4 || events[0] != "message" || events[1] != "run" {
		t.Fatalf("lifecycle events = %#v, want local finalization then two DELETEs", events)
	}
	deletes := map[string]int{}
	for _, event := range events[2:] {
		deletes[event]++
	}
	if initialized.Load() != 2 || deletes["delete:session-1"] != 1 || deletes["delete:session-2"] != 1 || len(deletes) != 2 {
		t.Fatalf("initialized=%d deletes=%#v, want two opened sessions exactly once", initialized.Load(), deletes)
	}
}

func TestMessageSendRunStateLogsOnlySafeCleanupErrorClass(t *testing.T) {
	const secret = "cleanup bearer-secret admin@example.test"
	core, logs := observer.New(zap.DebugLevel)
	repo := &runFinalizationRecorder{}
	manager := &finalizationSessionManager{events: &repo.events, closeErr: errors.New(secret)}
	service := &Service{repo: repo, mcpSessions: manager, logger: zap.New(core)}
	state := &messageSendRunState{service: service, run: &model.Run{RunID: "run-public"}, startedAt: time.Now()}
	state.bindMCPContext(mcp.TemplateContext{UserPublicID: "user-public"})

	state.finalize(t.Context(), errors.New("primary generation failed"))

	entries := logs.AllUntimed()
	if len(entries) != 1 {
		t.Fatalf("cleanup logs = %#v, want one", entries)
	}
	logText := entries[0].Message + fmt.Sprint(entries[0].ContextMap())
	if strings.Contains(logText, secret) || strings.Contains(logText, "bearer-secret") || strings.Contains(logText, "admin@example.test") {
		t.Fatalf("cleanup log leaked secret: %q", logText)
	}
	if !strings.Contains(logText, mcp.SafeErrorSummary(errors.New(secret))) {
		t.Fatalf("cleanup log = %q, want safe error class", logText)
	}
}

func TestMessageSendRunStateCleanupFailureIsNilLoggerSafe(t *testing.T) {
	events := []string{}
	manager := &finalizationSessionManager{events: &events, closeErr: errors.New("cleanup failed")}
	service := &Service{repo: &runFinalizationRecorder{}, mcpSessions: manager}
	state := &messageSendRunState{service: service, run: &model.Run{RunID: "run-public"}, startedAt: time.Now()}
	state.bindMCPContext(mcp.TemplateContext{UserPublicID: "user-public"})
	state.finalize(t.Context(), nil)
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expiredContext() context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()
	return ctx
}
