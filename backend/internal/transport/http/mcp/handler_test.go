package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/mcp"
	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	domainuser "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/user"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	inframcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/response"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/security"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/transport/http/middleware"
	"github.com/gin-gonic/gin"
)

func TestPreviewHeaderTemplateHandlerReturnsAuthoritativeRedactedPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := appmcp.NewServiceWithRuntime(config.NewRuntime(config.Config{}), nil, nil)
	handler := NewHandler(service)
	router := gin.New()
	router.POST("/api/v1/admin/mcp/header-templates/preview", handler.PreviewHeaderTemplate)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/mcp/header-templates/preview",
		strings.NewReader(`{"headersJSON":"{\"X-API-Key\":\"{{DEEIX_USER_PUBLIC_ID}}\"}","headersEnabled":true,"mode":"chat"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		ErrorCode string                        `json:"errorCode"`
		Data      HeaderTemplatePreviewResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ErrorCode != "" || envelope.Data.Mode != "chat" ||
		len(envelope.Data.SupportedTokens) != 11 || len(envelope.Data.Headers) != 1 ||
		envelope.Data.Headers[0].Name != "X-API-Key" ||
		envelope.Data.Headers[0].Value != security.RedactedHeaderValue ||
		!envelope.Data.Headers[0].Sensitive {
		t.Fatalf("response = %#v", envelope)
	}
	assertEnvelopeDataKeys(t, recorder.Body.Bytes(), "headers", "mode", "signedContextHeader", "supportedTokens", "warnings")
	assertJSONArrayField(t, recorder.Body.Bytes(), "warnings")
}

func TestPreviewHeaderTemplateHandlerUsesStableValidationCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := appmcp.NewServiceWithRuntime(config.NewRuntime(config.Config{}), nil, nil)
	handler := NewHandler(service)
	router := gin.New()
	router.POST("/preview", handler.PreviewHeaderTemplate)

	oversizedBody, err := json.Marshal(map[string]interface{}{
		"headersJSON":    strings.Repeat("x", 32769),
		"headersEnabled": true,
		"mode":           "chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "invalid mode", body: `{"headersJSON":"{}","headersEnabled":true,"mode":"other"}`, code: response.CodeMCPHeaderTemplateInvalidMode},
		{name: "oversized template", body: string(oversizedBody), code: response.CodeMCPHeaderTemplateInvalid},
		{name: "invalid template json", body: `{"headersJSON":"not-json","headersEnabled":true,"mode":"chat"}`, code: response.CodeMCPHeaderTemplateInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var envelope response.Envelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.ErrorCode != tt.code || envelope.Data != nil {
				t.Fatalf("envelope=%#v", envelope)
			}
		})
	}
}

func TestPreviewHeaderTemplateHandlerReturnsSignedBindingAndRequiresToggle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := auditTestServer(9, `{}`)
	server.ContextJWTMode = "hs256"
	server.ContextJWTSecretEnc = "v1:configured"
	server.ContextJWTAudience = "urn:deeix:mcp:mcp_preview"
	server.ContextJWTKeyID = "ctx_preview"
	server.ContextJWTExpiresSeconds = 300
	repo := &controlPlaneRepoStub{getServerFn: func(context.Context, uint) (*domainmcp.Server, error) {
		return server, nil
	}}
	handler := NewHandler(newControlPlaneService(repo, nil))
	router := gin.New()
	router.POST("/preview", handler.PreviewHeaderTemplate)

	t.Run("explicit false is accepted and server state is authoritative", func(t *testing.T) {
		body := `{"headersJSON":"{\"X-Customer-JWT\":\"{{DEEIX_SIGNED_CONTEXT}}\"}",` +
			`"headersEnabled":false,"serverID":9,"mode":"chat"}`
		recorder := serveJSONRequest(router, http.MethodPost, "/preview", body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var envelope struct {
			Data HeaderTemplatePreviewResponse `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.SignedContextHeader != "X-Customer-JWT" ||
			len(envelope.Data.Warnings) != 1 ||
			envelope.Data.Warnings[0].Code != "signed_context_headers_disabled" ||
			len(envelope.Data.Headers) != 1 || envelope.Data.Headers[0].Name != "X-Customer-JWT" ||
			envelope.Data.Headers[0].Value != security.RedactedHeaderValue || !envelope.Data.Headers[0].Sensitive {
			t.Fatalf("preview response=%#v", envelope.Data)
		}
		assertJSONArrayField(t, recorder.Body.Bytes(), "warnings")
	})

	t.Run("missing toggle uses standard invalid body envelope", func(t *testing.T) {
		body := `{"headersJSON":"{}","serverID":9,"mode":"chat"}`
		recorder := serveJSONRequest(router, http.MethodPost, "/preview", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var envelope response.Envelope
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.ErrorCode != response.CodeRequestInvalidBody || envelope.Data != nil {
			t.Fatalf("envelope=%#v", envelope)
		}
	})
}

type probeHandlerRepoStub struct {
	repository.MCPRepository
}

func (probeHandlerRepoStub) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	return &domainmcp.Server{
		ID: 9, Name: "Memory", BaseURL: "https://mcp.example.test/mcp",
		HeadersJSON: `{}`, HeadersEnabled: true, Status: "active", ContextJWTMode: "none",
	}, nil
}

