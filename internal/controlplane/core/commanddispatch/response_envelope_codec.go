package commanddispatch

import (
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

// CommandInvokeResponseEnvelopeBuilder normalizes command invoke projections
// into shared writer-kernel response envelopes.
type CommandInvokeResponseEnvelopeBuilder struct {
	Projection *CommandInvokeProjection
}

var _ transportwriter.EnvelopeBuilder = CommandInvokeResponseEnvelopeBuilder{}

// BuildResponseEnvelope implements transportwriter.EnvelopeBuilder.
func (b CommandInvokeResponseEnvelopeBuilder) BuildResponseEnvelope(surface transportwriter.Surface) *transportwriter.ResponseEnvelope {
	switch surface {
	case transportwriter.SurfaceHTTP:
		return encodeCommandInvokeHTTPEnvelope(b.Projection)
	case transportwriter.SurfaceMCP:
		return encodeCommandInvokeMCPEnvelope(b.Projection)
	default:
		return unsupportedCommandInvokeResponseEnvelope(string(surface))
	}
}

// EncodeCommandInvokeResponseEnvelope normalizes command invoke projections
// into a writer-kernel response envelope for a concrete transport surface.
func EncodeCommandInvokeResponseEnvelope(projection *CommandInvokeProjection, surface ProjectionDispatchSurface) *transportwriter.ResponseEnvelope {
	transportSurface, ok := ResolveCommandInvokeTransportSurface(surface)
	if !ok {
		return unsupportedCommandInvokeResponseEnvelope(string(surface))
	}
	return CommandInvokeResponseEnvelopeBuilder{Projection: projection}.BuildResponseEnvelope(transportSurface)
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

func unsupportedCommandInvokeResponseEnvelope(surface string) *transportwriter.ResponseEnvelope {
	message := transportwriter.UnsupportedSurfaceMessage("command invoke", surface)
	return transportwriter.UnsupportedSurfaceEnvelope(message)
}
