package mcpserver

import (
	"fmt"

	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func renderRunCommandMCP(envelope *corecommanddispatch.CommandResultEnvelope) (*mcp.CallToolResult, any, error) {
	if envelope == nil {
		return nil, nil, fmt.Errorf("command failed: empty result")
	}

	var dispatchErr error
	handled := corecommanddispatch.DispatchCommandErrorsForSurface(envelope, corecommanddispatch.ProjectionDispatchSurfaceMCP, corecommanddispatch.CommandProjectionDispatchWriter{
		WriteMCPError: func(err error) {
			dispatchErr = err
		},
	})
	if handled {
		return nil, nil, dispatchErr
	}

	if envelope.Result == nil {
		return nil, nil, fmt.Errorf("command failed: empty result")
	}

	resultText := ""
	corecommanddispatch.DispatchCommandReadForSurface(envelope.Result, corecommanddispatch.ProjectionDispatchSurfaceMCP, corecommanddispatch.CommandProjectionDispatchWriter{
		WriteMCPText: func(text string) {
			resultText = text
		},
	})
	return textToolResult(resultText), nil, nil
}
