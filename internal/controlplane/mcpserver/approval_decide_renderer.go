package mcpserver

import (
	"io"

	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type approvalDecideResponseRenderer interface {
	RenderMCP(projection *coreapprovalpolicy.DecideApprovalProjection) (*mcp.CallToolResult, any, error)
}

type approvalDecideMCPRenderer struct{}

func (approvalDecideMCPRenderer) RenderMCP(projection *coreapprovalpolicy.DecideApprovalProjection) (*mcp.CallToolResult, any, error) {
	if projection == nil {
		projection = coreapprovalpolicy.ProjectDecideApprovalTransport(nil)
	}
	if err := projection.MCPError(); err != nil {
		return nil, nil, err
	}

	return jsonToolResult(projection.Success)
}

// orchestrateDecideApprovalMCP is the MCP-side seam for shared decide flow
// orchestration used by the MCP approval-decide tool handler.
func orchestrateDecideApprovalMCP(body io.Reader, decide func(*coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error)) *coreapprovalpolicy.DecideApprovalProjection {
	return coreapprovalpolicy.OrchestrateDecideApproval(body, decide, coreapprovalpolicy.DecideApprovalRenderTargetMCP)
}

func renderDecideApprovalMCP(projection *coreapprovalpolicy.DecideApprovalProjection) (*mcp.CallToolResult, any, error) {
	return approvalDecideMCPRenderer{}.RenderMCP(projection)
}
