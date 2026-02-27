package commanddispatch

import (
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
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
	envelope := EncodeCommandInvokeResponseEnvelope(projection, ProjectionDispatchSurfaceHTTP)

	transportwriter.WriteForSurface(transportwriter.SurfaceHTTP, envelope, transportwriter.WriterKernel{
		WriteHTTPError: func(httpErr *transportwriter.HTTPError) {
			if httpErr == nil {
				return
			}
			response.Status = httpErr.Status
			response.Body = HTTPJSONErrorPayload{Error: httpErr.Message, Code: httpErr.Code}
			response.HasBody = true
		},
		WriteHTTPSuccess: func(payload any) {
			response.Status = http.StatusOK
			response.Body = payload
			response.HasBody = true
		},
	})

	if envelope != nil && envelope.HTTPError != nil && envelope.HTTPError.SuppressWrite {
		response.SuppressWrite = true
		response.HasBody = false
		response.Body = nil
	}

	return response
}

// EncodeCommandInvokeMCPTextResponse shapes command invoke projections into MCP
// text/error outputs while preserving existing error semantics.
func EncodeCommandInvokeMCPTextResponse(projection *CommandInvokeProjection) (string, error) {
	envelope := EncodeCommandInvokeResponseEnvelope(projection, ProjectionDispatchSurfaceMCP)
	resultText := ""
	var dispatchErr error

	transportwriter.WriteForSurface(transportwriter.SurfaceMCP, envelope, transportwriter.WriterKernel{
		WriteMCPError: func(err error) {
			dispatchErr = err
		},
		WriteMCPSuccess: func(payload any) {
			text, _ := payload.(string)
			resultText = text
		},
	})

	if dispatchErr != nil {
		return "", dispatchErr
	}
	return resultText, nil
}
