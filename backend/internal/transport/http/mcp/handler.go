package mcp

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	appmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/mcp"
	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	inframcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/response"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/security"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/transport/http/middleware"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

type Handler struct {
	service *appmcp.Service
}

func NewHandler(service *appmcp.Service) *Handler {
	return &Handler{service: service}
}

// ListServers godoc
// @Summary List MCP servers
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Success 200 {object} ServerListResponseDoc
// @Failure 500 {object} response.Envelope
// @Router /admin/mcp/servers [get]
func (h *Handler) ListServers(c *gin.Context) {
	items, err := h.service.ListServers(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "list mcp servers failed")
		return
	}
	results := make([]ServerResponse, 0, len(items))
	for _, item := range items {
		results = append(results, toServerResponse(item))
	}
	response.Success(c, ServerListResponse{Results: results})
}

// ListAvailableTools godoc
// @Summary List available MCP tools
// @Tags mcp
// @Produce json
// @Security BearerAuth
// @Success 200 {object} ToolListResponseDoc
// @Failure 500 {object} response.Envelope
// @Router /mcp/tools [get]
func (h *Handler) ListAvailableTools(c *gin.Context) {
	items, err := h.service.ListAvailableTools(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "list mcp tools failed")
		return
	}
	results := make([]ToolResponse, 0, len(items))
	for _, item := range items {
		results = append(results, toToolResponse(item))
	}
	response.Success(c, ToolListResponse{Results: results})
}

// PreviewHeaderTemplate godoc
// @Summary Preview an MCP Header template
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body PreviewHeaderTemplateRequest true "Preview input"
// @Success 200 {object} HeaderTemplatePreviewResponseDoc
// @Failure 400 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /admin/mcp/header-templates/preview [post]
func (h *Handler) PreviewHeaderTemplate(c *gin.Context) {
	var req PreviewHeaderTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if writePreviewValidationError(c, err) {
			return
		}
		response.InvalidRequestBody(c, err)
		return
	}
	result, err := h.service.PreviewHeaderTemplate(
		c.Request.Context(),
		req.HeadersJSON,
		inframcp.ContextMode(req.Mode),
	)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	warnings := toHeaderTemplateWarningResponses(result.Warnings)
	headers := make([]HeaderPreviewItemResponse, 0, len(result.Headers))
	for _, item := range result.Headers {
		headers = append(headers, HeaderPreviewItemResponse{
			Name: item.Name, Value: item.Value, Sensitive: item.Sensitive,
		})
	}
	response.Success(c, HeaderTemplatePreviewResponse{
		Mode: string(result.Mode), SupportedTokens: result.SupportedTokens,
		Warnings: warnings, Headers: headers,
	})
}

func writePreviewValidationError(c *gin.Context, err error) bool {
	var validationErrors validator.ValidationErrors
	if !errors.As(err, &validationErrors) {
		return false
	}
	hasHeaderTemplateError := false
	for _, fieldErr := range validationErrors {
		switch fieldErr.StructField() {
		case "Mode":
			writeMCPPublicError(c, appmcp.ErrInvalidHeaderTemplateMode)
			return true
		case "HeadersJSON":
			hasHeaderTemplateError = true
		}
	}
	if hasHeaderTemplateError {
		writeMCPPublicError(c, appmcp.ErrInvalidHeaderTemplate)
		return true
	}
	return false
}

// CreateServer godoc
// @Summary Create an MCP server
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreateServerRequest true "Server input"
// @Success 200 {object} ServerDataResponseDoc
// @Failure 400 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /admin/mcp/servers [post]
func (h *Handler) CreateServer(c *gin.Context) {
	var req CreateServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidRequestBody(c, err)
		return
	}
	audit := newMCPAuditInput(c, "mcp.server.create", "")
	item, err := h.service.CreateServer(c.Request.Context(), appmcp.CreateServerInput{
		Name:        req.Name,
		BaseURL:     req.BaseURL,
		AuthToken:   req.AuthToken,
		HeadersJSON: req.HeadersJSON,
		Status:      req.Status,
	})
	if err != nil {
		recordMCPAuditError(h.service, c, audit, err)
		writeServiceError(c, err)
		return
	}
	audit.ResourceID = strconv.FormatUint(uint64(item.ID), 10)
	audit.Detail = map[string]interface{}{
		"outcome":       "success",
		"changedFields": createServerChangedFields(req),
	}
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, ServerDataResponse{Server: toServerResponse(*item)})
}

