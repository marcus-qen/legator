package commanddispatch

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

// EncodeCommandInvokeResponseEnvelope normalizes command invoke projections
// into a writer-kernel response envelope for a concrete transport surface.
func EncodeCommandInvokeResponseEnvelope(projection *CommandInvokeProjection, surface ProjectionDispatchSurface) *transportwriter.ResponseEnvelope {
	switch surface {
	case ProjectionDispatchSurfaceHTTP:
		return encodeCommandInvokeHTTPEnvelope(projection)
	case ProjectionDispatchSurfaceMCP:
		return encodeCommandInvokeMCPEnvelope(projection)
	default:
		return encodeUnsupportedCommandInvokeSurfaceEnvelope(surface)
	}
}

func encodeCommandInvokeHTTPEnvelope(projection *CommandInvokeProjection) *transportwriter.ResponseEnvelope {
	if projection == nil || projection.Envelope == nil {
		return &transportwriter.ResponseEnvelope{HTTPError: &transportwriter.HTTPError{
			Status:  http.StatusBadGateway,
			Code:    "bad_gateway",
			Message: "command dispatch failed",
		}}
	}

	if httpErr, ok := projection.Envelope.HTTPError(); ok {
		return &transportwriter.ResponseEnvelope{HTTPError: &transportwriter.HTTPError{
			Status:        httpErr.Status,
			Code:          httpErr.Code,
			Message:       httpErr.Message,
			SuppressWrite: httpErr.SuppressWrite,
		}}
	}

	if !projection.WaitForResult {
		return &transportwriter.ResponseEnvelope{HTTPSuccess: map[string]string{
			"status":     "dispatched",
			"request_id": projection.RequestID,
		}}
	}

	return &transportwriter.ResponseEnvelope{HTTPSuccess: projection.Envelope.Result}
}

func encodeCommandInvokeMCPEnvelope(projection *CommandInvokeProjection) *transportwriter.ResponseEnvelope {
	if projection == nil || projection.Envelope == nil {
		return &transportwriter.ResponseEnvelope{MCPError: ErrEmptyResult}
	}
	if err := projection.Envelope.MCPError(); err != nil {
		return &transportwriter.ResponseEnvelope{MCPError: err}
	}
	if projection.Envelope.Result == nil {
		return &transportwriter.ResponseEnvelope{MCPError: ErrEmptyResult}
	}
	return &transportwriter.ResponseEnvelope{MCPSuccess: ResultText(projection.Envelope.Result)}
}

func encodeUnsupportedCommandInvokeSurfaceEnvelope(surface ProjectionDispatchSurface) *transportwriter.ResponseEnvelope {
	message := fmt.Sprintf("unsupported command invoke surface %q", string(surface))
	return &transportwriter.ResponseEnvelope{
		HTTPError: &transportwriter.HTTPError{
			Status:  http.StatusInternalServerError,
			Code:    "internal_error",
			Message: message,
		},
		MCPError: errors.New(message),
	}
}
