package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/conversation"
	appmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/mcp"
	inframcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
)

type appLifecycleSessionManager struct {
	closeAll func(context.Context) error
}

func (*appLifecycleSessionManager) Acquire(context.Context, inframcp.AcquireInput) (inframcp.Operation, error) {
	return nil, nil
}

func (*appLifecycleSessionManager) OpenEphemeral(context.Context, inframcp.CallConfig, int) (inframcp.Operation, func(context.Context) error, error) {
	return nil, nil, nil
}

func (*appLifecycleSessionManager) CloseRun(context.Context, string, string) error { return nil }

func (m *appLifecycleSessionManager) CloseAll(ctx context.Context) error {
	if m.closeAll == nil {
		return nil
	}
	return m.closeAll(ctx)
}

func TestComposeMCPSessionServicesSharesAndStoresOneManager(t *testing.T) {
	manager := &appLifecycleSessionManager{}
	var controlPlaneManager inframcp.SessionManager
	var conversationManager inframcp.SessionManager

	composition := composeMCPSessionServices(
		manager,
		func(got inframcp.SessionManager) *appmcp.Service {
			controlPlaneManager = got
			return nil
		},
		func(got inframcp.SessionManager) *conversation.Service {
			conversationManager = got
			return nil
		},
	)
	application := &App{}
	composition.storeIn(application)

	if controlPlaneManager != manager || conversationManager != manager {
		t.Fatalf("composed managers: control=%p conversation=%p want=%p", controlPlaneManager, conversationManager, manager)
	}
	if composition.sessions != manager || application.mcpSessions != manager {
		t.Fatalf("stored managers: composition=%p app=%p want=%p", composition.sessions, application.mcpSessions, manager)
	}
}

func TestCloseAppLifecycleOrdersCleanupAndContinuesAfterMCPError(t *testing.T) {
	events := make([]string, 0, 7)
	appendEvent := func(event string) { events = append(events, event) }
	backgroundCtx, cancelBackground := context.WithCancel(context.Background())
	manager := &appLifecycleSessionManager{closeAll: func(ctx context.Context) error {
		appendEvent("mcp")
		if !errors.Is(backgroundCtx.Err(), context.Canceled) {
			t.Fatalf("background context error = %v, want canceled before CloseAll", backgroundCtx.Err())
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("CloseAll context already done: %v", err)
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("CloseAll context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining < 4*time.Second || remaining > 5*time.Second {
			t.Fatalf("CloseAll deadline remaining = %v, want approximately 5s", remaining)
		}
		return errors.New("cleanup failed")
	}}

	closeAppLifecycle(appCloseLifecycle{
		backgroundCancel: func() {
			appendEvent("cancel")
			cancelBackground()
		},
		mcpSessions:   manager,
		closeRedis:    func() { appendEvent("redis") },
		closeGeo:      func() { appendEvent("geo") },
		closeDatabase: func() { appendEvent("db") },
		shutdownTracing: func(ctx context.Context) {
			appendEvent("tracing")
			if err := ctx.Err(); err != nil {
				t.Fatalf("tracing context already done: %v", err)
			}
		},
		syncLogger: func() { appendEvent("logger") },
	})

	want := []string{"cancel", "mcp", "redis", "geo", "db", "tracing", "logger"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("close events = %#v, want %#v", events, want)
	}
}