type probeHandlerSessionManagerStub struct{}

type handlerListOperation struct {
	tools []inframcp.Tool
	err   error
}

func (o handlerListOperation) ListTools(context.Context) ([]inframcp.Tool, error) {
	return append([]inframcp.Tool(nil), o.tools...), o.err
}

func (handlerListOperation) CallTool(context.Context, inframcp.CallInput) (string, error) {
	return "", nil
}

func (probeHandlerSessionManagerStub) Acquire(context.Context, inframcp.AcquireInput) (inframcp.Operation, error) {
	return nil, nil
}

func (probeHandlerSessionManagerStub) OpenEphemeral(context.Context, inframcp.CallConfig, int) (inframcp.Operation, func(context.Context) error, error) {
	return handlerListOperation{tools: []inframcp.Tool{{Name: "memory.list"}}}, func(context.Context) error { return nil }, nil
}

func (probeHandlerSessionManagerStub) CloseRun(context.Context, string, string) error { return nil }
func (probeHandlerSessionManagerStub) CloseAll(context.Context) error                 { return nil }

type probeHandlerUserStub struct{}

func (probeHandlerUserStub) GetByID(context.Context, uint) (*domainuser.User, error) {
	return &domainuser.User{ID: 3, PublicID: "user-admin", Username: "admin", Role: domainuser.RoleAdmin}, nil
}

type probeAuditRecord struct {
	requestID  string
	userID     uint
	action     string
	resource   string
	resourceID string
	ip         string
	userAgent  string
	detail     interface{}
}

type probeAuditCapture struct {
	records []probeAuditRecord
}

func (a *probeAuditCapture) Write(
	_ context.Context,
	requestID string,
	userID uint,
	action string,
	resource string,
	resourceID string,
	ip string,
	userAgent string,
	detail interface{},
) {
	a.records = append(a.records, probeAuditRecord{
		requestID: requestID, userID: userID, action: action,
		resource: resource, resourceID: resourceID, ip: ip, userAgent: userAgent, detail: detail,
	})
}

func TestProbeServerHandlerUsesAuthenticatedActorAndSafeResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := appmcp.NewServiceWithRuntime(
		config.NewRuntime(config.Config{DataEncryptionKey: "test-data-key"}),
		probeHandlerRepoStub{},
		probeHandlerSessionManagerStub{},
	)
	service.SetUserProfileResolver(probeHandlerUserStub{})
	audit := &probeAuditCapture{}
	service.SetAuditWriter(audit)
	handler := NewHandler(service)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, uint(3))
		c.Set(middleware.ContextKeyRequestID, "request-probe")
		c.Next()
	})
	router.POST("/api/v1/admin/mcp/servers/:id/probe", handler.ProbeServer)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/mcp/servers/9/probe", nil)
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		ErrorCode string              `json:"errorCode"`
		RequestID string              `json:"requestId"`
		Data      ProbeServerResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ErrorCode != "" || envelope.Data.ToolCount != 1 {
		t.Fatalf("response = %#v", envelope)
	}
	assertEnvelopeDataKeys(t, recorder.Body.Bytes(), "toolCount", "warnings")
	if strings.Contains(recorder.Body.String(), "user-admin") ||
		strings.Contains(recorder.Body.String(), "admin") {
		t.Fatalf("identity leaked in response: %s", recorder.Body.String())
	}
	if len(audit.records) != 1 {
		t.Fatalf("audit records = %#v", audit.records)
	}
	record := audit.records[0]
	if record.requestID != "request-probe" || record.userID != 3 ||
		record.action != "mcp.server.probe" || record.resource != "mcp_servers" ||
		record.resourceID != "9" {
		t.Fatalf("audit record = %#v", record)
	}
	detail, err := json.Marshal(record.detail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(detail), "user-admin") || strings.Contains(string(detail), "admin") {
		t.Fatalf("identity leaked in audit detail: %s", detail)
	}
}

func TestPublicMCPErrorCodeMappings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		code string
	}{
		{appmcp.ErrInvalidHeaderTemplate, response.CodeMCPHeaderTemplateInvalid},
		{appmcp.ErrInvalidHeaderTemplateMode, response.CodeMCPHeaderTemplateInvalidMode},
		{appmcp.ErrInvalidAuthTokenUpdate, response.CodeMCPServerInvalidAuthUpdate},
		{appmcp.ErrMCPServerNotFound, response.CodeMCPServerNotFound},
		{appmcp.ErrUnsafeMCPServerTarget, response.CodeMCPServerUnsafeTarget},
		{appmcp.ErrMCPServerProbeFailed, response.CodeMCPServerProbeFailed},
		{appmcp.ErrMCPServerSyncFailed, response.CodeMCPServerSyncFailed},
	}
	for _, tt := range tests {
		if got := publicMCPErrorCode(tt.err); got != tt.code {
			t.Fatalf("error %v mapped to %q, want %q", tt.err, got, tt.code)
		}
	}
}

