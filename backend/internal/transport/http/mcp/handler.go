package mcp

import (
	"errors"
	"net/http"
	"regexp"
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
// @Summary 获取 MCP 服务列表
// @Description 管理员查看已配置的 MCP 服务及其工具统计
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Success 200 {object} ServerListResponseDoc
// @Failure 500 {object} ErrorDoc
// @Router /admin/mcp/servers [get]
func (h *Handler) ListServers(c *gin.Context) {
	items, err := h.service.ListServers(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "list mcp servers failed")
		return
	}
	results := make([]ServerResponse, 0, len(items))
	for _, item := range items {
		results = append(results, toServerResponse(h.service.DescribeServer(item)))
	}
	response.Success(c, ServerListResponse{Results: results})
}

// ListAvailableTools godoc
// @Summary 获取可用 MCP 工具
// @Description 获取当前聊天侧可选择的 MCP 工具
// @Tags mcp
// @Produce json
// @Security BearerAuth
// @Success 200 {object} ToolListResponseDoc
// @Failure 500 {object} ErrorDoc
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
// @Summary 预览 MCP Header 模板
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body PreviewHeaderTemplateRequest true "Preview input"
// @Success 200 {object} HeaderTemplatePreviewResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
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
		appmcp.PreviewHeaderTemplateInput{
			HeadersJSON:    req.HeadersJSON,
			HeadersEnabled: *req.HeadersEnabled,
			ServerID:       req.ServerID,
			Mode:           inframcp.ContextMode(req.Mode),
		},
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
		Warnings: warnings, Headers: headers, SignedContextHeader: result.SignedContextHeader,
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
// @Summary 创建 MCP 服务
// @Description 管理员创建一个 MCP 服务配置
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreateServerRequest true "MCP 服务配置"
// @Success 200 {object} ServerDataResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
// @Router /admin/mcp/servers [post]
func (h *Handler) CreateServer(c *gin.Context) {
	var req CreateServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidRequestBody(c, err)
		return
	}
	audit := newMCPAuditInput(c, "mcp.server.create", "")
	item, err := h.service.CreateServer(c.Request.Context(), appmcp.CreateServerInput{
		Name:           req.Name,
		BaseURL:        req.BaseURL,
		AuthToken:      req.AuthToken,
		HeadersJSON:    req.HeadersJSON,
		HeadersEnabled: req.HeadersEnabled,
		Status:         req.Status,
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
	response.Success(c, ServerDataResponse{Server: toServerResponse(h.service.DescribeServer(*item))})
}

// UpdateServer godoc
// @Summary 更新 MCP 服务
// @Description 管理员更新一个 MCP 服务配置
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "MCP 服务 ID"
// @Param body body UpdateServerRequest true "MCP 服务局部配置"
// @Success 200 {object} ServerDataResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 404 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
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
		HeadersEnabled: req.HeadersEnabled,
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
	response.Success(c, ServerDataResponse{Server: toServerResponse(h.service.DescribeServer(*item))})
}

// UpdateContextJWT godoc
// @Summary Update MCP context JWT policy
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Server ID"
// @Param body body UpdateContextJWTRequest true "Context JWT policy"
// @Success 200 {object} ContextJWTStatusResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 404 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
// @Failure 503 {object} ErrorDoc
// @Router /admin/mcp/servers/{id}/context-jwt [patch]
func (h *Handler) UpdateContextJWT(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	audit := newMCPAuditInput(
		c,
		"mcp.context_jwt.policy_update",
		strconv.FormatUint(uint64(serverID), 10),
	)
	var req UpdateContextJWTRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidRequestBody(c, err)
		audit.Detail = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
			Outcome:   "error",
			ErrorCode: response.CodeRequestInvalidBody,
		})
		h.service.RecordAudit(c.Request.Context(), audit)
		return
	}
	status, err := h.service.UpdateContextJWTPolicy(
		c.Request.Context(),
		serverID,
		appmcp.ContextJWTPolicyInput{
			ExpiresSeconds: req.ExpiresSeconds,
			IncludeName:    req.IncludeName,
			IncludeEmail:   req.IncludeEmail,
			IncludeRole:    req.IncludeRole,
		},
	)
	if err != nil {
		code := writeContextJWTServiceError(c, err)
		audit.Detail = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
			Outcome:   "error",
			ErrorCode: code,
			Policy:    &req,
		})
		h.service.RecordAudit(c.Request.Context(), audit)
		return
	}
	audit.Detail = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
		Outcome:        "success",
		ServerPublicID: status.ServerPublicID,
		KeyID:          status.KeyID,
		Policy:         &req,
	})
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, toContextJWTStatusResponse(status))
}

