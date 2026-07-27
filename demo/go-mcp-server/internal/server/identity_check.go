package server

import (
	"context"
	"errors"
	"time"

	"github.com/DEEIX-AI/DEEIX-Chat/demo/go-mcp-server/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type IdentityCheckInput struct{}

type IdentityCheckOutput struct {
	Authenticated  bool                     `json:"authenticated"`
	CheckedAt      string                   `json:"checkedAt"`
	Verification   identity.Verification    `json:"verification"`
	Identity       identity.HeaderIdentity  `json:"identity"`
	SignedIdentity *identity.SignedIdentity `json:"signedIdentity,omitempty"`
	Mismatches     []string                 `json:"mismatches"`
}

func identityCheck(now func() time.Time) mcp.ToolHandlerFor[IdentityCheckInput, IdentityCheckOutput] {
	return func(
		_ context.Context,
		request *mcp.CallToolRequest,
		_ IdentityCheckInput,
	) (*mcp.CallToolResult, IdentityCheckOutput, error) {
		output := IdentityCheckOutput{Mismatches: make([]string, 0)}
		if now == nil || request == nil || request.Extra == nil {
			return nil, output, errors.New("identity snapshot unavailable")
		}
		snapshot, ok := identity.SnapshotFromTokenInfo(request.Extra.TokenInfo)
		if !ok {
			return nil, output, errors.New("identity snapshot unavailable")
		}
		output.Authenticated = true
		output.CheckedAt = now().UTC().Format(time.RFC3339)
		output.Verification = snapshot.Verification
		output.Identity = snapshot.Identity
		output.SignedIdentity = snapshot.SignedIdentity
		output.Mismatches = append(output.Mismatches, snapshot.Mismatches...)
		result := &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "identity_check"}},
		}
		return result, output, nil
	}
}
