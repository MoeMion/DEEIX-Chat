package mcp

import "time"

type ServerResponse struct {
	ID                  uint                            `json:"id"`
	PublicID            string                          `json:"publicID"`
	Name                string                          `json:"name"`
	BaseURL             string                          `json:"baseURL"`
	AuthTokenConfigured bool                            `json:"authTokenConfigured"`
	HeadersJSON         string                          `json:"headersJSON"`
	HeadersEnabled      bool                            `json:"headersEnabled"`
	HeaderWarnings      []HeaderTemplateWarningResponse `json:"headerWarnings"`
	SignedContextHeader string                          `json:"signedContextHeader"`
	Status              string                          `json:"status"`
	SortOrder           int                             `json:"sortOrder"`
	ToolCount           int                             `json:"toolCount"`
	ActiveToolCount     int                             `json:"activeToolCount"`
	LastSyncedAt        *time.Time                      `json:"lastSyncedAt" extensions:"x-nullable,!x-omitempty"`
	LastError           string                          `json:"lastError"`
	CreatedAt           time.Time                       `json:"createdAt"`
	UpdatedAt           time.Time                       `json:"updatedAt"`
	ContextJWT          ContextJWTStatusResponse        `json:"contextJWT"`
}

type UpdateContextJWTRequest struct {
	ExpiresSeconds int  `json:"expiresSeconds" binding:"required,min=60,max=900"`
	IncludeName    bool `json:"includeName"`
	IncludeEmail   bool `json:"includeEmail"`
	IncludeRole    bool `json:"includeRole"`
}

type ContextJWTStatusResponse struct {
	Mode             string     `json:"mode"`
	Configured       bool       `json:"configured"`
	Issuer           string     `json:"issuer"`
	Audience         string     `json:"audience"`
	KeyID            string     `json:"keyID"`
	ExpiresSeconds   int        `json:"expiresSeconds"`
	IncludeName      bool       `json:"includeName"`
	IncludeEmail     bool       `json:"includeEmail"`
	IncludeRole      bool       `json:"includeRole"`
	PendingKeyID     string     `json:"pendingKeyID,omitempty"`
	PendingExpiresAt *time.Time `json:"pendingExpiresAt,omitempty"`
}

type PrepareContextJWTRotationResponse struct {
	TemplateToken     string `json:"templateToken"`
	RecommendedHeader string `json:"recommendedHeader"`
	Algorithm         string `json:"algorithm"`
	Secret            string `json:"secret"`
	Issuer            string `json:"issuer"`
	Audience          string `json:"audience"`
	KeyID             string `json:"keyID"`
	ExpiresSeconds    int    `json:"expiresSeconds"`
}

type ToolResponse struct {
	ID              uint      `json:"id"`
	ServerID        uint      `json:"serverID"`
	ServerName      string    `json:"serverName"`
	Name            string    `json:"name"`
	DisplayName     string    `json:"displayName"`
	Description     string    `json:"description"`
	InputSchemaJSON string    `json:"inputSchemaJSON"`
	Status          string    `json:"status"`
	SortOrder       int       `json:"sortOrder"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type CreateServerRequest struct {
	Name           string `json:"name" binding:"required,max=128"`
	BaseURL        string `json:"baseURL" binding:"required,max=512"`
	AuthToken      string `json:"authToken,omitempty" binding:"max=8192"`
	HeadersJSON    string `json:"headersJSON,omitempty" binding:"max=32768"`
	HeadersEnabled *bool  `json:"headersEnabled,omitempty"`
	Status         string `json:"status,omitempty" binding:"omitempty,oneof=active inactive"`
}

type UpdateServerRequest struct {
	Name           *string `json:"name,omitempty" binding:"omitempty,max=128"`
	BaseURL        *string `json:"baseURL,omitempty" binding:"omitempty,max=512"`
	AuthToken      *string `json:"authToken,omitempty" binding:"omitempty,max=8192"`
	ClearAuthToken bool    `json:"clearAuthToken,omitempty"`
	HeadersJSON    *string `json:"headersJSON,omitempty" binding:"omitempty,max=32768"`
	HeadersEnabled *bool   `json:"headersEnabled,omitempty"`
	Status         *string `json:"status,omitempty" binding:"omitempty,oneof=active inactive"`
}

type UpdateToolRequest struct {
	DisplayName *string `json:"displayName,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}

type UpdateServerToolsStatusRequest struct {
	ToolIDs []uint `json:"toolIDs"`
	Status  string `json:"status"`
}

type ReorderServerOrderItem struct {
	ServerID uint   `json:"serverID"`
	ToolIDs  []uint `json:"toolIDs"`
}

type ReorderServersRequest struct {
	Servers []ReorderServerOrderItem `json:"servers"`
}

type HeaderTemplateWarningResponse struct {
	Code       string `json:"code"`
	HeaderName string `json:"headerName,omitempty"`
	Token      string `json:"token,omitempty"`
}

type HeaderPreviewItemResponse struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
}

