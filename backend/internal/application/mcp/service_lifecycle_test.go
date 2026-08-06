package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	domainuser "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/user"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	inframcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
)

type lifecycleOperation struct {
	tools      []inframcp.Tool
	err        error
	respectCtx bool
	calls      int
}

func (o *lifecycleOperation) ListTools(ctx context.Context) ([]inframcp.Tool, error) {
	o.calls++
	if o.respectCtx && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return append([]inframcp.Tool(nil), o.tools...), o.err
}

func (*lifecycleOperation) CallTool(context.Context, inframcp.CallInput) (string, error) {
	return "", nil
}

type lifecycleSessionManager struct {
	operation      inframcp.Operation
	openConfigs    []inframcp.CallConfig
	openBudgets    []int
	closeCalls     int
	closeCtxErrors []error
	closeDeadlines []time.Time
}

func (*lifecycleSessionManager) Acquire(context.Context, inframcp.AcquireInput) (inframcp.Operation, error) {
	return nil, nil
}

func (m *lifecycleSessionManager) OpenEphemeral(
	_ context.Context,
	cfg inframcp.CallConfig,
	retryCount int,
) (inframcp.Operation, func(context.Context) error, error) {
	m.openConfigs = append(m.openConfigs, cfg)
	m.openBudgets = append(m.openBudgets, retryCount)
	return m.operation, func(ctx context.Context) error {
		m.closeCalls++
		m.closeCtxErrors = append(m.closeCtxErrors, ctx.Err())
		deadline, ok := ctx.Deadline()
		if !ok {
			m.closeDeadlines = append(m.closeDeadlines, time.Time{})
		} else {
			m.closeDeadlines = append(m.closeDeadlines, deadline)
		}
		return nil
	}, nil
}

func (*lifecycleSessionManager) CloseRun(context.Context, string, string) error { return nil }
func (*lifecycleSessionManager) CloseAll(context.Context) error                 { return nil }

type lifecycleMCPRepo struct {
	repository.MCPRepository
	server   domainmcp.Server
	replaced int
	tools    []domainmcp.Tool
}

func (r *lifecycleMCPRepo) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	server := r.server
	return &server, nil
}

func (r *lifecycleMCPRepo) ReplaceServerTools(_ context.Context, _ uint, tools []domainmcp.Tool, _ bool) error {
	r.replaced++
	r.tools = append([]domainmcp.Tool(nil), tools...)
	return nil
}

func (r *lifecycleMCPRepo) ListTools(context.Context, uint, bool) ([]domainmcp.Tool, error) {
	return append([]domainmcp.Tool(nil), r.tools...), nil
}

func (r *lifecycleMCPRepo) UpdateServer(context.Context, uint, repository.UpdateMCPServerInput) (*domainmcp.Server, error) {
	server := r.server
	return &server, nil
}

func TestServiceProbeAndSyncAlwaysCloseEphemeralOperation(t *testing.T) {
	remoteErr := errors.New("remote failed")
	for _, mode := range []inframcp.ContextMode{inframcp.ContextModeProbe, inframcp.ContextModeSync} {
		for _, outcome := range []struct {
			name       string
			operation  func() *lifecycleOperation
			newContext func() context.Context
		}{
			{
				name: "success",
				operation: func() *lifecycleOperation {
					return &lifecycleOperation{tools: []inframcp.Tool{{Name: "memory.list"}}}
				},
				newContext: context.Background,
			},
			{
				name:       "operation error",
				operation:  func() *lifecycleOperation { return &lifecycleOperation{err: remoteErr} },
				newContext: context.Background,
			},
			{
				name: "canceled parent",
				operation: func() *lifecycleOperation {
					return &lifecycleOperation{respectCtx: true}
				},
				newContext: canceledLifecycleContext,
			},
		} {
			t.Run(string(mode)+"/"+outcome.name, func(t *testing.T) {
				repo := &lifecycleMCPRepo{server: domainmcp.Server{
					ID: 9, BaseURL: "https://mcp.example.test/mcp", HeadersJSON: `{}`, HeadersEnabled: true,
					Status: "active", ContextJWTMode: "none",
				}}
				operation := outcome.operation()
				manager := &lifecycleSessionManager{operation: operation}
				service := NewServiceWithRuntime(
					config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}),
					repo,
					manager,
				)
				service.SetUserProfileResolver(userProfileResolverStub{user: domainuser.User{
					PublicID: "actor-public", Username: "actor", Role: domainuser.RoleAdmin,
				}})

				ctx := outcome.newContext()
				if mode == inframcp.ContextModeProbe {
					_, _ = service.ProbeServer(ctx, ProbeServerInput{ServerID: 9, ActorUserID: 3, RequestID: "probe-request"})
				} else {
					_, _ = service.SyncServerTools(ctx, SyncServerToolsInput{ServerID: 9, RequestID: "sync-request"})
				}

				if len(manager.openConfigs) != 1 || len(manager.openBudgets) != 1 || manager.openBudgets[0] != 0 {
					t.Fatalf("OpenEphemeral configs=%d budgets=%#v", len(manager.openConfigs), manager.openBudgets)
				}
				if manager.openConfigs[0].Context.Mode != mode {
					t.Fatalf("context mode = %q, want %q", manager.openConfigs[0].Context.Mode, mode)
				}
				if operation.calls != 1 {
					t.Fatalf("Operation.ListTools calls = %d, want 1", operation.calls)
				}
				if manager.closeCalls != 1 || len(manager.closeCtxErrors) != 1 || manager.closeCtxErrors[0] != nil {
					t.Fatalf("close calls=%d ctx errors=%#v", manager.closeCalls, manager.closeCtxErrors)
				}
				if len(manager.closeDeadlines) != 1 || manager.closeDeadlines[0].IsZero() {
					t.Fatalf("close deadlines = %#v, want bounded deadline", manager.closeDeadlines)
				}
				remaining := time.Until(manager.closeDeadlines[0])
				if remaining <= 0 || remaining > 5*time.Second {
					t.Fatalf("cleanup deadline remaining = %v, want (0, 5s]", remaining)
				}
			})
		}
	}
}

