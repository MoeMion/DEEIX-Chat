package mcp

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	systemeventapp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/systemevent"
	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	domainuser "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/user"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	inframcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/pkg/conv"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/pkg/secretbox"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/security"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var (
	ErrInvalidServerName           = errors.New("invalid mcp server name")
	ErrInvalidServerBaseURL        = errors.New("invalid mcp server base url")
	ErrInvalidServerStatus         = errors.New("invalid mcp server status")
	ErrInvalidServerHeaders        = errors.New("invalid mcp server headers json")
	ErrInvalidAuthTokenUpdate      = errors.New("invalid mcp auth token update")
	ErrMCPServerNotFound           = errors.New("mcp server not found")
	ErrInvalidToolStatus           = errors.New("invalid mcp tool status")
	ErrInvalidToolName             = errors.New("invalid mcp tool display name")
	ErrInvalidToolDesc             = errors.New("invalid mcp tool description")
	ErrInvalidToolSelection        = errors.New("invalid mcp tool selection")
	ErrMCPClientUnavailable        = errors.New("mcp client unavailable")
	ErrInvalidHeaderTemplate       = errors.New("invalid mcp Header template")
	ErrInvalidHeaderTemplateMode   = errors.New("invalid mcp Header template mode")
	ErrUnsafeMCPServerTarget       = errors.New("unsafe mcp server target")
	ErrMCPServerProbeFailed        = errors.New("mcp server probe failed")
	ErrMCPServerSyncFailed         = errors.New("mcp server sync failed")
	ErrInvalidToolAttachmentConfig = errors.New("invalid mcp tool attachment configuration")
)

const mcpServerToolListTimeoutMS = 10000

type Service struct {
	cfg                 *config.Runtime
	repo                repository.MCPRepository
	mcpSessions         inframcp.SessionManager
	userProfileResolver UserProfileResolver
	systemEventWriter   systemEventWriter
	auditWriter         auditWriter
	contextJWTNow       func() time.Time
	contextJWTRandom    io.Reader
	contextJWTNewKeyID  func() string
}

type ReorderServerInput struct {
	ServerID uint
	ToolIDs  []uint
}

type systemEventWriter interface {
	Write(ctx context.Context, input systemeventapp.WriteInput)
}

type auditWriter interface {
	Write(ctx context.Context, requestID string, actorUserID uint, action string, resource string, resourceID string, ip string, userAgent string, detail interface{})
}

// UserProfileResolver resolves the authoritative persisted user profile used by probes.
type UserProfileResolver interface {
	GetByID(ctx context.Context, userID uint) (*domainuser.User, error)
}

type CreateServerInput struct {
	Name           string
	BaseURL        string
	AuthToken      string
	HeadersJSON    string
	HeadersEnabled *bool
	Status         string
}

type UpdateServerInput struct {
	Name           *string
	BaseURL        *string
	AuthToken      *string
	ClearAuthToken bool
	HeadersJSON    *string
	HeadersEnabled *bool
	Status         *string
}

type ToolInput struct {
	DisplayName              *string
	Description              *string
	AttachmentInputMode      *string
	AttachmentArgument       *string
	AttachmentEncoding       *string
	AttachmentPromptArgument *string
	Status                   *string
}

// ProbeServerInput describes an actor-authorized MCP server probe.
type ProbeServerInput struct {
	ServerID    uint
	ActorUserID uint
	RequestID   string
}

// ProbeServerResult reports probe metadata without mutating the stored tool catalog.
type ProbeServerResult struct {
	ToolCount int
	Analysis  inframcp.HeaderTemplateAnalysis
}

// HeaderPreviewItem is one deterministically ordered, safely redacted preview Header.
type HeaderPreviewItem struct {
	Name      string
	Value     string
	Sensitive bool
}

// PreviewHeaderTemplateResult contains a synthetic preview and its template analysis.
type PreviewHeaderTemplateResult struct {
	Mode                inframcp.ContextMode
	SupportedTokens     []string
	Warnings            []inframcp.HeaderTemplateWarning
	Headers             []HeaderPreviewItem
	SignedContextHeader string
}

type PreviewHeaderTemplateInput struct {
	HeadersJSON    string
	HeadersEnabled bool
	ServerID       *uint
	Mode           inframcp.ContextMode
}

// AuditInput describes an MCP server audit record.
type AuditInput struct {
	UserID     uint
	RequestID  string
	Action     string
	ResourceID string
	ClientIP   string
	UserAgent  string
	Detail     interface{}
}

// SyncServerToolsInput 描述一次 MCP 工具同步请求。
type SyncServerToolsInput struct {
	ServerID                    uint
	RequestID                   string
	OverwriteCustomizedMetadata bool
}

