package mcp

import "time"

type ServerResponse struct {
	ID                  uint       `json:"id"`
	Name                string     `json:"name"`
	BaseURL             string     `json:"baseURL"`
	AuthTokenConfigured bool       `json:"authTokenConfigured"`
	HeadersJSON         string     `json:"headersJSON"`
	Status              string     `json:"status"`
	SortOrder           int        `json:"sortOrder"`
	ToolCount           int        `json:"toolCount"`
	ActiveToolCount     int        `json:"activeToolCount"`
	LastSyncedAt        *time.Time `json:"lastSyncedAt"`
	LastError           string     `json:"lastError"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
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
	Name        string `json:"name" binding:"required,max=128"`
	BaseURL     string `json:"baseURL" binding:"required,max=512"`
	AuthToken   string `json:"authToken" binding:"max=8192"`
	HeadersJSON string `json:"headersJSON" binding:"max=32768"`
	Status      string `json:"status" binding:"omitempty,oneof=active inactive"`
}

type UpdateServerRequest struct {
	Name           *string `json:"name" binding:"omitempty,max=128"`
	BaseURL        *string `json:"baseURL" binding:"omitempty,max=512"`
	AuthToken      *string `json:"authToken" binding:"omitempty,max=8192"`
	ClearAuthToken bool    `json:"clearAuthToken"`
	HeadersJSON    *string `json:"headersJSON" binding:"omitempty,max=32768"`
	Status         *string `json:"status" binding:"omitempty,oneof=active inactive"`
}

type UpdateToolRequest struct {
	DisplayName *string `json:"displayName"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
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