func TestWriteMCPPublicErrorUsesStableEnvelopeWithoutCauseText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "remote-secret-must-not-leak"
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid template", err: appmcp.ErrInvalidHeaderTemplate, status: 400, code: response.CodeMCPHeaderTemplateInvalid},
		{name: "invalid mode", err: appmcp.ErrInvalidHeaderTemplateMode, status: 400, code: response.CodeMCPHeaderTemplateInvalidMode},
		{name: "invalid auth update", err: appmcp.ErrInvalidAuthTokenUpdate, status: 400, code: response.CodeMCPServerInvalidAuthUpdate},
		{name: "not found", err: appmcp.ErrMCPServerNotFound, status: 404, code: response.CodeMCPServerNotFound},
		{name: "unsafe target", err: appmcp.ErrUnsafeMCPServerTarget, status: 400, code: response.CodeMCPServerUnsafeTarget},
		{name: "probe", err: fmt.Errorf("%w: %s", appmcp.ErrMCPServerProbeFailed, secret), status: 502, code: response.CodeMCPServerProbeFailed},
		{name: "sync", err: fmt.Errorf("%w: %s", appmcp.ErrMCPServerSyncFailed, secret), status: 502, code: response.CodeMCPServerSyncFailed},
		{name: "client unavailable", err: appmcp.ErrMCPClientUnavailable, status: 503, code: "mcp.client_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set(middleware.ContextKeyRequestID, "request-error")
			writeMCPPublicError(ctx, tt.err)
			if recorder.Code != tt.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var envelope response.Envelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.ErrorCode != tt.code || envelope.RequestID != "request-error" || envelope.Data != nil {
				t.Fatalf("envelope = %#v", envelope)
			}
			if strings.Contains(recorder.Body.String(), secret) {
				t.Fatalf("remote cause leaked: %s", recorder.Body.String())
			}
		})
	}
}

func TestMCPAdminRoutesRegisterPreviewAndProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := appmcp.NewServiceWithRuntime(config.NewRuntime(config.Config{}), nil, nil)
	router := gin.New()
	NewModule(NewHandler(service)).RegisterAdminRoutes(router.Group("/api/v1/admin"))

	routes := make(map[string]struct{})
	for _, item := range router.Routes() {
		routes[item.Method+" "+item.Path] = struct{}{}
	}
	for _, route := range []string{
		"POST /api/v1/admin/mcp/header-templates/preview",
		"POST /api/v1/admin/mcp/servers/:id/probe",
	} {
		if _, ok := routes[route]; !ok {
			t.Fatalf("missing route %q; routes=%#v", route, routes)
		}
	}
}

func TestWarningCodesReturnsSortedUniqueSafeCodes(t *testing.T) {
	t.Parallel()
	got := warningCodes([]inframcp.HeaderTemplateWarning{
		{Code: " unknown_token ", HeaderName: "X-Secret", Token: "{{PRIVATE_TOKEN}}"},
		{Code: "malformed_token", HeaderName: "X-Other"},
		{Code: "unknown_token", HeaderName: "Authorization"},
		{Code: "   ", Token: "secret-value"},
	})
	if strings.Join(got, ",") != "malformed_token,unknown_token" {
		t.Fatalf("warning codes = %#v", got)
	}
}