// NewServiceWithRuntime 创建 MCP 应用服务。
func NewServiceWithRuntime(cfg *config.Runtime, repo repository.MCPRepository, mcpSessions inframcp.SessionManager) *Service {
	return &Service{
		cfg:              cfg,
		repo:             repo,
		mcpSessions:      mcpSessions,
		contextJWTNow:    time.Now,
		contextJWTRandom: cryptorand.Reader,
		contextJWTNewKeyID: func() string {
			return "ctx_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		},
	}
}

// SetUserProfileResolver injects authoritative user profile resolution for probes.
func (s *Service) SetUserProfileResolver(resolver UserProfileResolver) {
	s.userProfileResolver = resolver
}

// SetAuditWriter injects the MCP audit writer.
func (s *Service) SetAuditWriter(writer auditWriter) {
	s.auditWriter = writer
}

// SetSystemEventWriter 注入系统事件写入器。
func (s *Service) SetSystemEventWriter(writer systemEventWriter) {
	s.systemEventWriter = writer
}

func (s *Service) ListServers(ctx context.Context) ([]domainmcp.Server, error) {
	now, err := s.contextJWTCurrentTime()
	if err != nil {
		return nil, err
	}
	if err = s.repo.ClearExpiredContextJWTPending(ctx, now); err != nil {
		return nil, err
	}
	return s.repo.ListServers(ctx)
}

func (s *Service) GetServer(ctx context.Context, serverID uint) (*domainmcp.Server, error) {
	item, err := s.repo.GetServer(ctx, serverID)
	if err != nil {
		return nil, translateServerRepositoryError(err)
	}
	return item, nil
}

// BuildCallConfig is the single application kernel for decrypting, parsing, rendering,
// and assembling outbound MCP call configuration.
func (s *Service) BuildCallConfig(
	ctx context.Context,
	server domainmcp.Server,
	templateContext inframcp.TemplateContext,
	timeoutMS int,
) (inframcp.CallConfig, inframcp.HeaderTemplateAnalysis, error) {
	if err := s.validateServerBaseURL(server.BaseURL); err != nil {
		return inframcp.CallConfig{}, inframcp.HeaderTemplateAnalysis{},
			fmt.Errorf("%w", ErrUnsafeMCPServerTarget)
	}
	token, err := s.decryptToken(server.AuthTokenEnc)
	if err != nil {
		return inframcp.CallConfig{}, inframcp.HeaderTemplateAnalysis{}, err
	}
	callConfig := inframcp.CallConfig{
		BaseURL:        strings.TrimSpace(server.BaseURL),
		AuthToken:      token,
		TimeoutMS:      timeoutMS,
		HeadersEnabled: server.HeadersEnabled,
		Context:        templateContext,
	}
	if !server.HeadersEnabled {
		trace.SpanFromContext(ctx).SetAttributes(attribute.Bool("signed_context", false))
		return callConfig, inframcp.HeaderTemplateAnalysis{}, nil
	}
	parsed, err := inframcp.ParseHeaderTemplateJSON(server.HeadersJSON)
	if err != nil {
		return inframcp.CallConfig{}, inframcp.HeaderTemplateAnalysis{},
			fmt.Errorf("%w", ErrInvalidHeaderTemplate)
	}
	headers, warnings, err := inframcp.RenderHeaderTemplate(parsed.Template, templateContext)
	if err != nil {
		return inframcp.CallConfig{}, inframcp.HeaderTemplateAnalysis{},
			fmt.Errorf("%w", ErrInvalidHeaderTemplate)
	}
	analysis := parsed.Analysis
	analysis.Warnings = append([]inframcp.HeaderTemplateWarning(nil), warnings...)
	var signedContext *inframcp.SignedContextConfig
	if analysis.SignedContextHeader != "" {
		signedContext, err = s.buildSignedContextConfig(server)
		if err != nil {
			return inframcp.CallConfig{}, inframcp.HeaderTemplateAnalysis{}, err
		}
	}
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.Bool("signed_context", signedContext != nil))
	if signedContext != nil {
		span.SetAttributes(attribute.String("kid", signedContext.KeyID))
	}
	callConfig.CustomHeaders = headers
	callConfig.SignedContextHeader = analysis.SignedContextHeader
	callConfig.SignedContext = signedContext
	return callConfig, analysis, nil
}

func (s *Service) buildSignedContextConfig(server domainmcp.Server) (*inframcp.SignedContextConfig, error) {
	switch server.ContextJWTMode {
	case "none":
		return nil, nil
	case "hs256":
	default:
		return nil, ErrMCPContextJWTInvalidStorage
	}

	cfg := s.cfg.Snapshot()
	secret, err := secretbox.DecryptString(cfg.DataEncryptionKey, server.ContextJWTSecretEnc)
	if err != nil {
		return nil, ErrMCPContextJWTInvalidStorage
	}
	if err = inframcp.ValidateSignedContextSecret(secret); err != nil {
		return nil, ErrMCPContextJWTInvalidStorage
	}
	if strings.TrimSpace(server.ContextJWTKeyID) == "" ||
		strings.TrimSpace(server.ContextJWTAudience) == "" {
		return nil, ErrMCPContextJWTInvalidStorage
	}
	issuer, err := normalizeContextJWTIssuer(cfg.PublicWebBaseURL, cfg.Env)
	if err != nil {
		return nil, ErrMCPContextJWTUnavailable
	}
	if !validContextJWTExpiresSeconds(server.ContextJWTExpiresSeconds) {
		return nil, ErrMCPContextJWTInvalidStorage
	}
	return &inframcp.SignedContextConfig{
		Secret:         secret,
		Issuer:         issuer,
		Audience:       server.ContextJWTAudience,
		KeyID:          server.ContextJWTKeyID,
		ExpiresSeconds: server.ContextJWTExpiresSeconds,
		IncludeName:    server.ContextJWTIncludeName,
		IncludeEmail:   server.ContextJWTIncludeEmail,
		IncludeRole:    server.ContextJWTIncludeRole,
	}, nil
}

