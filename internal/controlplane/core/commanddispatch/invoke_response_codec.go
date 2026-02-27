package commanddispatch

import (
	"net/http"

	"github.com/marcus-qen/legator/internal/protocol"
)

// HTTPJSONErrorPayload is the shared JSON error payload emitted by command
// invoke HTTP renderers.
type HTTPJSONErrorPayload struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// CommandInvokeHTTPJSONResponse is the transport-agnostic HTTP JSON response
// contract for command invoke projections.
type CommandInvokeHTTPJSONResponse struct {
	Status        int
	Body          any
	HasBody       bool
	SuppressWrite bool
}

// EncodeCommandInvokeHTTPJSONResponse shapes command invoke projections into
// HTTP JSON response/error payloads while preserving status semantics.
func EncodeCommandInvokeHTTPJSONResponse(projection *CommandInvokeProjection) CommandInvokeHTTPJSONResponse {
	response := CommandInvokeHTTPJSONResponse{Status: http.StatusOK}

	DispatchCommandInvokeProjection(projection, CommandInvokeRenderDispatchWriter{
		WriteHTTPError: func(httpErr *HTTPErrorContract) {
			if httpErr == nil {
				return
			}
			if httpErr.SuppressWrite {
				response.SuppressWrite = true
				response.HasBody = false
				response.Body = nil
				return
			}

			response.Status = httpErr.Status
			response.Body = HTTPJSONErrorPayload{Error: httpErr.Message, Code: httpErr.Code}
			response.HasBody = true
		},
		WriteHTTPDispatched: func(requestID string) {
			response.Status = http.StatusOK
			response.Body = map[string]string{
				"status":     "dispatched",
				"request_id": requestID,
			}
			response.HasBody = true
		},
		WriteHTTPResult: func(result *protocol.CommandResultPayload) {
			response.Status = http.StatusOK
			response.Body = result
			response.HasBody = true
		},
	})

	return response
}

// EncodeCommandInvokeMCPTextResponse shapes command invoke projections into MCP
// text/error outputs while preserving existing error semantics.
func EncodeCommandInvokeMCPTextResponse(projection *CommandInvokeProjection) (string, error) {
	resultText := ""
	var dispatchErr error

	DispatchCommandInvokeProjection(projection, CommandInvokeRenderDispatchWriter{
		WriteMCPError: func(err error) {
			dispatchErr = err
		},
		WriteMCPText: func(text string) {
			resultText = text
		},
	})

	if dispatchErr != nil {
		return "", dispatchErr
	}
	return resultText, nil
}