// PrepareContextJWTRotation godoc
// @Summary Prepare MCP context JWT key rotation
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Param id path int true "Server ID"
// @Success 200 {object} PrepareContextJWTRotationResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 404 {object} ErrorDoc
// @Failure 409 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
// @Failure 503 {object} ErrorDoc
// @Router /admin/mcp/servers/{id}/context-jwt/rotations [post]
func (h *Handler) PrepareContextJWTRotation(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	audit := newMCPAuditInput(
		c,
		"mcp.context_jwt.rotation_prepare",
		strconv.FormatUint(uint64(serverID), 10),
	)
	result, err := h.service.PrepareContextJWTRotation(c.Request.Context(), serverID)
	if err != nil {
		code := writeContextJWTServiceError(c, err)
		audit.Detail = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
			Outcome:   "error",
			ErrorCode: code,
		})
		h.service.RecordAudit(c.Request.Context(), audit)
		return
	}
	audit.Detail = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
		Outcome:        "success",
		ServerPublicID: result.ServerPublicID,
		KeyID:          result.KeyID,
	})
	h.service.RecordAudit(c.Request.Context(), audit)
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	response.Success(c, toPrepareContextJWTRotationResponse(result))
}

// ActivateContextJWTRotation godoc
// @Summary Activate MCP context JWT key rotation
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Param id path int true "Server ID"
// @Param kid path string true "Pending key ID"
// @Success 200 {object} ContextJWTStatusResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 404 {object} ErrorDoc
// @Failure 409 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
// @Failure 503 {object} ErrorDoc
// @Router /admin/mcp/servers/{id}/context-jwt/rotations/{kid}/activate [post]
func (h *Handler) ActivateContextJWTRotation(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	audit := newMCPAuditInput(
		c,
		"mcp.context_jwt.rotation_activate",
		strconv.FormatUint(uint64(serverID), 10),
	)
	status, err := h.service.ActivateContextJWTRotation(
		c.Request.Context(),
		serverID,
		strings.TrimSpace(c.Param("kid")),
	)
	if err != nil {
		code := writeContextJWTServiceError(c, err)
		audit.Detail = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
			Outcome:   "error",
			ErrorCode: code,
		})
		h.service.RecordAudit(c.Request.Context(), audit)
		return
	}
	audit.Detail = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
		Outcome:        "success",
		ServerPublicID: status.ServerPublicID,
		KeyID:          status.KeyID,
	})
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, toContextJWTStatusResponse(status))
}

// CancelContextJWTRotation godoc
// @Summary Cancel MCP context JWT key rotation
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Param id path int true "Server ID"
// @Param kid path string true "Pending key ID"
// @Success 200 {object} ContextJWTStatusResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 404 {object} ErrorDoc
// @Failure 409 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
// @Failure 503 {object} ErrorDoc
// @Router /admin/mcp/servers/{id}/context-jwt/rotations/{kid} [delete]
func (h *Handler) CancelContextJWTRotation(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	kid := strings.TrimSpace(c.Param("kid"))
	audit := newMCPAuditInput(
		c,
		"mcp.context_jwt.rotation_cancel",
		strconv.FormatUint(uint64(serverID), 10),
	)
	status, err := h.service.CancelContextJWTRotation(c.Request.Context(), serverID, kid)
	if err != nil {
		code := writeContextJWTServiceError(c, err)
		audit.Detail = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
			Outcome:   "error",
			ErrorCode: code,
		})
		h.service.RecordAudit(c.Request.Context(), audit)
		return
	}
	audit.Detail = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
		Outcome:        "success",
		ServerPublicID: status.ServerPublicID,
		KeyID:          kid,
	})
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, toContextJWTStatusResponse(status))
}