func (s *Service) CreateServer(ctx context.Context, input CreateServerInput) (*domainmcp.Server, error) {
	normalized, err := s.normalizeCreateServerInput(input)
	if err != nil {
		return nil, err
	}
	tokenEnc, err := s.encryptToken(normalized.AuthToken)
	if err != nil {
		return nil, err
	}
	publicID := "mcp_" + conv.NormalizePublicID(uuid.NewString())
	return s.repo.CreateServer(ctx, repository.CreateMCPServerInput{
		PublicID:           publicID,
		ContextJWTAudience: "urn:deeix:mcp:" + publicID,
		Name:               normalized.Name,
		BaseURL:            normalized.BaseURL,
		AuthTokenEnc:       tokenEnc,
		HeadersJSON:        normalized.HeadersJSON,
		HeadersEnabled:     *normalized.HeadersEnabled,
		Status:             normalized.Status,
	})
}

func (s *Service) UpdateServer(ctx context.Context, serverID uint, input UpdateServerInput) (*domainmcp.Server, error) {
	current, err := s.repo.GetServer(ctx, serverID)
	if err != nil {
		return nil, translateServerRepositoryError(err)
	}
	if input.AuthToken != nil && input.ClearAuthToken {
		return nil, ErrInvalidAuthTokenUpdate
	}

	update := repository.UpdateMCPServerInput{}
	if input.Name != nil {
		name, normalizeErr := normalizeServerName(*input.Name)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		update.Name = &name
	}
	if input.BaseURL != nil {
		baseURL, normalizeErr := s.normalizeServerBaseURL(*input.BaseURL)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		update.BaseURL = &baseURL
	}
	if input.HeadersJSON != nil {
		headersJSON, mergeErr := security.MergeRedactedHeadersJSON(current.HeadersJSON, *input.HeadersJSON)
		if mergeErr != nil {
			return nil, ErrInvalidServerHeaders
		}
		if _, parseErr := inframcp.ParseHeaderTemplateJSON(headersJSON); parseErr != nil {
			return nil, ErrInvalidServerHeaders
		}
		update.HeadersJSON = &headersJSON
	}
	if input.HeadersEnabled != nil {
		headersEnabled := *input.HeadersEnabled
		update.HeadersEnabled = &headersEnabled
	}
	if input.Status != nil {
		status, normalizeErr := normalizeServerStatus(*input.Status, false)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		update.Status = &status
	}
	if input.AuthToken != nil {
		token := strings.TrimSpace(*input.AuthToken)
		if token == "" {
			return nil, ErrInvalidAuthTokenUpdate
		}
		tokenEnc, encryptErr := s.encryptToken(token)
		if encryptErr != nil {
			return nil, encryptErr
		}
		update.AuthTokenEnc = &tokenEnc
	} else if input.ClearAuthToken {
		empty := ""
		update.AuthTokenEnc = &empty
	}

	item, err := s.repo.UpdateServer(ctx, serverID, update)
	if err != nil {
		return nil, translateServerRepositoryError(err)
	}
	return item, nil
}

func (s *Service) DeleteServer(ctx context.Context, serverID uint) error {
	return translateServerRepositoryError(s.repo.DeleteServer(ctx, serverID))
}

