package mcpserver

import (
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type approvalDecideResponseRenderer interface {
	RenderMCP(contract *coreapprovalpolicy.DecideApprovalTransportContract) (*mcp.CallToolResult, any, error)
}

type approvalDecideMCPRenderer struct{}

func (approvalDecideMCPRenderer) RenderMCP(contract *coreapprovalpolicy.DecideApprovalTransportContract) (*mcp.CallToolResult, any, error) {
	projection := coreapprovalpolicy.ProjectDecideApprovalTransport(contract)
	if err := projection.MCPError(); err != nil {
		return nil, nil, err
	}

	return jsonToolResult(projection.Success)
}

func renderDecideApprovalMCP(contract *coreapprovalpolicy.DecideApprovalTransportContract) (*mcp.CallToolResult, any, error) {
	return approvalDecideMCPRenderer{}.RenderMCP(contract)
}