func TestServiceProbeClosesEphemeralHandleWhenOperationIsMissing(t *testing.T) {
	repo := &lifecycleMCPRepo{server: domainmcp.Server{
		ID: 9, BaseURL: "https://mcp.example.test/mcp", HeadersJSON: `{}`, HeadersEnabled: true,
		Status: "active", ContextJWTMode: "none",
	}}
	manager := &lifecycleSessionManager{}
	service := NewServiceWithRuntime(
		config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}),
		repo,
		manager,
	)
	service.SetUserProfileResolver(userProfileResolverStub{user: domainuser.User{
		PublicID: "actor-public", Username: "actor", Role: domainuser.RoleAdmin,
	}})

	_, err := service.ProbeServer(t.Context(), ProbeServerInput{ServerID: 9, ActorUserID: 3})
	if !errors.Is(err, ErrMCPServerProbeFailed) {
		t.Fatalf("ProbeServer error = %v, want ErrMCPServerProbeFailed", err)
	}
	if manager.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", manager.closeCalls)
	}
}

func canceledLifecycleContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestServiceRealSessionLifecycleDeletesOpenedAndSkipsPreOpenCanceled(t *testing.T) {
	for _, test := range []struct {
		name       string
		mode       inframcp.ContextMode
		listError  bool
		newContext func() context.Context
		wantOpen   int64
		wantDelete int64
	}{
		{name: "probe success", mode: inframcp.ContextModeProbe, newContext: context.Background, wantOpen: 1, wantDelete: 1},
		{name: "sync list error", mode: inframcp.ContextModeSync, listError: true, newContext: context.Background, wantOpen: 1, wantDelete: 1},
		{name: "probe canceled before open", mode: inframcp.ContextModeProbe, newContext: canceledLifecycleContext},
	} {
		t.Run(test.name, func(t *testing.T) {
			var initializes atomic.Int64
			var lists atomic.Int64
			var deletes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodDelete {
					deletes.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var envelope struct {
					ID     any    `json:"id"`
					Method string `json:"method"`
				}
				if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				switch envelope.Method {
				case "initialize":
					initializes.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("MCP-Session-Id", "ephemeral-session")
					_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-11-25","capabilities":{}}}`, envelope.ID)
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
				case "tools/list":
					lists.Add(1)
					w.Header().Set("Content-Type", "application/json")
					if test.listError {
						_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"error":{"code":-32000,"message":"fixture"}}`, envelope.ID)
						return
					}
					_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"tools":[{"name":"memory.list","inputSchema":{}}]}}`, envelope.ID)
				default:
					t.Errorf("unexpected method %q", envelope.Method)
					http.Error(w, "unexpected method", http.StatusBadRequest)
				}
			}))
			defer server.Close()

			repo := &lifecycleMCPRepo{server: domainmcp.Server{
				ID: 9, BaseURL: server.URL, HeadersJSON: `{}`, HeadersEnabled: true, Status: "active", ContextJWTMode: "none",
			}}
			manager := inframcp.NewSessionManager(inframcp.NewClient())
			service := NewServiceWithRuntime(
				config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}),
				repo,
				manager,
			)
			service.SetUserProfileResolver(userProfileResolverStub{user: domainuser.User{
				PublicID: "actor-public", Username: "actor", Role: domainuser.RoleAdmin,
			}})

			ctx := test.newContext()
			var callErr error
			if test.mode == inframcp.ContextModeProbe {
				_, callErr = service.ProbeServer(ctx, ProbeServerInput{ServerID: 9, ActorUserID: 3, RequestID: "probe-request"})
			} else {
				_, callErr = service.SyncServerTools(ctx, SyncServerToolsInput{ServerID: 9, RequestID: "sync-request"})
			}
			if test.wantOpen == 1 && !test.listError && callErr != nil {
				t.Fatalf("service call failed: %v", callErr)
			}
			if (test.listError || test.wantOpen == 0) && callErr == nil {
				t.Fatal("service call unexpectedly succeeded")
			}
			if initializes.Load() != test.wantOpen || lists.Load() != test.wantOpen || deletes.Load() != test.wantDelete {
				t.Fatalf("initialize/list/delete = %d/%d/%d, want %d/%d/%d", initializes.Load(), lists.Load(), deletes.Load(), test.wantOpen, test.wantOpen, test.wantDelete)
			}
			beforeCloseAll := deletes.Load()
			if err := manager.CloseAll(context.Background()); err != nil {
				t.Fatalf("CloseAll: %v", err)
			}
			if deletes.Load() != beforeCloseAll {
				t.Fatalf("CloseAll duplicated ephemeral DELETE: before=%d after=%d", beforeCloseAll, deletes.Load())
			}
		})
	}
}
