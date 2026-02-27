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
	var (
		result *mcp.CallToolResult
		meta   any
		err    error
	)

	coreapprovalpolicy.DispatchDecideApprovalResponseForSurface(projection, coreapprovalpolicy.DecideApprovalRenderSurfaceMCP, coreapprovalpolicy.DecideApprovalResponseDispatchWriter{
		WriteMCPError: func(dispatchErr error) {
			err = dispatchErr
		},
		WriteSuccess: func(success *coreapprovalpolicy.DecideApprovalSuccess) {
			result, meta, err = jsonToolResult(success)
		},
	})

	return result, meta, err
}

// orchestrateDecideApprovalMCP is the MCP-side seam for shared decide flow
// orchestration used by the MCP approval-decide tool handler.
func orchestrateDecideApprovalMCP(body io.Reader, decide func(*coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error)) *coreapprovalpolicy.DecideApprovalProjection {
	return coreapprovalpolicy.OrchestrateDecideApprovalForSurface(body, decide, coreapprovalpolicy.DecideApprovalRenderSurfaceMCP)
}

func renderDecideApprovalMCP(projection *coreapprovalpolicy.DecideApprovalProjection) (*mcp.CallToolResult, any, error) {
	return approvalDecideMCPRenderer{}.RenderMCP(projection)
}