func TestMCPControlPlaneHandlersRecordSafeSuccessAudits(t *testing.T) {
	const (
		actorID   = uint(3)
		requestID = "request-control-plane"
	)
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		action     string
		resourceID string
		detail     map[string]interface{}
		service    func() *appmcp.Service
	}{
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/servers",
			body: `{"name":"Memory","baseURL":"https://mcp.example.test/mcp",` +
				`"authToken":"bearer-secret","headersJSON":"{\"X-API-Key\":\"template-value-secret\"}","status":"active"}`,
			action:     "mcp.server.create",
			resourceID: "11",
			detail: map[string]interface{}{
				"outcome":       "success",
				"changedFields": []string{"authToken", "baseURL", "headersJSON", "name", "status"},
			},
			service: func() *appmcp.Service {
				repo := &controlPlaneRepoStub{createServerFn: func(_ context.Context, input repository.CreateMCPServerInput) (*domainmcp.Server, error) {
					return &domainmcp.Server{ID: 11, Name: input.Name, BaseURL: input.BaseURL, AuthTokenEnc: input.AuthTokenEnc, HeadersJSON: input.HeadersJSON, Status: input.Status}, nil
				}}
				return newControlPlaneService(repo, nil)
			},
		},
		{
			name:   "update",
			method: http.MethodPatch,
			path:   "/servers/9",
			body: `{"name":"owner@example.test","authToken":"bearer-secret",` +
				`"headersJSON":"{\"X-API-Key\":\"********\",\"X-Tenant\":\"template-value-secret\"}","headersEnabled":false,"status":"inactive"}`,
			action:     "mcp.server.update",
			resourceID: "9",
			detail: map[string]interface{}{
				"outcome":       "success",
				"changedFields": []string{"authToken", "headersEnabled", "headersJSON", "name", "status"},
			},
			service: func() *appmcp.Service {
				server := auditTestServer(9, `{ "X-API-Key": "stored-secret" }`)
				repo := &controlPlaneRepoStub{
					getServerFn: func(context.Context, uint) (*domainmcp.Server, error) { return server, nil },
					updateServerFn: func(_ context.Context, _ uint, input repository.UpdateMCPServerInput) (*domainmcp.Server, error) {
						item := *server
						if input.Name != nil {
							item.Name = *input.Name
						}
						if input.AuthTokenEnc != nil {
							item.AuthTokenEnc = *input.AuthTokenEnc
						}
						if input.HeadersJSON != nil {
							item.HeadersJSON = *input.HeadersJSON
						}
						if input.HeadersEnabled != nil {
							item.HeadersEnabled = *input.HeadersEnabled
						}
						if input.Status != nil {
							item.Status = *input.Status
						}
						return &item, nil
					},
				}
				return newControlPlaneService(repo, nil)
			},
		},
		{
			name:       "delete",
			method:     http.MethodDelete,
			path:       "/servers/9",
			action:     "mcp.server.delete",
			resourceID: "9",
			detail:     map[string]interface{}{"outcome": "success"},
			service: func() *appmcp.Service {
				return newControlPlaneService(&controlPlaneRepoStub{deleteServerFn: func(context.Context, uint) error { return nil }}, nil)
			},
		},
		{
			name:       "status update",
			method:     http.MethodPatch,
			path:       "/servers/9/tools/status",
			body:       `{"toolIDs":[1,2],"status":"inactive"}`,
			action:     "mcp.server.status_update",
			resourceID: "9",
			detail: map[string]interface{}{
				"outcome":       "success",
				"changedFields": []string{"status"},
				"toolCount":     2,
			},
			service: func() *appmcp.Service {
				repo := &controlPlaneRepoStub{updateStatusFn: func(context.Context, uint, []uint, string) ([]domainmcp.Tool, error) {
					return []domainmcp.Tool{{ID: 1}, {ID: 2}}, nil
				}}
				return newControlPlaneService(repo, nil)
			},
		},
		{
			name:   "reorder",
			method: http.MethodPatch,
			path:   "/servers/order",
			body:   `{"servers":[{"serverID":9,"toolIDs":[1,2]},{"serverID":10,"toolIDs":[3]}]}`,
			action: "mcp.server.reorder",
			detail: map[string]interface{}{
				"outcome":     "success",
				"serverCount": 2,
				"toolCount":   3,
			},
			service: func() *appmcp.Service {
				repo := &controlPlaneRepoStub{
					listServersFn: func(context.Context) ([]domainmcp.Server, error) {
						return []domainmcp.Server{*auditTestServer(9, `{}`), *auditTestServer(10, `{}`)}, nil
					},
					listToolsFn: func(_ context.Context, serverID uint, _ bool) ([]domainmcp.Tool, error) {
						if serverID == 9 {
							return []domainmcp.Tool{{ID: 1}, {ID: 2}}, nil
						}
						return []domainmcp.Tool{{ID: 3}}, nil
					},
					reorderFn: func(context.Context, []repository.ReorderMCPServerInput) ([]domainmcp.ServerWithTools, error) {
						return []domainmcp.ServerWithTools{
							{Server: *auditTestServer(9, `{}`), Tools: []domainmcp.Tool{{ID: 1}, {ID: 2}}},
							{Server: *auditTestServer(10, `{}`), Tools: []domainmcp.Tool{{ID: 3}}},
						}, nil
					},
				}
				return newControlPlaneService(repo, nil)
			},
		},
		{
			name:       "sync",
			method:     http.MethodPost,
			path:       "/servers/9/sync",
			action:     "mcp.server.sync",
			resourceID: "9",
			detail:     map[string]interface{}{"outcome": "success", "toolCount": 1},
			service: func() *appmcp.Service {
				server := auditTestServer(9, `{}`)
				repo := &controlPlaneRepoStub{
					getServerFn:    func(context.Context, uint) (*domainmcp.Server, error) { return server, nil },
					replaceToolsFn: func(context.Context, uint, []domainmcp.Tool, bool) error { return nil },
					listToolsFn: func(context.Context, uint, bool) ([]domainmcp.Tool, error) {
						return []domainmcp.Tool{{ID: 1, ServerID: 9, Name: "memory.list"}}, nil
					},
				}
				return newControlPlaneService(repo, controlPlaneSessionManagerStub{tools: []inframcp.Tool{{Name: "memory.list"}}})
			},
		},
		{
			name:       "probe",
			method:     http.MethodPost,
			path:       "/servers/9/probe",
			action:     "mcp.server.probe",
			resourceID: "9",
			detail: map[string]interface{}{
				"outcome":      "success",
				"toolCount":    1,
				"warningCodes": []string{"unknown_token"},
			},
			service: func() *appmcp.Service {
				repo := &controlPlaneRepoStub{getServerFn: func(context.Context, uint) (*domainmcp.Server, error) {
					return auditTestServer(9, `{"X-Warning":"{{VENDOR_TOKEN}}"}`), nil
				}}
				service := newControlPlaneService(repo, controlPlaneSessionManagerStub{tools: []inframcp.Tool{{Name: "memory.list"}}})
				service.SetUserProfileResolver(controlPlaneUserStub{user: &domainuser.User{
					ID: actorID, PublicID: "user-admin", Username: "admin", Email: "owner@example.test", Role: domainuser.RoleAdmin,
				}})
				return service
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			audit := &probeAuditCapture{}
			service := tt.service()
			service.SetAuditWriter(audit)
			router := newControlPlaneRouter(service, actorID, requestID)
			recorder := serveControlPlaneRequest(router, tt.method, tt.path, tt.body)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			assertSingleSafeAudit(t, audit, requestID, tt.action, tt.resourceID, tt.detail)
		})
	}
}

