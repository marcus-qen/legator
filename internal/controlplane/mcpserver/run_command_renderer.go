package mcpserver

import (
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func renderRunCommandMCP(projection *corecommanddispatch.CommandInvokeProjection) (*mcp.CallToolResult, any, error) {
	text, err := corecommanddispatch.EncodeCommandInvokeMCPTextResponse(projection)
	if err != nil {
		return nil, nil, err
	}
	return textToolResult(text), nil, nil
}