// DisableContextJWT godoc
// @Summary Disable MCP context JWT
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Param id path int true "Server ID"
// @Success 200 {object} ContextJWTStatusResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 404 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
// @Failure 503 {object} ErrorDoc
// @Router /admin/mcp/servers/{id}/context-jwt [delete]
func (h *Handler) DisableContextJWT(c *gin.Context) {
	serverID, ok := parseIDParam(c, "id", "mcp server")
	if !ok {
		return
	}
	audit := newMCPAuditInput(
		c,
		"mcp.context_jwt.disable",
		strconv.FormatUint(uint64(serverID), 10),
	)
	status, err := h.service.DisableContextJWT(c.Request.Context(), serverID)
	if err != nil {
		code := writeContextJWTServiceError(c, err)
		audit.Detail = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
			Outcome:   "error",
			ErrorCode: code,
		})
		h.service.RecordAudit(c.Request.Context(), audit)
		return
	}
	audit.Detail = buildContextJWTAuditDetail(contextJWTAuditDetailInput{
		Outcome:        "success",
		ServerPublicID: status.ServerPublicID,
		KeyID:          status.KeyID,
	})
	h.service.RecordAudit(c.Request.Context(), audit)
	response.Success(c, toContextJWTStatusResponse(status))
}

// DeleteServer godoc
// @Summary 删除 MCP 服务
// @Description 管理员删除一个 MCP 服务及其工具
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Param id path int true "MCP 服务 ID"
// @Success 200 {object} DeleteServerResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 404 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
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
// @Summary 同步 MCP 工具
// @Description 管理员从 MCP 服务同步工具定义
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Param id path int true "MCP 服务 ID"
// @Success 200 {object} ToolListResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 404 {object} ErrorDoc
// @Failure 502 {object} ErrorDoc
// @Failure 503 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
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
// @Failure 400 {object} ErrorDoc
// @Failure 404 {object} ErrorDoc
// @Failure 502 {object} ErrorDoc
// @Failure 503 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
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
// @Summary 获取 MCP 服务工具
// @Description 管理员查看指定 MCP 服务已同步的工具
// @Tags admin-mcp
// @Produce json
// @Security BearerAuth
// @Param id path int true "MCP 服务 ID"
// @Success 200 {object} ToolListResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
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
// @Summary 更新 MCP 工具
// @Description 管理员更新 MCP 工具的展示信息或状态
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "MCP 工具 ID"
// @Param body body UpdateToolRequest true "MCP 工具配置"
// @Success 200 {object} ToolResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
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
// @Summary 批量更新 MCP 工具状态
// @Description 管理员批量启用或停用指定 MCP 服务的工具
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "MCP 服务 ID"
// @Param body body UpdateServerToolsStatusRequest true "MCP 工具状态"
// @Success 200 {object} ToolListResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
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
// @Summary 调整 MCP 服务及工具顺序
// @Description 管理员保存 MCP 服务及其工具的展示顺序
// @Tags admin-mcp
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body ReorderServersRequest true "MCP 排序配置"
// @Success 200 {object} ServerToolOrderListResponseDoc
// @Failure 400 {object} ErrorDoc
// @Failure 500 {object} ErrorDoc
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
			Server: toServerResponse(h.service.DescribeServer(item.Server)),
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

type contextJWTAuditDetailInput struct {
	Outcome        string
	ErrorCode      string
	ServerPublicID string
	KeyID          string
	Policy         *UpdateContextJWTRequest
}

var (
	safeMCPPublicIDAuditPattern = regexp.MustCompile(`^mcp_[A-Za-z0-9_-]+$`)
	safeContextKIDAuditPattern  = regexp.MustCompile(`^ctx_[A-Za-z0-9_-]+$`)
)

func buildContextJWTAuditDetail(input contextJWTAuditDetailInput) map[string]interface{} {
	detail := map[string]interface{}{"outcome": input.Outcome}
	if input.ErrorCode != "" {
		detail["error_code"] = input.ErrorCode
	}
	if len(input.ServerPublicID) <= 128 && safeMCPPublicIDAuditPattern.MatchString(input.ServerPublicID) {
		detail["server_public_id"] = input.ServerPublicID
	}
	if len(input.KeyID) <= 64 && safeContextKIDAuditPattern.MatchString(input.KeyID) {
		detail["kid"] = input.KeyID
	}
	if input.Policy != nil {
		detail["expires_seconds"] = input.Policy.ExpiresSeconds
		detail["include_name"] = input.Policy.IncludeName
		detail["include_email"] = input.Policy.IncludeEmail
		detail["include_role"] = input.Policy.IncludeRole
	}
	return detail
}

