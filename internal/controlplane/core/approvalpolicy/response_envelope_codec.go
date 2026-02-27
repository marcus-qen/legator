package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

// DecideApprovalResponseEnvelopeBuilder normalizes approval-decide projections
// into shared writer-kernel response envelopes.
type DecideApprovalResponseEnvelopeBuilder struct {
	Projection *DecideApprovalProjection
}

var _ transportwriter.EnvelopeBuilder = DecideApprovalResponseEnvelopeBuilder{}

// BuildResponseEnvelope implements transportwriter.EnvelopeBuilder.
func (b DecideApprovalResponseEnvelopeBuilder) BuildResponseEnvelope(surface transportwriter.Surface) *transportwriter.ResponseEnvelope {
	projection := normalizeDecideApprovalProjection(b.Projection)

	switch surface {
	case transportwriter.SurfaceHTTP:
		if httpErr, ok := projection.HTTPError(); ok {
			return &transportwriter.ResponseEnvelope{HTTPError: &transportwriter.HTTPError{
				Status:  httpErr.Status,
				Code:    httpErr.Code,
				Message: httpErr.Message,
			}}
		}
		return &transportwriter.ResponseEnvelope{HTTPSuccess: normalizeDecideApprovalSuccess(projection.Success)}
	case transportwriter.SurfaceMCP:
		if err := projection.MCPError(); err != nil {
			return &transportwriter.ResponseEnvelope{MCPError: err}
		}
		return &transportwriter.ResponseEnvelope{MCPSuccess: normalizeDecideApprovalSuccess(projection.Success)}
	default:
		return unsupportedDecideApprovalResponseEnvelope(string(surface))
	}
}

// EncodeDecideApprovalResponseEnvelope normalizes approval-decide projections
// for HTTP/MCP writer-kernel transport rendering.
func EncodeDecideApprovalResponseEnvelope(projection *DecideApprovalProjection, surface DecideApprovalRenderSurface) *transportwriter.ResponseEnvelope {
	transportSurface, ok := ResolveDecideApprovalTransportSurface(surface)
	if !ok {
		return unsupportedDecideApprovalResponseEnvelope(string(surface))
	}
	return DecideApprovalResponseEnvelopeBuilder{Projection: projection}.BuildResponseEnvelope(transportSurface)
}

func unsupportedDecideApprovalResponseEnvelope(surface string) *transportwriter.ResponseEnvelope {
	message := transportwriter.UnsupportedSurfaceMessage("approval decide dispatch", surface)
	return transportwriter.UnsupportedSurfaceEnvelope(message)
}

func normalizeDecideApprovalProjection(projection *DecideApprovalProjection) *DecideApprovalProjection {
	if projection == nil {
		return ProjectDecideApprovalTransport(nil)
	}
	return projection
}

func normalizeDecideApprovalSuccess(success *DecideApprovalSuccess) *DecideApprovalSuccess {
	if success == nil {
		return &DecideApprovalSuccess{}
	}
	return success
}