// PreviewHeaderTemplate renders a deterministic synthetic preview without loading a user.
func (s *Service) PreviewHeaderTemplate(
	ctx context.Context,
	input PreviewHeaderTemplateInput,
) (PreviewHeaderTemplateResult, error) {
	mode := input.Mode
	if mode != inframcp.ContextModeChat &&
		mode != inframcp.ContextModeProbe &&
		mode != inframcp.ContextModeSync {
		return PreviewHeaderTemplateResult{}, ErrInvalidHeaderTemplateMode
	}
	previewContext := inframcp.TemplateContext{Mode: mode, RequestID: "request_example"}
	if mode == inframcp.ContextModeChat || mode == inframcp.ContextModeProbe {
		previewContext.UserPublicID = "user_example"
		previewContext.UserDisplayName = "name_example"
		previewContext.UserEmail = "email_example@example.test"
		previewContext.UserRole = "role_example"
	}
	if mode == inframcp.ContextModeChat {
		previewContext.ConversationPublicID = "conversation_example"
		previewContext.AssistantMessagePublicID = "assistant_message_example"
		previewContext.UserMessagePublicID = "user_message_example"
		previewContext.RunID = "run_example"
		previewContext.TraceID = "trace_example"
	}
	parsed, err := inframcp.ParseHeaderTemplateJSON(input.HeadersJSON)
	if err != nil {
		return PreviewHeaderTemplateResult{}, ErrInvalidHeaderTemplate
	}
	headers, warnings, err := inframcp.RenderHeaderTemplate(parsed.Template, previewContext)
	if err != nil {
		return PreviewHeaderTemplateResult{}, ErrInvalidHeaderTemplate
	}
	headersEnabled := input.HeadersEnabled
	signedConfigured := false
	if input.ServerID != nil {
		if s == nil || s.repo == nil {
			return PreviewHeaderTemplateResult{}, ErrMCPClientUnavailable
		}
		server, getErr := s.repo.GetServer(ctx, *input.ServerID)
		if getErr != nil {
			return PreviewHeaderTemplateResult{}, translateServerRepositoryError(getErr)
		}
		if server == nil {
			return PreviewHeaderTemplateResult{}, ErrMCPServerNotFound
		}
		signedConfigured = buildContextJWTStatus(*server, "", time.Time{}).Configured
	}
	warnings = deriveHeaderWarnings(
		warnings,
		headersEnabled,
		signedConfigured,
		parsed.Analysis.SignedContextHeader,
	)

	names := make([]string, 0, len(headers)+1)
	for name := range headers {
		names = append(names, name)
	}
	if parsed.Analysis.SignedContextHeader != "" {
		names = append(names, parsed.Analysis.SignedContextHeader)
	}
	sort.Strings(names)
	items := make([]HeaderPreviewItem, 0, len(names))
	for _, name := range names {
		if name == parsed.Analysis.SignedContextHeader {
			items = append(items, HeaderPreviewItem{
				Name: name, Value: security.RedactedHeaderValue, Sensitive: true,
			})
			continue
		}
		sensitive := security.IsSensitiveHeaderName(name)
		value := headers[name]
		if sensitive {
			value = security.RedactedHeaderValue
		}
		items = append(items, HeaderPreviewItem{Name: name, Value: value, Sensitive: sensitive})
	}
	return PreviewHeaderTemplateResult{
		Mode:                mode,
		SupportedTokens:     inframcp.SupportedHeaderTemplateTokens(),
		Warnings:            warnings,
		Headers:             items,
		SignedContextHeader: parsed.Analysis.SignedContextHeader,
	}, nil
}

func deriveHeaderWarnings(
	base []inframcp.HeaderTemplateWarning,
	headersEnabled bool,
	signedConfigured bool,
	signedContextHeader string,
) []inframcp.HeaderTemplateWarning {
	warnings := append([]inframcp.HeaderTemplateWarning(nil), base...)
	code := ""
	switch {
	case !headersEnabled && signedConfigured:
		code = "signed_context_headers_disabled"
	case headersEnabled && signedConfigured && signedContextHeader == "":
		code = "signed_context_not_referenced"
	case headersEnabled && !signedConfigured && signedContextHeader != "":
		code = "signed_context_not_configured"
	}
	if code != "" {
		warnings = append(warnings, inframcp.HeaderTemplateWarning{Code: code})
	}
	return warnings
}

// RecordAudit writes MCP server audit metadata through the shared audit service.
func (s *Service) RecordAudit(ctx context.Context, input AuditInput) {
	if s.auditWriter == nil {
		return
	}
	s.auditWriter.Write(
		ctx,
		strings.TrimSpace(input.RequestID),
		input.UserID,
		strings.TrimSpace(input.Action),
		"mcp_servers",
		strings.TrimSpace(input.ResourceID),
		strings.TrimSpace(input.ClientIP),
		strings.TrimSpace(input.UserAgent),
		input.Detail,
	)
}

// ProbeServer lists remote tools using the authoritative actor profile without mutating tools.
func (s *Service) ProbeServer(ctx context.Context, input ProbeServerInput) (ProbeServerResult, error) {
	if s.userProfileResolver == nil {
		return ProbeServerResult{}, fmt.Errorf("%w", ErrMCPServerProbeFailed)
	}
	profile, err := s.userProfileResolver.GetByID(ctx, input.ActorUserID)
	if err != nil {
		return ProbeServerResult{}, err
	}
	if profile == nil {
		return ProbeServerResult{}, fmt.Errorf("%w", ErrMCPServerProbeFailed)
	}
	server, err := s.repo.GetServer(ctx, input.ServerID)
	if err != nil {
		return ProbeServerResult{}, translateServerRepositoryError(err)
	}
	displayName := strings.TrimSpace(profile.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(profile.Username)
	}
	probeContext := inframcp.TemplateContext{
		Mode:            inframcp.ContextModeProbe,
		UserPublicID:    strings.TrimSpace(profile.PublicID),
		UserDisplayName: displayName,
		UserEmail:       strings.TrimSpace(profile.Email),
		UserRole:        strings.TrimSpace(profile.Role),
		RequestID:       strings.TrimSpace(input.RequestID),
	}
	callConfig, analysis, err := s.BuildCallConfig(ctx, *server, probeContext, mcpServerToolListTimeoutMS)
	if err != nil {
		return ProbeServerResult{}, err
	}
	if s.mcpSessions == nil {
		return ProbeServerResult{}, ErrMCPClientUnavailable
	}
	operation, closeOperation, err := s.mcpSessions.OpenEphemeral(ctx, callConfig, 0)
	if err != nil || closeOperation == nil {
		return ProbeServerResult{}, fmt.Errorf("%w", ErrMCPServerProbeFailed)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = closeOperation(cleanupCtx)
	}()
	if operation == nil {
		return ProbeServerResult{}, fmt.Errorf("%w", ErrMCPServerProbeFailed)
	}
	tools, err := operation.ListTools(ctx)
	if err != nil {
		return ProbeServerResult{}, fmt.Errorf("%w", ErrMCPServerProbeFailed)
	}
	return ProbeServerResult{ToolCount: len(tools), Analysis: analysis}, nil
}

