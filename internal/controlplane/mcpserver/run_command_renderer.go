package mcpserver

import (
	"fmt"

	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func renderRunCommandMCP(projection *corecommanddispatch.CommandInvokeProjection) (*mcp.CallToolResult, any, error) {
	if projection == nil || projection.Envelope == nil {
		return nil, nil, fmt.Errorf("command failed: empty result")
	}

	var dispatchErr error
	handled := corecommanddispatch.DispatchCommandErrorsForSurface(projection.Envelope, projection.Surface, corecommanddispatch.CommandProjectionDispatchWriter{
		WriteMCPError: func(err error) {
			dispatchErr = err
		},
	})
	if handled {
		return nil, nil, dispatchErr
	}

	if projection.Envelope.Result == nil {
		return nil, nil, fmt.Errorf("command failed: empty result")
	}

	resultText := ""
	corecommanddispatch.DispatchCommandReadForSurface(projection.Envelope.Result, projection.Surface, corecommanddispatch.CommandProjectionDispatchWriter{
		WriteMCPText: func(text string) {
			resultText = text
		},
	})
	return textToolResult(resultText), nil, nil
}