func TestMCPControlPlaneHandlersRecordStableSafeErrorAudits(t *testing.T) {
	const remoteError = "remote-error-text-secret"
	tests := []struct {
		name         string
		method       string
		path         string
		body         string
		action       string
		resourceID   string
		status       int
		responseCode string
		auditCode    string
		service      func() *appmcp.Service
	}{
		{
			name: "create", method: http.MethodPost, path: "/servers",
			body: `{"name":"Memory","baseURL":"https://mcp.example.test/mcp?token=url-query-secret",` +
				`"authToken":"bearer-secret","headersJSON":"{\"X-Owner\":\"owner@example.test\",\"X-Template\":\"template-value-secret\"}","status":"active"}`,
			action: "mcp.server.create", status: http.StatusBadRequest,
			responseCode: "mcp.invalid_server_base_url", auditCode: response.CodeInternal,
			service: func() *appmcp.Service { return newControlPlaneService(&controlPlaneRepoStub{}, nil) },
		},
		{
			name: "update", method: http.MethodPatch, path: "/servers/9",
			body: `{"name":"owner@example.test","authToken":"bearer-secret",` +
				`"headersJSON":"{\"X-Template\":\"template-value-secret\"}"}`,
			action: "mcp.server.update", resourceID: "9", status: http.StatusNotFound,
			responseCode: response.CodeMCPServerNotFound, auditCode: response.CodeMCPServerNotFound,
			service: func() *appmcp.Service {
				return newControlPlaneService(&controlPlaneRepoStub{getServerFn: func(context.Context, uint) (*domainmcp.Server, error) {
					return nil, repository.ErrNotFound
				}}, nil)
			},
		},
		{
			name: "delete", method: http.MethodDelete, path: "/servers/9",
			action: "mcp.server.delete", resourceID: "9", status: http.StatusNotFound,
			responseCode: response.CodeMCPServerNotFound, auditCode: response.CodeMCPServerNotFound,
			service: func() *appmcp.Service {
				return newControlPlaneService(&controlPlaneRepoStub{deleteServerFn: func(context.Context, uint) error {
					return repository.ErrNotFound
				}}, nil)
			},
		},
		{
			name: "status update", method: http.MethodPatch, path: "/servers/9/tools/status",
			body:   `{"toolIDs":[1],"status":"active"}`,
			action: "mcp.server.status_update", resourceID: "9", status: http.StatusInternalServerError,
			responseCode: response.CodeInternal, auditCode: response.CodeInternal,
			service: func() *appmcp.Service {
				return newControlPlaneService(&controlPlaneRepoStub{updateStatusFn: func(context.Context, uint, []uint, string) ([]domainmcp.Tool, error) {
					return nil, errors.New(remoteError)
				}}, nil)
			},
		},
		{
			name: "reorder", method: http.MethodPatch, path: "/servers/order",
			body:   `{"servers":[{"serverID":9,"toolIDs":[1]}]}`,
			action: "mcp.server.reorder", status: http.StatusInternalServerError,
			responseCode: response.CodeInternal, auditCode: response.CodeInternal,
			service: func() *appmcp.Service {
				return newControlPlaneService(&controlPlaneRepoStub{
					listServersFn: func(context.Context) ([]domainmcp.Server, error) {
						return []domainmcp.Server{*auditTestServer(9, `{}`)}, nil
					},
					listToolsFn: func(context.Context, uint, bool) ([]domainmcp.Tool, error) {
						return []domainmcp.Tool{{ID: 1}}, nil
					},
					reorderFn: func(context.Context, []repository.ReorderMCPServerInput) ([]domainmcp.ServerWithTools, error) {
						return nil, errors.New(remoteError)
					},
				}, nil)
			},
		},
		{
			name: "sync", method: http.MethodPost, path: "/servers/9/sync",
			action: "mcp.server.sync", resourceID: "9", status: http.StatusBadGateway,
			responseCode: response.CodeMCPServerSyncFailed, auditCode: response.CodeMCPServerSyncFailed,
			service: func() *appmcp.Service {
				server := auditTestServer(9, `{}`)
				repo := &controlPlaneRepoStub{
					getServerFn: func(context.Context, uint) (*domainmcp.Server, error) { return server, nil },
					updateServerFn: func(context.Context, uint, repository.UpdateMCPServerInput) (*domainmcp.Server, error) {
						return server, nil
					},
				}
				return newControlPlaneService(repo, controlPlaneSessionManagerStub{err: errors.New(remoteError)})
			},
		},
		{
			name: "probe", method: http.MethodPost, path: "/servers/9/probe",
			action: "mcp.server.probe", resourceID: "9", status: http.StatusBadGateway,
			responseCode: response.CodeMCPServerProbeFailed, auditCode: response.CodeMCPServerProbeFailed,
			service: func() *appmcp.Service {
				repo := &controlPlaneRepoStub{getServerFn: func(context.Context, uint) (*domainmcp.Server, error) {
					return auditTestServer(9, `{"X-Owner":"{{DEEIX_USER_EMAIL}}"}`), nil
				}}
				service := newControlPlaneService(repo, controlPlaneSessionManagerStub{err: errors.New(remoteError)})
				service.SetUserProfileResolver(controlPlaneUserStub{user: &domainuser.User{
					ID: 3, PublicID: "user-admin", Username: "admin", Email: "owner@example.test", Role: domainuser.RoleAdmin,
				}})
				return service
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			audit := &probeAuditCapture{}
			service := tt.service()
			service.SetAuditWriter(audit)
			router := newControlPlaneRouter(service, 3, "request-control-plane-error")
			recorder := serveControlPlaneRequest(router, tt.method, tt.path, tt.body)
			if recorder.Code != tt.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var envelope response.Envelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.ErrorCode != tt.responseCode || envelope.Data != nil {
				t.Fatalf("envelope=%#v", envelope)
			}
			assertTextOmitsAll(t, recorder.Body.String(), forbiddenMCPAuditFixtures...)
			assertSingleSafeAudit(t, audit, "request-control-plane-error", tt.action, tt.resourceID, map[string]interface{}{
				"outcome": "error", "errorCode": tt.auditCode,
			})
		})
	}
}