func (s *Service) SyncServerTools(ctx context.Context, input SyncServerToolsInput) ([]domainmcp.Tool, error) {
	serverID := input.ServerID
	fail := func(returnErr error, summary string) ([]domainmcp.Tool, error) {
		s.writeToolSyncEvent(ctx, input.RequestID, "error", "mcp.tools_sync_failed", serverID, "MCP 工具同步失败", map[string]interface{}{
			"server_id": serverID,
			"error":     summary,
		})
		return nil, returnErr
	}

	server, err := s.repo.GetServer(ctx, serverID)
	if err != nil {
		translated := translateServerRepositoryError(err)
		if errors.Is(translated, ErrMCPServerNotFound) {
			return fail(ErrMCPServerNotFound, ErrMCPServerNotFound.Error())
		}
		return fail(fmt.Errorf("%w", ErrMCPServerSyncFailed), inframcp.SafeErrorSummary(err))
	}
	syncContext := inframcp.TemplateContext{
		Mode:      inframcp.ContextModeSync,
		RequestID: strings.TrimSpace(input.RequestID),
	}
	callConfig, _, err := s.BuildCallConfig(ctx, *server, syncContext, mcpServerToolListTimeoutMS)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnsafeMCPServerTarget):
			return fail(ErrUnsafeMCPServerTarget, ErrUnsafeMCPServerTarget.Error())
		case errors.Is(err, ErrInvalidHeaderTemplate):
			return fail(ErrInvalidHeaderTemplate, ErrInvalidHeaderTemplate.Error())
		default:
			return fail(fmt.Errorf("%w", ErrMCPServerSyncFailed), inframcp.SafeErrorSummary(err))
		}
	}
	if s.mcpSessions == nil {
		return fail(ErrMCPClientUnavailable, ErrMCPClientUnavailable.Error())
	}
	operation, closeOperation, err := s.mcpSessions.OpenEphemeral(ctx, callConfig, 0)
	if err != nil || closeOperation == nil {
		summary := inframcp.SafeErrorSummary(err)
		return fail(fmt.Errorf("%w", ErrMCPServerSyncFailed), summary)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = closeOperation(cleanupCtx)
	}()
	if operation == nil {
		return fail(fmt.Errorf("%w", ErrMCPServerSyncFailed), inframcp.SafeErrorSummary(nil))
	}
	tools, err := operation.ListTools(ctx)
	if err != nil {
		summary := inframcp.SafeErrorSummary(err)
		if _, persistErr := s.repo.UpdateServer(ctx, serverID, repository.UpdateMCPServerInput{LastError: &summary}); persistErr != nil {
			return fail(fmt.Errorf("%w", ErrMCPServerSyncFailed), summary)
		}
		return fail(fmt.Errorf("%w", ErrMCPServerSyncFailed), summary)
	}
	existingTools, err := s.repo.ListTools(ctx, serverID, false)
	if err != nil {
		return fail(err, inframcp.SafeErrorSummary(err))
	}
	existingToolsByName := make(map[string]domainmcp.Tool, len(existingTools))
	for _, tool := range existingTools {
		existingToolsByName[tool.Name] = tool
	}
	items := make([]domainmcp.Tool, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		schema := strings.TrimSpace(string(tool.InputSchema))
		if schema == "" {
			schema = "{}"
		}
		displayName := strings.TrimSpace(tool.Title)
		if displayName == "" {
			displayName = name
		}
		item := domainmcp.Tool{
			ServerID:            serverID,
			Name:                name,
			DisplayName:         displayName,
			Description:         strings.TrimSpace(tool.Description),
			InputSchemaJSON:     schema,
			AttachmentInputMode: domainmcp.AttachmentInputModeNone,
			Status:              "active",
		}
		if existing, ok := existingToolsByName[name]; ok {
			preserveCompatibleToolAttachmentConfig(&item, existing)
		}
		items = append(items, item)
	}
	if err = s.repo.ReplaceServerTools(ctx, serverID, items, input.OverwriteCustomizedMetadata); err != nil {
		return fail(fmt.Errorf("%w", ErrMCPServerSyncFailed), inframcp.SafeErrorSummary(err))
	}
	result, err := s.repo.ListTools(ctx, serverID, false)
	if err != nil {
		return fail(fmt.Errorf("%w", ErrMCPServerSyncFailed), inframcp.SafeErrorSummary(err))
	}
	s.writeToolSyncEvent(ctx, input.RequestID, "info", "mcp.tools_synced", serverID, "MCP 工具已同步", map[string]interface{}{
		"server_id":                     serverID,
		"tool_count":                    len(result),
		"overwrite_customized_metadata": input.OverwriteCustomizedMetadata,
	})
	return result, nil
}