type PreviewHeaderTemplateRequest struct {
	HeadersJSON    string `json:"headersJSON" binding:"required,max=32768"`
	HeadersEnabled *bool  `json:"headersEnabled" binding:"required"`
	ServerID       *uint  `json:"serverID,omitempty" binding:"omitempty,min=1"`
	Mode           string `json:"mode" binding:"required,oneof=chat probe sync"`
}

type HeaderTemplatePreviewResponse struct {
	Mode                string                          `json:"mode"`
	SupportedTokens     []string                        `json:"supportedTokens"`
	Warnings            []HeaderTemplateWarningResponse `json:"warnings" binding:"required"`
	Headers             []HeaderPreviewItemResponse     `json:"headers"`
	SignedContextHeader string                          `json:"signedContextHeader" binding:"required"`
}

type ProbeServerResponse struct {
	ToolCount int                             `json:"toolCount"`
	Warnings  []HeaderTemplateWarningResponse `json:"warnings"`
}

type ServerDataResponse struct {
	Server ServerResponse `json:"server"`
}

type DeleteServerResponse struct {
	Deleted bool `json:"deleted"`
}

type ServerListResponse struct {
	Results []ServerResponse `json:"results"`
}

type ToolListResponse struct {
	Results []ToolResponse `json:"results"`
}

type ServerToolOrderResponse struct {
	Server ServerResponse `json:"server"`
	Tools  []ToolResponse `json:"tools"`
}

type ServerToolOrderListResponse struct {
	Results []ServerToolOrderResponse `json:"results"`
}

// ErrorDoc 表示 MCP 管理接口的错误响应。
type ErrorDoc struct {
	ErrorMsg string `json:"errorMsg"`
}

// ServerListResponseDoc 包裹 MCP 服务列表响应。
type ServerListResponseDoc struct {
	ErrorMsg string             `json:"errorMsg"`
	Data     ServerListResponse `json:"data"`
}

// ServerDataResponseDoc 包裹 MCP 服务详情响应。
type ServerDataResponseDoc struct {
	ErrorMsg string             `json:"errorMsg"`
	Data     ServerDataResponse `json:"data"`
}

// ToolListResponseDoc 包裹 MCP 工具列表响应。
type ToolListResponseDoc struct {
	ErrorMsg string           `json:"errorMsg"`
	Data     ToolListResponse `json:"data"`
}

// ToolResponseDoc 包裹 MCP 工具详情响应。
type ToolResponseDoc struct {
	ErrorMsg string       `json:"errorMsg"`
	Data     ToolResponse `json:"data"`
}

// ServerToolOrderListResponseDoc 包裹 MCP 服务及工具排序响应。
type ServerToolOrderListResponseDoc struct {
	ErrorMsg string                      `json:"errorMsg"`
	Data     ServerToolOrderListResponse `json:"data"`
}

// DeleteServerResponseDoc 包裹 MCP 服务删除响应。
type DeleteServerResponseDoc struct {
	ErrorMsg string               `json:"errorMsg"`
	Data     DeleteServerResponse `json:"data"`
}

type HeaderTemplatePreviewResponseDoc struct {
	ErrorMsg string                        `json:"errorMsg"`
	Data     HeaderTemplatePreviewResponse `json:"data"`
}

type ProbeServerResponseDoc struct {
	ErrorMsg string              `json:"errorMsg"`
	Data     ProbeServerResponse `json:"data"`
}

type ContextJWTStatusResponseDoc struct {
	ErrorMsg string                   `json:"errorMsg"`
	Data     ContextJWTStatusResponse `json:"data"`
}

type PrepareContextJWTRotationResponseDoc struct {
	ErrorMsg string                            `json:"errorMsg"`
	Data     PrepareContextJWTRotationResponse `json:"data"`
}