// UpdateServer godoc
// @Summary Update an MCP server
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Server ID"
// @Param body body UpdateServerRequest true "Partial server input"
// @Success 200 {object} ServerDataResponseDoc
// @Failure 400 {object} response.Envelope
// @Failure 404 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /admin/mcp/servers/{id} [patch]
func (h *Handler) UpdateServer(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	var req UpdateServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidRequestBody(c, err)
		return
	}
	audit := newMCPAuditInput(c, "mcp.server.update", strconv.FormatUint(uint64(serverID), 10))
	item, err := h.service.UpdateServer(c.Request.Context(), serverID, appmcp.UpdateServerInput{
		Name:           req.Name,
		BaseURL:        req.BaseURL,
		AuthToken:      req.AuthToken,
		ClearAuthToken: req.ClearAuthToken,
		HeadersJSON:    req.HeadersJSON,
		Status:         req.Status,
	})
	if err != nil {
		recordMCPAuditError(h.service, c, audit, err)
		writeServiceError(c, err)
		return
	}
	audit.Detail = map[string]interface{}{
		"outcome":       "success",
		"changedFields": updateServerChangedFields(req),
	}
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, ServerDataResponse{Server: toServerResponse(*item)})
}

// DeleteServer godoc
// @Summary Delete an MCP server
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Param id path int true "Server ID"
// @Success 200 {object} DeleteServerResponseDoc
// @Failure 400 {object} response.Envelope
// @Failure 404 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /admin/mcp/servers/{id} [delete]
func (h *Handler) DeleteServer(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	audit := newMCPAuditInput(c, "mcp.server.delete", strconv.FormatUint(uint64(serverID), 10))
	if err := h.service.DeleteServer(c.Request.Context(), serverID); err != nil {
		recordMCPAuditError(h.service, c, audit, err)
		writeServiceError(c, err)
		return
	}
	audit.Detail = map[string]interface{}{"outcome": "success"}
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, DeleteServerResponse{Deleted: true})
}

// SyncServerTools godoc
// @Summary Synchronize tools from an MCP server
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Param id path int true "Server ID"
// @Success 200 {object} ToolListResponseDoc
// @Failure 400 {object} response.Envelope
// @Failure 404 {object} response.Envelope
// @Failure 502 {object} response.Envelope
// @Failure 503 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /admin/mcp/servers/{id}/sync [post]
func (h *Handler) SyncServerTools(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	audit := newMCPAuditInput(c, "mcp.server.sync", strconv.FormatUint(uint64(serverID), 10))
	items, err := h.service.SyncServerTools(c.Request.Context(), appmcp.SyncServerToolsInput{
		ServerID: serverID, RequestID: audit.RequestID,
	})
	if err != nil {
		recordMCPAuditError(h.service, c, audit, err)
		writeServiceError(c, err)
		return
	}
	results := make([]ToolResponse, 0, len(items))
	for _, item := range items {
		results = append(results, toToolResponse(item))
	}
	audit.Detail = map[string]interface{}{
		"outcome": "success", "toolCount": len(items),
	}
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, ToolListResponse{Results: results})
}

// ProbeServer godoc
// @Summary Probe an MCP server
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Param id path int true "Server ID"
// @Success 200 {object} ProbeServerResponseDoc
// @Failure 400 {object} response.Envelope
// @Failure 404 {object} response.Envelope
// @Failure 502 {object} response.Envelope
// @Failure 503 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /admin/mcp/servers/{id}/probe [post]
func (h *Handler) ProbeServer(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	audit := newMCPAuditInput(c, "mcp.server.probe", strconv.FormatUint(uint64(serverID), 10))
	result, err := h.service.ProbeServer(c.Request.Context(), appmcp.ProbeServerInput{
		ServerID: serverID, ActorUserID: audit.UserID, RequestID: audit.RequestID,
	})
	if err != nil {
		recordMCPAuditError(h.service, c, audit, err)
		writeServiceError(c, err)
		return
	}
	audit.Detail = map[string]interface{}{
		"outcome": "success", "toolCount": result.ToolCount,
		"warningCodes": warningCodes(result.Analysis.Warnings),
	}
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, ProbeServerResponse{
		ToolCount: result.ToolCount,
		Warnings:  toHeaderTemplateWarningResponses(result.Analysis.Warnings),
	})
}