func TestUpdateServerMapsPartialRequestWithoutSynthesizingFields(t *testing.T) {
	repo := &handlerMCPRepositoryStub{server: handlerTestMCPServer()}
	router := newMCPHandlerTestRouter(repo)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/servers/7", strings.NewReader(`{"status":"inactive"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if repo.updateCalls != 1 {
		t.Fatalf("expected one update, got %d", repo.updateCalls)
	}
	if repo.updatedInput.Name != nil || repo.updatedInput.BaseURL != nil || repo.updatedInput.AuthTokenEnc != nil || repo.updatedInput.HeadersJSON != nil {
		t.Fatalf("partial request synthesized absent fields: %#v", repo.updatedInput)
	}
	if repo.updatedInput.Status == nil || *repo.updatedInput.Status != "inactive" {
		t.Fatalf("expected inactive status pointer, got %#v", repo.updatedInput.Status)
	}
	if !strings.Contains(recorder.Body.String(), `"authTokenConfigured":true`) {
		t.Fatalf("expected configured state in response: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "real-secret") {
		t.Fatalf("response exposed sensitive header: %s", recorder.Body.String())
	}
}

func TestUpdateServerReturnsNotFound(t *testing.T) {
	repo := &handlerMCPRepositoryStub{getErr: repository.ErrNotFound}
	router := newMCPHandlerTestRouter(repo)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/servers/404", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"data":null`) {
		t.Fatalf("expected standard error envelope, got %s", recorder.Body.String())
	}
}

func TestDeleteServerReturnsNotFound(t *testing.T) {
	repo := &handlerMCPRepositoryStub{deleteErr: repository.ErrNotFound}
	router := newMCPHandlerTestRouter(repo)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/servers/404", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"data":null`) {
		t.Fatalf("expected standard error envelope, got %s", recorder.Body.String())
	}
}