func preserveCompatibleToolAttachmentConfig(discovered *domainmcp.Tool, existing domainmcp.Tool) {
	if discovered == nil {
		return
	}
	config := toolAttachmentConfig{
		Mode:           strings.TrimSpace(existing.AttachmentInputMode),
		Argument:       strings.TrimSpace(existing.AttachmentArgument),
		Encoding:       strings.TrimSpace(existing.AttachmentEncoding),
		PromptArgument: strings.TrimSpace(existing.AttachmentPromptArgument),
	}
	if validateToolAttachmentConfig(config, discovered.InputSchemaJSON) != nil {
		return
	}
	discovered.AttachmentInputMode = config.Mode
	discovered.AttachmentArgument = config.Argument
	discovered.AttachmentEncoding = config.Encoding
	discovered.AttachmentPromptArgument = config.PromptArgument
}

func (s *Service) writeToolSyncEvent(ctx context.Context, requestID string, level string, event string, serverID uint, message string, detail interface{}) {
	if s.systemEventWriter == nil {
		return
	}
	s.systemEventWriter.Write(ctx, systemeventapp.WriteInput{
		RequestID:  strings.TrimSpace(requestID),
		Level:      level,
		Source:     "mcp",
		Event:      event,
		Resource:   "mcp_server",
		ResourceID: fmt.Sprintf("%d", serverID),
		Message:    message,
		Detail:     detail,
	})
}

func (s *Service) ListTools(ctx context.Context, serverID uint, onlyActive bool) ([]domainmcp.Tool, error) {
	return s.repo.ListTools(ctx, serverID, onlyActive)
}

func (s *Service) ListAvailableTools(ctx context.Context) ([]domainmcp.Tool, error) {
	if !s.cfg.Snapshot().MCPEnable {
		return []domainmcp.Tool{}, nil
	}
	servers, err := s.repo.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]domainmcp.Tool, 0)
	for _, server := range servers {
		if server.Status != "active" {
			continue
		}
		tools, err := s.repo.ListTools(ctx, server.ID, true)
		if err != nil {
			return nil, err
		}
		for _, tool := range tools {
			tool.ServerName = server.Name
			result = append(result, tool)
		}
	}
	return result, nil
}

func (s *Service) UpdateTool(ctx context.Context, toolID uint, input ToolInput) (*domainmcp.Tool, error) {
	update, err := normalizeToolInput(input)
	if err != nil {
		return nil, err
	}
	if toolAttachmentConfigChanged(input) {
		tools, listErr := s.repo.ListToolsByIDs(ctx, []uint{toolID})
		if listErr != nil {
			return nil, listErr
		}
		if len(tools) != 1 || tools[0].ID != toolID {
			return nil, repository.ErrNotFound
		}
		config := mergedToolAttachmentConfig(tools[0], update)
		if validationErr := validateToolAttachmentConfig(config, tools[0].InputSchemaJSON); validationErr != nil {
			return nil, validationErr
		}
		if config.Mode == domainmcp.AttachmentInputModeNone {
			empty := ""
			update.AttachmentArgument = &empty
			update.AttachmentEncoding = &empty
			update.AttachmentPromptArgument = &empty
		}
	}
	return s.repo.UpdateTool(ctx, toolID, update)
}

func (s *Service) UpdateServerToolsStatus(ctx context.Context, serverID uint, toolIDs []uint, status string) ([]domainmcp.Tool, error) {
	normalized, err := normalizeToolStatus(status)
	if err != nil {
		return nil, err
	}
	if len(toolIDs) == 0 {
		return nil, ErrInvalidToolSelection
	}
	return s.repo.UpdateServerToolsStatus(ctx, serverID, toolIDs, normalized)
}