// ListServerTools godoc
// @Summary List tools for an MCP server
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Param id path int true "Server ID"
// @Success 200 {object} ToolListResponseDoc
// @Failure 400 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /admin/mcp/servers/{id}/tools [get]
func (h *Handler) ListServerTools(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	items, err := h.service.ListTools(c.Request.Context(), serverID, false)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "list mcp tools failed")
		return
	}
	results := make([]ToolResponse, 0, len(items))
	for _, item := range items {
		results = append(results, toToolResponse(item))
	}
	response.Success(c, ToolListResponse{Results: results})
}

// UpdateTool godoc
// @Summary Update an MCP tool
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Tool ID"
// @Param body body UpdateToolRequest true "Tool input"
// @Success 200 {object} ToolDataResponseDoc
// @Failure 400 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /admin/mcp/tools/{id} [patch]
func (h *Handler) UpdateTool(c *gin.Context) {
	toolID, ok := parseIDParam(c, "id", "mcp tool")
	if !ok {
		return
	}
	var req UpdateToolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidRequestBody(c, err)
		return
	}
	item, err := h.service.UpdateTool(c.Request.Context(), toolID, appmcp.ToolInput{
		DisplayName: req.DisplayName,
		Description: req.Description,
		Status:      req.Status,
	})
	if err != nil {
		writeServiceError(c, err)
		return
	}
	response.Success(c, toToolResponse(*item))
}

// UpdateServerToolsStatus godoc
// @Summary Update MCP server tool statuses
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Server ID"
// @Param body body UpdateServerToolsStatusRequest true "Status update"
// @Success 200 {object} ToolListResponseDoc
// @Failure 400 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /admin/mcp/servers/{id}/tools/status [patch]
func (h *Handler) UpdateServerToolsStatus(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	var req UpdateServerToolsStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidRequestBody(c, err)
		return
	}
	audit := newMCPAuditInput(c, "mcp.server.status_update", strconv.FormatUint(uint64(serverID), 10))
	items, err := h.service.UpdateServerToolsStatus(c.Request.Context(), serverID, req.ToolIDs, req.Status)
	if err != nil {
		recordMCPAuditError(h.service, c, audit, err)
		writeServiceError(c, err)
		return
	}
	results := make([]ToolResponse, 0, len(items))
	for _, item := range items {
		results = append(results, toToolResponse(item))
	}
	audit.Detail = map[string]interface{}{
		"outcome":       "success",
		"changedFields": []string{"status"},
		"toolCount":     len(items),
	}
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, ToolListResponse{Results: results})
}

// ReorderServers godoc
// @Summary Reorder MCP servers and tools
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body ReorderServersRequest true "Server and tool order"
// @Success 200 {object} ServerToolOrderListResponseDoc
// @Failure 400 {object} response.Envelope
// @Failure 500 {object} response.Envelope
// @Router /admin/mcp/servers/order [patch]
func (h *Handler) ReorderServers(c *gin.Context) {
	var req ReorderServersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidRequestBody(c, err)
		return
	}
	input := make([]appmcp.ReorderServerInput, 0, len(req.Servers))
	for _, item := range req.Servers {
		input = append(input, appmcp.ReorderServerInput{
			ServerID: item.ServerID,
			ToolIDs:  item.ToolIDs,
		})
	}
	audit := newMCPAuditInput(c, "mcp.server.reorder", "")
	items, err := h.service.ReorderServersWithTools(c.Request.Context(), input)
	if err != nil {
		recordMCPAuditError(h.service, c, audit, err)
		writeServiceError(c, err)
		return
	}
	results := make([]ServerToolOrderResponse, 0, len(items))
	for _, item := range items {
		tools := make([]ToolResponse, 0, len(item.Tools))
		for _, tool := range item.Tools {
			tools = append(tools, toToolResponse(tool))
		}
		results = append(results, ServerToolOrderResponse{
			Server: toServerResponse(item.Server),
			Tools:  tools,
		})
	}
	audit.Detail = map[string]interface{}{
		"outcome": "success", "serverCount": len(items),
		"toolCount": serverToolCount(items),
	}
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, ServerToolOrderListResponse{Results: results})
}

