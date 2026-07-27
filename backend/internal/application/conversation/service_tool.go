package conversation

import (
	"context"
	"fmt"
	"strings"

	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
)

// ExecuteToolInput 定义工具执行入参。
type ExecuteToolInput struct {
	ToolName      string
	ArgumentsJSON string
	Operation     mcp.Operation
}

func (s *Service) executeToolCall(ctx context.Context, input ExecuteToolInput) (string, error) {
	toolName := strings.TrimSpace(input.ToolName)
	if toolName == "" {
		return "", fmt.Errorf("tool name is required")
	}
	if input.Operation == nil {
		return "", fmt.Errorf("tool %s is not enabled for this run", toolName)
	}
	cfg := s.cfg.Snapshot()

	limit := cfg.MCPMaxConcurrentCalls
	if limit <= 0 {
		limit = 8
	}

	return s.executeWithToolLimiter(ctx, limit, func() (string, error) {
		return input.Operation.CallTool(ctx, mcp.CallInput{
			ToolName:      toolName,
			ArgumentsJSON: strings.TrimSpace(input.ArgumentsJSON),
		})
	})
}

func (s *Service) resolveMaxToolCallsPerRun() int {
	maxCalls := s.cfg.Snapshot().MCPMaxToolCallsPerRun
	if maxCalls <= 0 {
		maxCalls = 8
	}
	if maxCalls > 64 {
		maxCalls = 64
	}
	return maxCalls
}

func (s *Service) resolveMaxSelectedToolsPerMessage() int {
	maxTools := s.cfg.Snapshot().MCPMaxSelectedToolsPerMessage
	if maxTools <= 0 {
		maxTools = config.DefaultMCPMaxSelectedToolsPerMessage
	}
	if maxTools > config.MaxMCPSelectedToolsPerMessage {
		maxTools = config.MaxMCPSelectedToolsPerMessage
	}
	return maxTools
}

// ValidateSelectedToolIDs 校验单次消息选择的 MCP 工具数量。
func (s *Service) ValidateSelectedToolIDs(toolIDs []uint) error {
	if len(toolIDs) > s.resolveMaxSelectedToolsPerMessage() {
		return ErrTooManySelectedTools
	}
	return nil
}

func (s *Service) resolveMaxLLMCallsPerRun() int {
	maxCalls := s.cfg.Snapshot().MCPMaxLLMCallsPerRun
	if maxCalls <= 0 {
		maxCalls = 5
	}
	if maxCalls < 2 {
		maxCalls = 2
	}
	if maxCalls > 32 {
		maxCalls = 32
	}
	return maxCalls
}

func (s *Service) executeWithToolLimiter(
	ctx context.Context,
	limit int,
	fn func() (string, error),
) (string, error) {
	if fn == nil {
		return "", fmt.Errorf("tool execution function is nil")
	}
	if limit <= 0 {
		return fn()
	}

	limiter := s.getToolLimiter(limit)
	select {
	case limiter <- struct{}{}:
		defer func() { <-limiter }()
		return fn()
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *Service) getToolLimiter(limit int) chan struct{} {
	if limit <= 0 {
		limit = 1
	}
	if value, ok := s.toolLimiters.Load(limit); ok {
		if limiter, castOK := value.(chan struct{}); castOK {
			return limiter
		}
	}
	created := make(chan struct{}, limit)
	actual, _ := s.toolLimiters.LoadOrStore(limit, created)
	limiter, ok := actual.(chan struct{})
	if !ok {
		return created
	}
	return limiter
}