func (s *Service) ReorderServersWithTools(ctx context.Context, order []ReorderServerInput) ([]domainmcp.ServerWithTools, error) {
	if len(order) == 0 {
		return nil, ErrInvalidToolSelection
	}
	currentServers, err := s.repo.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	if len(order) != len(currentServers) {
		return nil, ErrInvalidToolSelection
	}

	allowedServers := make(map[uint]struct{}, len(currentServers))
	for _, server := range currentServers {
		allowedServers[server.ID] = struct{}{}
	}
	seenServers := make(map[uint]struct{}, len(order))
	for _, item := range order {
		if item.ServerID == 0 {
			return nil, ErrInvalidToolSelection
		}
		if _, ok := allowedServers[item.ServerID]; !ok {
			return nil, ErrInvalidToolSelection
		}
		if _, ok := seenServers[item.ServerID]; ok {
			return nil, ErrInvalidToolSelection
		}
		seenServers[item.ServerID] = struct{}{}

		currentTools, err := s.repo.ListTools(ctx, item.ServerID, false)
		if err != nil {
			return nil, err
		}
		if len(item.ToolIDs) != len(currentTools) {
			return nil, ErrInvalidToolSelection
		}
		allowedTools := make(map[uint]struct{}, len(currentTools))
		for _, tool := range currentTools {
			allowedTools[tool.ID] = struct{}{}
		}
		seenTools := make(map[uint]struct{}, len(item.ToolIDs))
		for _, toolID := range item.ToolIDs {
			if toolID == 0 {
				return nil, ErrInvalidToolSelection
			}
			if _, ok := allowedTools[toolID]; !ok {
				return nil, ErrInvalidToolSelection
			}
			if _, ok := seenTools[toolID]; ok {
				return nil, ErrInvalidToolSelection
			}
			seenTools[toolID] = struct{}{}
		}
	}
	repoOrder := make([]repository.ReorderMCPServerInput, 0, len(order))
	for _, item := range order {
		repoOrder = append(repoOrder, repository.ReorderMCPServerInput{
			ServerID: item.ServerID,
			ToolIDs:  item.ToolIDs,
		})
	}
	return s.repo.ReorderServersWithTools(ctx, repoOrder)
}

func (s *Service) normalizeCreateServerInput(input CreateServerInput) (CreateServerInput, error) {
	name, err := normalizeServerName(input.Name)
	if err != nil {
		return CreateServerInput{}, err
	}
	baseURL, err := s.normalizeServerBaseURL(input.BaseURL)
	if err != nil {
		return CreateServerInput{}, err
	}
	status, err := normalizeServerStatus(input.Status, true)
	if err != nil {
		return CreateServerInput{}, err
	}
	headersJSON := strings.TrimSpace(input.HeadersJSON)
	if headersJSON == "" {
		headersJSON = "{}"
	}
	if _, err = inframcp.ParseHeaderTemplateJSON(headersJSON); err != nil {
		return CreateServerInput{}, ErrInvalidServerHeaders
	}
	if security.ContainsRedactedHeaderValue(headersJSON) {
		return CreateServerInput{}, ErrInvalidServerHeaders
	}
	headersEnabled := true
	if input.HeadersEnabled != nil {
		headersEnabled = *input.HeadersEnabled
	}
	return CreateServerInput{
		Name:           name,
		BaseURL:        baseURL,
		AuthToken:      strings.TrimSpace(input.AuthToken),
		HeadersJSON:    headersJSON,
		HeadersEnabled: &headersEnabled,
		Status:         status,
	}, nil
}

func normalizeServerName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || len([]rune(name)) > 128 {
		return "", ErrInvalidServerName
	}
	return name, nil
}

func (s *Service) normalizeServerBaseURL(raw string) (string, error) {
	baseURL := strings.TrimSpace(raw)
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" ||
		(parsedURL.Scheme != "http" && parsedURL.Scheme != "https") ||
		parsedURL.User != nil || hasDisallowedMCPServerURLComponents(baseURL, parsedURL) {
		return "", ErrInvalidServerBaseURL
	}
	if err = s.validateServerBaseURL(baseURL); err != nil {
		return "", ErrInvalidServerBaseURL
	}
	return baseURL, nil
}

func normalizeServerStatus(raw string, defaultActive bool) (string, error) {
	status := strings.TrimSpace(raw)
	if status == "" && defaultActive {
		status = "active"
	}
	switch status {
	case "active", "inactive":
		return status, nil
	default:
		return "", ErrInvalidServerStatus
	}
}

func (s *Service) validateServerBaseURL(raw string) error {
	value := strings.TrimSpace(raw)
	parsedURL, err := url.Parse(value)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" ||
		(parsedURL.Scheme != "http" && parsedURL.Scheme != "https") ||
		parsedURL.User != nil || hasDisallowedMCPServerURLComponents(value, parsedURL) {
		return security.ErrUnsafeOutboundURL
	}
	return security.ValidateTrustedOutboundHTTPURL(value)
}

func hasDisallowedMCPServerURLComponents(raw string, parsedURL *url.URL) bool {
	return parsedURL.ForceQuery || parsedURL.RawQuery != "" ||
		parsedURL.Fragment != "" || parsedURL.RawFragment != "" || strings.Contains(raw, "#")
}