func writeContextJWTServiceError(c *gin.Context, err error) string {
	code := response.CodeInternal
	status := http.StatusInternalServerError
	message := "internal server error"
	switch {
	case errors.Is(err, appmcp.ErrMCPContextJWTInvalidPolicy):
		code = response.CodeMCPContextJWTInvalidPolicy
		status = http.StatusBadRequest
		message = "invalid mcp context jwt policy"
	case errors.Is(err, appmcp.ErrMCPContextJWTUnavailable):
		code = response.CodeMCPContextJWTUnavailable
		status = http.StatusServiceUnavailable
		message = "mcp context jwt unavailable"
	case errors.Is(err, appmcp.ErrMCPContextJWTPendingExists):
		code = response.CodeMCPContextJWTPendingExists
		status = http.StatusConflict
		message = "mcp context jwt pending rotation exists"
	case errors.Is(err, appmcp.ErrMCPContextJWTRotationConflict):
		code = response.CodeMCPContextJWTRotationConflict
		status = http.StatusConflict
		message = "mcp context jwt rotation conflict"
	case errors.Is(err, appmcp.ErrMCPContextJWTRotationExpired):
		code = response.CodeMCPContextJWTRotationExpired
		status = http.StatusConflict
		message = "mcp context jwt rotation expired"
	case errors.Is(err, appmcp.ErrMCPContextJWTInvalidStorage):
		code = response.CodeMCPContextJWTInvalidStorage
	case errors.Is(err, appmcp.ErrMCPServerNotFound):
		code = response.CodeMCPServerNotFound
		status = http.StatusNotFound
		message = "mcp server not found"
	}
	response.ErrorWithCode(c, status, code, message)
	return code
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
	if req.HeadersEnabled != nil {
		fields = append(fields, "headersEnabled")
	}
	if strings.TrimSpace(req.Status) != "" {
		fields = append(fields, "status")
	}
	sort.Strings(fields)
	return fields
}

func updateServerChangedFields(req UpdateServerRequest) []string {
	fields := make([]string, 0, 6)
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
	if req.HeadersEnabled != nil {
		fields = append(fields, "headersEnabled")
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

func toServerResponse(view appmcp.ServerView) ServerResponse {
	item := view.Server
	return ServerResponse{
		ID:                  item.ID,
		PublicID:            item.PublicID,
		Name:                item.Name,
		BaseURL:             item.BaseURL,
		AuthTokenConfigured: strings.TrimSpace(item.AuthTokenEnc) != "",
		HeadersJSON:         security.RedactHeadersJSON(item.HeadersJSON),
		HeadersEnabled:      item.HeadersEnabled,
		HeaderWarnings:      toHeaderTemplateWarningResponses(view.HeaderWarnings),
		SignedContextHeader: view.SignedContextHeader,
		Status:              item.Status,
		SortOrder:           item.SortOrder,
		ToolCount:           item.ToolCount,
		ActiveToolCount:     item.ActiveToolCount,
		LastSyncedAt:        item.LastSyncedAt,
		LastError:           item.LastError,
		CreatedAt:           item.CreatedAt,
		UpdatedAt:           item.UpdatedAt,
		ContextJWT:          toContextJWTStatusResponse(view.ContextJWT),
	}
}

func toContextJWTStatusResponse(status appmcp.ContextJWTStatus) ContextJWTStatusResponse {
	return ContextJWTStatusResponse{
		Mode:             status.Mode,
		Configured:       status.Configured,
		Issuer:           status.Issuer,
		Audience:         status.Audience,
		KeyID:            status.KeyID,
		ExpiresSeconds:   status.ExpiresSeconds,
		IncludeName:      status.IncludeName,
		IncludeEmail:     status.IncludeEmail,
		IncludeRole:      status.IncludeRole,
		PendingKeyID:     status.PendingKeyID,
		PendingExpiresAt: status.PendingExpiresAt,
	}
}

func toPrepareContextJWTRotationResponse(
	result appmcp.PrepareContextJWTRotationResult,
) PrepareContextJWTRotationResponse {
	return PrepareContextJWTRotationResponse{
		TemplateToken:     result.TemplateToken,
		RecommendedHeader: result.RecommendedHeader,
		Algorithm:         result.Algorithm,
		Secret:            result.Secret,
		Issuer:            result.Issuer,
		Audience:          result.Audience,
		KeyID:             result.KeyID,
		ExpiresSeconds:    result.ExpiresSeconds,
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
