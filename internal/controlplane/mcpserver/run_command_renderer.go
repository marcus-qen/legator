package mcpserver

import (
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func renderRunCommandMCP(projection *corecommanddispatch.CommandInvokeProjection) (*mcp.CallToolResult, any, error) {
	var (
		result      *mcp.CallToolResult
		dispatchErr error
	)

	corecommanddispatch.DispatchCommandInvokeProjection(projection, corecommanddispatch.CommandInvokeRenderDispatchWriter{
		WriteMCPError: func(err error) {
			dispatchErr = err
		},
		WriteMCPText: func(text string) {
			result = textToolResult(text)
		},
	})

	if dispatchErr != nil {
		return nil, nil, dispatchErr
	}
	return result, nil, nil
}