func normalizeToolInput(input ToolInput) (repository.UpdateMCPToolInput, error) {
	update := repository.UpdateMCPToolInput{}
	if input.DisplayName != nil {
		displayName := strings.TrimSpace(*input.DisplayName)
		if len([]rune(displayName)) > 160 {
			return update, ErrInvalidToolName
		}
		update.DisplayName = &displayName
	}
	if input.Description != nil {
		description := strings.TrimSpace(*input.Description)
		if len([]rune(description)) > 4096 {
			return update, ErrInvalidToolDesc
		}
		update.Description = &description
	}
	if input.Status != nil {
		status, err := normalizeToolStatus(*input.Status)
		if err != nil {
			return update, err
		}
		update.Status = &status
	}
	if input.AttachmentInputMode != nil {
		mode := strings.ToLower(strings.TrimSpace(*input.AttachmentInputMode))
		switch mode {
		case domainmcp.AttachmentInputModeNone, domainmcp.AttachmentInputModeImage:
			update.AttachmentInputMode = &mode
		default:
			return update, ErrInvalidToolAttachmentConfig
		}
	}
	if input.AttachmentArgument != nil {
		value := strings.TrimSpace(*input.AttachmentArgument)
		if len([]rune(value)) > 128 {
			return update, ErrInvalidToolAttachmentConfig
		}
		update.AttachmentArgument = &value
	}
	if input.AttachmentEncoding != nil {
		encoding := strings.ToLower(strings.TrimSpace(*input.AttachmentEncoding))
		switch encoding {
		case "", domainmcp.AttachmentEncodingBase64, domainmcp.AttachmentEncodingDataURL:
			update.AttachmentEncoding = &encoding
		default:
			return update, ErrInvalidToolAttachmentConfig
		}
	}
	if input.AttachmentPromptArgument != nil {
		value := strings.TrimSpace(*input.AttachmentPromptArgument)
		if len([]rune(value)) > 128 {
			return update, ErrInvalidToolAttachmentConfig
		}
		update.AttachmentPromptArgument = &value
	}
	return update, nil
}

type toolAttachmentConfig struct {
	Mode           string
	Argument       string
	Encoding       string
	PromptArgument string
}

func toolAttachmentConfigChanged(input ToolInput) bool {
	return input.AttachmentInputMode != nil ||
		input.AttachmentArgument != nil ||
		input.AttachmentEncoding != nil ||
		input.AttachmentPromptArgument != nil
}

func mergedToolAttachmentConfig(tool domainmcp.Tool, update repository.UpdateMCPToolInput) toolAttachmentConfig {
	config := toolAttachmentConfig{
		Mode:           strings.TrimSpace(tool.AttachmentInputMode),
		Argument:       strings.TrimSpace(tool.AttachmentArgument),
		Encoding:       strings.TrimSpace(tool.AttachmentEncoding),
		PromptArgument: strings.TrimSpace(tool.AttachmentPromptArgument),
	}
	if update.AttachmentInputMode != nil {
		config.Mode = *update.AttachmentInputMode
	}
	if update.AttachmentArgument != nil {
		config.Argument = *update.AttachmentArgument
	}
	if update.AttachmentEncoding != nil {
		config.Encoding = *update.AttachmentEncoding
	}
	if update.AttachmentPromptArgument != nil {
		config.PromptArgument = *update.AttachmentPromptArgument
	}
	if update.AttachmentInputMode != nil && *update.AttachmentInputMode == domainmcp.AttachmentInputModeNone {
		config.Argument = ""
		config.Encoding = ""
		config.PromptArgument = ""
	}
	return config
}

func validateToolAttachmentConfig(config toolAttachmentConfig, schemaJSON string) error {
	if config.Mode == domainmcp.AttachmentInputModeNone {
		if config.Argument != "" || config.Encoding != "" || config.PromptArgument != "" {
			return ErrInvalidToolAttachmentConfig
		}
		return nil
	}
	if config.Mode != domainmcp.AttachmentInputModeImage || config.Argument == "" {
		return ErrInvalidToolAttachmentConfig
	}
	switch config.Encoding {
	case domainmcp.AttachmentEncodingBase64, domainmcp.AttachmentEncodingDataURL:
	default:
		return ErrInvalidToolAttachmentConfig
	}
	if config.PromptArgument == config.Argument {
		return ErrInvalidToolAttachmentConfig
	}

	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(schemaJSON)), &schema); err != nil || len(schema.Properties) == 0 {
		return ErrInvalidToolAttachmentConfig
	}
	if !toolSchemaPropertyAcceptsString(schemaJSON, config.Argument) {
		return ErrInvalidToolAttachmentConfig
	}
	if config.PromptArgument != "" && !toolSchemaPropertyAcceptsString(schemaJSON, config.PromptArgument) {
		return ErrInvalidToolAttachmentConfig
	}
	for _, name := range schema.Required {
		name = strings.TrimSpace(name)
		if name != "" && name != config.Argument && name != config.PromptArgument {
			return ErrInvalidToolAttachmentConfig
		}
	}
	return nil
}

func normalizeToolStatus(status string) (string, error) {
	normalized := strings.TrimSpace(status)
	switch normalized {
	case "active", "inactive":
		return normalized, nil
	default:
		return "", ErrInvalidToolStatus
	}
}

func (s *Service) encryptToken(token string) (string, error) {
	return secretbox.EncryptString(s.cfg.Snapshot().DataEncryptionKey, token)
}

func (s *Service) decryptToken(encrypted string) (string, error) {
	return secretbox.DecryptString(s.cfg.Snapshot().DataEncryptionKey, encrypted)
}

func translateServerRepositoryError(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return ErrMCPServerNotFound
	}
	return err
}