func TestUpdateServerRejectsInvalidAuthTokenPatch(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty token", body: `{"authToken":"   "}`},
		{name: "clear and token", body: `{"authToken":"replacement","clearAuthToken":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &handlerMCPRepositoryStub{server: handlerTestMCPServer()}
			router := newMCPHandlerTestRouter(repo)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPatch, "/servers/7", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
			}
			if repo.updateCalls != 0 {
				t.Fatalf("invalid auth patch wrote %d updates", repo.updateCalls)
			}
		})
	}
}

func TestToServerResponseReportsAuthTokenConfigured(t *testing.T) {
	tests := []struct {
		name       string
		ciphertext string
		want       bool
	}{
		{name: "configured", ciphertext: "v1:ciphertext", want: true},
		{name: "empty", ciphertext: "", want: false},
		{name: "whitespace", ciphertext: "   ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toServerResponse(appmcp.ServerView{Server: domainmcp.Server{AuthTokenEnc: tt.ciphertext}})
			if got.AuthTokenConfigured != tt.want {
				t.Fatalf("got AuthTokenConfigured=%v want %v", got.AuthTokenConfigured, tt.want)
			}
		})
	}
}

func newMCPHandlerTestRouter(repo repository.MCPRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	service := appmcp.NewServiceWithRuntime(config.NewRuntime(config.Config{
		Env:               "dev",
		DataEncryptionKey: "test-mcp-handler-data-encryption-key",
	}), repo, nil)
	handler := NewHandler(service)
	router := gin.New()
	router.PATCH("/servers/:id", handler.UpdateServer)
	router.DELETE("/servers/:id", handler.DeleteServer)
	return router
}

func handlerTestMCPServer() *domainmcp.Server {
	return &domainmcp.Server{
		ID:             7,
		Name:           "Example",
		BaseURL:        "https://example.com/mcp",
		AuthTokenEnc:   "existing-ciphertext",
		HeadersJSON:    `{"X-API-Key":"real-secret","X-Tenant":"old"}`,
		HeadersEnabled: true,
		Status:         "active",
	}
}

type handlerMCPRepositoryStub struct {
	server       *domainmcp.Server
	getErr       error
	updateErr    error
	deleteErr    error
	updateCalls  int
	updatedInput repository.UpdateMCPServerInput
}

func (*handlerMCPRepositoryStub) CreateServer(context.Context, repository.CreateMCPServerInput) (*domainmcp.Server, error) {
	return nil, nil
}

func (r *handlerMCPRepositoryStub) UpdateServer(_ context.Context, _ uint, input repository.UpdateMCPServerInput) (*domainmcp.Server, error) {
	r.updateCalls++
	r.updatedInput = input
	if r.updateErr != nil {
		return nil, r.updateErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	if input.Name != nil {
		item.Name = *input.Name
	}
	if input.BaseURL != nil {
		item.BaseURL = *input.BaseURL
	}
	if input.AuthTokenEnc != nil {
		item.AuthTokenEnc = *input.AuthTokenEnc
	}
	if input.HeadersJSON != nil {
		item.HeadersJSON = *input.HeadersJSON
	}
	if input.Status != nil {
		item.Status = *input.Status
	}
	return &item, nil
}

func (*handlerMCPRepositoryStub) ListServers(context.Context) ([]domainmcp.Server, error) {
	return nil, nil
}

func (r *handlerMCPRepositoryStub) GetServer(context.Context, uint) (*domainmcp.Server, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.server == nil {
		return nil, repository.ErrNotFound
	}
	item := *r.server
	return &item, nil
}

func (r *handlerMCPRepositoryStub) DeleteServer(context.Context, uint) error {
	return r.deleteErr
}

func (*handlerMCPRepositoryStub) ReplaceServerTools(context.Context, uint, []domainmcp.Tool, bool) error {
	return nil
}

func (*handlerMCPRepositoryStub) ListTools(context.Context, uint, bool) ([]domainmcp.Tool, error) {
	return nil, nil
}

func (*handlerMCPRepositoryStub) ListToolsByIDs(context.Context, []uint) ([]domainmcp.Tool, error) {
	return nil, nil
}

func (*handlerMCPRepositoryStub) UpdateTool(context.Context, uint, repository.UpdateMCPToolInput) (*domainmcp.Tool, error) {
	return nil, nil
}

func (*handlerMCPRepositoryStub) UpdateServerToolsStatus(context.Context, uint, []uint, string) ([]domainmcp.Tool, error) {
	return nil, nil
}

func (*handlerMCPRepositoryStub) ReorderServersWithTools(context.Context, []repository.ReorderMCPServerInput) ([]domainmcp.ServerWithTools, error) {
	return nil, nil
}

var forbiddenMCPAuditFixtures = []string{
	"template-value-secret",
	"bearer-secret",
	"owner@example.test",
	"url-query-secret",
	"remote-error-text-secret",
	"stored-secret",
	"user-admin",
}

func assertEnvelopeDataKeys(t *testing.T, body []byte, want ...string) {
	t.Helper()
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != len(want) {
		t.Fatalf("data keys=%v want=%v body=%s", mapKeys(envelope.Data), want, body)
	}
	for _, key := range want {
		if _, ok := envelope.Data[key]; !ok {
			t.Fatalf("missing data key %q; keys=%v body=%s", key, mapKeys(envelope.Data), body)
		}
	}
}

func assertJSONArrayField(t *testing.T, body []byte, field string) {
	t.Helper()
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	raw, ok := envelope.Data[field]
	if !ok || string(raw) == "null" {
		t.Fatalf("%s must be a non-null array; body=%s", field, body)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("%s is not an array: %v; body=%s", field, err, body)
	}
}

func serveJSONRequest(router http.Handler, method string, path string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func assertSingleSafeAudit(
	t *testing.T,
	audit *probeAuditCapture,
	requestID string,
	action string,
	resourceID string,
	wantDetail map[string]interface{},
) {
	t.Helper()
	if len(audit.records) != 1 {
		t.Fatalf("audit records=%#v", audit.records)
	}
	record := audit.records[0]
	if record.requestID != requestID || record.userID != 3 || record.action != action ||
		record.resource != "mcp_servers" || record.resourceID != resourceID ||
		record.ip != "192.0.2.20" || record.userAgent != "mcp-audit-test-agent" {
		t.Fatalf("audit record=%#v", record)
	}
	got, err := json.Marshal(record.detail)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(wantDetail)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("audit detail=%s want=%s", got, want)
	}
	assertTextOmitsAll(t, string(got), forbiddenMCPAuditFixtures...)
}

func assertTextOmitsAll(t *testing.T, value string, forbidden ...string) {
	t.Helper()
	for _, item := range forbidden {
		if strings.Contains(value, item) {
			t.Fatalf("sensitive fixture %q leaked in %q", item, value)
		}
	}
}

func newControlPlaneRouter(service *appmcp.Service, actorID uint, requestID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(service)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, actorID)
		c.Set(middleware.ContextKeyRequestID, requestID)
		c.Next()
	})
	router.POST("/servers", handler.CreateServer)
	router.PATCH("/servers/order", handler.ReorderServers)
	router.PATCH("/servers/:id", handler.UpdateServer)
	router.DELETE("/servers/:id", handler.DeleteServer)
	router.PATCH("/servers/:id/tools/status", handler.UpdateServerToolsStatus)
	router.POST("/servers/:id/sync", handler.SyncServerTools)
	router.POST("/servers/:id/probe", handler.ProbeServer)
	return router
}

func serveControlPlaneRequest(router *gin.Engine, method string, path string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "mcp-audit-test-agent")
	request.RemoteAddr = "192.0.2.20:43123"
	router.ServeHTTP(recorder, request)
	return recorder
}

func newControlPlaneService(repo repository.MCPRepository, sessions inframcp.SessionManager) *appmcp.Service {
	return appmcp.NewServiceWithRuntime(config.NewRuntime(config.Config{
		Env:               "dev",
		DataEncryptionKey: "test-control-plane-data-key",
	}), repo, sessions)
}

func auditTestServer(id uint, headersJSON string) *domainmcp.Server {
	return &domainmcp.Server{
		ID: id, Name: "Memory", BaseURL: "https://mcp.example.test/mcp",
		HeadersJSON: headersJSON, HeadersEnabled: true, Status: "active", ContextJWTMode: "none",
	}
}

type controlPlaneSessionManagerStub struct {
	tools []inframcp.Tool
	err   error
}

func (controlPlaneSessionManagerStub) Acquire(context.Context, inframcp.AcquireInput) (inframcp.Operation, error) {
	return nil, nil
}

func (s controlPlaneSessionManagerStub) OpenEphemeral(context.Context, inframcp.CallConfig, int) (inframcp.Operation, func(context.Context) error, error) {
	return handlerListOperation{tools: s.tools, err: s.err}, func(context.Context) error { return nil }, nil
}

func (controlPlaneSessionManagerStub) CloseRun(context.Context, string, string) error { return nil }
func (controlPlaneSessionManagerStub) CloseAll(context.Context) error                 { return nil }

type controlPlaneUserStub struct {
	user *domainuser.User
	err  error
}

func (s controlPlaneUserStub) GetByID(context.Context, uint) (*domainuser.User, error) {
	return s.user, s.err
}

type controlPlaneRepoStub struct {
	repository.MCPRepository
	createServerFn func(context.Context, repository.CreateMCPServerInput) (*domainmcp.Server, error)
	updateServerFn func(context.Context, uint, repository.UpdateMCPServerInput) (*domainmcp.Server, error)
	listServersFn  func(context.Context) ([]domainmcp.Server, error)
	getServerFn    func(context.Context, uint) (*domainmcp.Server, error)
	deleteServerFn func(context.Context, uint) error
	replaceToolsFn func(context.Context, uint, []domainmcp.Tool, bool) error
	listToolsFn    func(context.Context, uint, bool) ([]domainmcp.Tool, error)
	updateStatusFn func(context.Context, uint, []uint, string) ([]domainmcp.Tool, error)
	reorderFn      func(context.Context, []repository.ReorderMCPServerInput) ([]domainmcp.ServerWithTools, error)
}

func (r *controlPlaneRepoStub) CreateServer(ctx context.Context, input repository.CreateMCPServerInput) (*domainmcp.Server, error) {
	if r.createServerFn == nil {
		panic("unexpected CreateServer")
	}
	return r.createServerFn(ctx, input)
}

func (r *controlPlaneRepoStub) UpdateServer(ctx context.Context, serverID uint, input repository.UpdateMCPServerInput) (*domainmcp.Server, error) {
	if r.updateServerFn == nil {
		panic("unexpected UpdateServer")
	}
	return r.updateServerFn(ctx, serverID, input)
}

func (r *controlPlaneRepoStub) ListServers(ctx context.Context) ([]domainmcp.Server, error) {
	if r.listServersFn == nil {
		panic("unexpected ListServers")
	}
	return r.listServersFn(ctx)
}

func (r *controlPlaneRepoStub) GetServer(ctx context.Context, serverID uint) (*domainmcp.Server, error) {
	if r.getServerFn == nil {
		panic("unexpected GetServer")
	}
	return r.getServerFn(ctx, serverID)
}

func (r *controlPlaneRepoStub) DeleteServer(ctx context.Context, serverID uint) error {
	if r.deleteServerFn == nil {
		panic("unexpected DeleteServer")
	}
	return r.deleteServerFn(ctx, serverID)
}

func (r *controlPlaneRepoStub) ReplaceServerTools(ctx context.Context, serverID uint, tools []domainmcp.Tool, overwriteCustomizedMetadata bool) error {
	if r.replaceToolsFn == nil {
		panic("unexpected ReplaceServerTools")
	}
	return r.replaceToolsFn(ctx, serverID, tools, overwriteCustomizedMetadata)
}

func (r *controlPlaneRepoStub) ListTools(ctx context.Context, serverID uint, onlyActive bool) ([]domainmcp.Tool, error) {
	if r.listToolsFn == nil {
		panic("unexpected ListTools")
	}
	return r.listToolsFn(ctx, serverID, onlyActive)
}

func (r *controlPlaneRepoStub) UpdateServerToolsStatus(ctx context.Context, serverID uint, toolIDs []uint, status string) ([]domainmcp.Tool, error) {
	if r.updateStatusFn == nil {
		panic("unexpected UpdateServerToolsStatus")
	}
	return r.updateStatusFn(ctx, serverID, toolIDs, status)
}

func (r *controlPlaneRepoStub) ReorderServersWithTools(ctx context.Context, order []repository.ReorderMCPServerInput) ([]domainmcp.ServerWithTools, error) {
	if r.reorderFn == nil {
		panic("unexpected ReorderServersWithTools")
	}
	return r.reorderFn(ctx, order)
}