func toHeaderTemplateWarningResponses(items []inframcp.HeaderTemplateWarning) []HeaderTemplateWarningResponse {
	result := make([]HeaderTemplateWarningResponse, 0, len(items))
	for _, item := range items {
		result = append(result, HeaderTemplateWarningResponse{
			Code: item.Code, HeaderName: item.HeaderName, Token: item.Token,
		})
	}
	return result
}

func warningCodes(items []inframcp.HeaderTemplateWarning) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		code := strings.TrimSpace(item.Code)
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		result = append(result, code)
	}
	sort.Strings(result)
	return result
}

func newMCPAuditInput(c *gin.Context, action string, resourceID string) appmcp.AuditInput {
	return appmcp.AuditInput{
		UserID: middleware.MustUserID(c), RequestID: middleware.MustRequestID(c),
		Action: action, ResourceID: resourceID,
		ClientIP: c.ClientIP(), UserAgent: c.Request.UserAgent(),
	}
}

func recordMCPAuditError(service *appmcp.Service, c *gin.Context, audit appmcp.AuditInput, err error) {
	audit.Detail = map[string]interface{}{
		"outcome": "error", "errorCode": publicMCPErrorCode(err),
	}
	service.RecordAudit(c.Request.Context(), audit)
}

func createServerChangedFields(req CreateServerRequest) []string {
	fields := []string{"baseURL", "name"}
	if strings.TrimSpace(req.AuthToken) != "" {
		fields = append(fields, "authToken")
	}
	if strings.TrimSpace(req.HeadersJSON) != "" {
		fields = append(fields, "headersJSON")
	}
	if strings.TrimSpace(req.Status) != "" {
		fields = append(fields, "status")
	}
	sort.Strings(fields)
	return fields
}

func updateServerChangedFields(req UpdateServerRequest) []string {
	fields := make([]string, 0, 5)
	if req.Name != nil {
		fields = append(fields, "name")
	}
	if req.BaseURL != nil {
		fields = append(fields, "baseURL")
	}
	if req.AuthToken != nil || req.ClearAuthToken {
		fields = append(fields, "authToken")
	}
	if req.HeadersJSON != nil {
		fields = append(fields, "headersJSON")
	}
	if req.Status != nil {
		fields = append(fields, "status")
	}
	sort.Strings(fields)
	return fields
}

func serverToolCount(items []domainmcp.ServerWithTools) int {
	total := 0
	for _, item := range items {
		total += len(item.Tools)
	}
	return total
}

func parseIDParam(c *gin.Context, key string, resource string) (uint, bool) {
	raw := c.Param(key)
	parsed, err := strconv.ParseUint(raw, 10, strconv.IntSize)
	if err != nil || parsed == 0 {
		response.Error(c, http.StatusBadRequest, "invalid "+resource+" id")
		return 0, false
	}
	return uint(parsed), true
}

func publicMCPErrorCode(err error) string {
	switch {
	case errors.Is(err, appmcp.ErrInvalidHeaderTemplate):
		return response.CodeMCPHeaderTemplateInvalid
	case errors.Is(err, appmcp.ErrInvalidHeaderTemplateMode):
		return response.CodeMCPHeaderTemplateInvalidMode
	case errors.Is(err, appmcp.ErrInvalidAuthTokenUpdate):
		return response.CodeMCPServerInvalidAuthUpdate
	case errors.Is(err, appmcp.ErrMCPServerNotFound):
		return response.CodeMCPServerNotFound
	case errors.Is(err, appmcp.ErrUnsafeMCPServerTarget):
		return response.CodeMCPServerUnsafeTarget
	case errors.Is(err, appmcp.ErrMCPServerProbeFailed):
		return response.CodeMCPServerProbeFailed
	case errors.Is(err, appmcp.ErrMCPServerSyncFailed):
		return response.CodeMCPServerSyncFailed
	default:
		return response.CodeInternal
	}
}

