package repository

import (
	"context"
	"errors"
	"time"

	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
)

var (
	ErrMCPContextJWTPendingExists    = errors.New("mcp context jwt pending rotation exists")
	ErrMCPContextJWTRotationConflict = errors.New("mcp context jwt rotation conflict")
	ErrMCPContextJWTPendingExpired   = errors.New("mcp context jwt pending rotation expired")
)

// CreateMCPServerInput 定义创建 MCP 服务字段。
type CreateMCPServerInput struct {
	PublicID           string
	ContextJWTAudience string
	Name               string
	BaseURL            string
	AuthTokenEnc       string
	HeadersJSON        string
	HeadersEnabled     bool
	Status             string
}

// UpdateMCPServerInput 定义更新 MCP 服务字段。
type UpdateMCPServerInput struct {
	Name           *string
	BaseURL        *string
	AuthTokenEnc   *string
	HeadersJSON    *string
	HeadersEnabled *bool
	Status         *string
	LastError      *string
}

// UpdateMCPToolInput 定义更新 MCP 工具字段。
type UpdateMCPToolInput struct {
	DisplayName *string
	Description *string
	Status      *string
}

type ReorderMCPServerInput struct {
	ServerID uint
	ToolIDs  []uint
}

type UpdateMCPContextJWTPolicyInput struct {
	ExpiresSeconds int
	IncludeName    bool
	IncludeEmail   bool
	IncludeRole    bool
}

type PrepareMCPContextJWTRotationInput struct {
	ServerID         uint
	PendingSecretEnc string
	PendingKeyID     string
	CreatedAt        time.Time
	ExpiresAt        time.Time
}

// MCPRepository 封装 MCP 控制面持久化。
type MCPRepository interface {
	CreateServer(ctx context.Context, input CreateMCPServerInput) (*domainmcp.Server, error)
	UpdateServer(ctx context.Context, serverID uint, input UpdateMCPServerInput) (*domainmcp.Server, error)
	ListServers(ctx context.Context) ([]domainmcp.Server, error)
	GetServer(ctx context.Context, serverID uint) (*domainmcp.Server, error)
	DeleteServer(ctx context.Context, serverID uint) error
	ReplaceServerTools(ctx context.Context, serverID uint, tools []domainmcp.Tool) error
	ListTools(ctx context.Context, serverID uint, onlyActive bool) ([]domainmcp.Tool, error)
	ListToolsByIDs(ctx context.Context, toolIDs []uint) ([]domainmcp.Tool, error)
	UpdateTool(ctx context.Context, toolID uint, input UpdateMCPToolInput) (*domainmcp.Tool, error)
	UpdateServerToolsStatus(ctx context.Context, serverID uint, toolIDs []uint, status string) ([]domainmcp.Tool, error)
	ReorderServersWithTools(ctx context.Context, order []ReorderMCPServerInput) ([]domainmcp.ServerWithTools, error)
	UpdateContextJWTPolicy(ctx context.Context, serverID uint, input UpdateMCPContextJWTPolicyInput) (*domainmcp.Server, error)
	PrepareContextJWTRotation(ctx context.Context, input PrepareMCPContextJWTRotationInput) (*domainmcp.Server, error)
	ActivateContextJWTRotation(ctx context.Context, serverID uint, kid string, now time.Time) (*domainmcp.Server, error)
	CancelContextJWTRotation(ctx context.Context, serverID uint, kid string) (*domainmcp.Server, error)
	DisableContextJWT(ctx context.Context, serverID uint) (*domainmcp.Server, error)
	ClearExpiredContextJWTPending(ctx context.Context, now time.Time) error
}