func writeMCPPublicError(c *gin.Context, err error) {
	code := publicMCPErrorCode(err)
	switch code {
	case response.CodeMCPHeaderTemplateInvalid,
		response.CodeMCPHeaderTemplateInvalidMode,
		response.CodeMCPServerInvalidAuthUpdate,
		response.CodeMCPServerUnsafeTarget:
		response.ErrorWithCode(c, http.StatusBadRequest, code, "invalid mcp request")
	case response.CodeMCPServerNotFound:
		response.ErrorWithCode(c, http.StatusNotFound, code, "mcp server not found")
	case response.CodeMCPServerProbeFailed, response.CodeMCPServerSyncFailed:
		response.ErrorWithCode(c, http.StatusBadGateway, code, "mcp remote operation failed")
	default:
		if errors.Is(err, appmcp.ErrMCPClientUnavailable) {
			response.ErrorWithCode(c, http.StatusServiceUnavailable, "mcp.client_unavailable", "mcp client unavailable")
			return
		}
		response.ErrorWithCode(c, http.StatusInternalServerError, response.CodeInternal, "internal server error")
	}
}

func writeServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, appmcp.ErrInvalidHeaderTemplate),
		errors.Is(err, appmcp.ErrInvalidHeaderTemplateMode),
		errors.Is(err, appmcp.ErrInvalidAuthTokenUpdate),
		errors.Is(err, appmcp.ErrMCPServerNotFound),
		errors.Is(err, appmcp.ErrUnsafeMCPServerTarget),
		errors.Is(err, appmcp.ErrMCPServerProbeFailed),
		errors.Is(err, appmcp.ErrMCPServerSyncFailed),
		errors.Is(err, appmcp.ErrMCPClientUnavailable):
		writeMCPPublicError(c, err)
	case errors.Is(err, appmcp.ErrInvalidServerName),
		errors.Is(err, appmcp.ErrInvalidServerBaseURL),
		errors.Is(err, appmcp.ErrInvalidServerStatus),
		errors.Is(err, appmcp.ErrInvalidServerHeaders),
		errors.Is(err, appmcp.ErrInvalidToolStatus),
		errors.Is(err, appmcp.ErrInvalidToolName),
		errors.Is(err, appmcp.ErrInvalidToolDesc),
		errors.Is(err, appmcp.ErrInvalidToolSelection):
		response.ErrorFrom(c, http.StatusBadRequest, err)
	default:
		response.ErrorFrom(c, http.StatusInternalServerError, err)
	}
}

func toServerResponse(item domainmcp.Server) ServerResponse {
	return ServerResponse{
		ID:                  item.ID,
		Name:                item.Name,
		BaseURL:             item.BaseURL,
		AuthTokenConfigured: strings.TrimSpace(item.AuthTokenEnc) != "",
		HeadersJSON:         security.RedactHeadersJSON(item.HeadersJSON),
		Status:              item.Status,
		SortOrder:           item.SortOrder,
		ToolCount:           item.ToolCount,
		ActiveToolCount:     item.ActiveToolCount,
		LastSyncedAt:        item.LastSyncedAt,
		LastError:           item.LastError,
		CreatedAt:           item.CreatedAt,
		UpdatedAt:           item.UpdatedAt,
	}
}

func toToolResponse(item domainmcp.Tool) ToolResponse {
	return ToolResponse{
		ID:              item.ID,
		ServerID:        item.ServerID,
		ServerName:      item.ServerName,
		Name:            item.Name,
		DisplayName:     item.DisplayName,
		Description:     item.Description,
		InputSchemaJSON: item.InputSchemaJSON,
		Status:          item.Status,
		SortOrder:       item.SortOrder,
		CreatedAt:       item.CreatedAt,
		UpdatedAt:       item.UpdatedAt,
	}
}
