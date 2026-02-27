package transportwriter

// Surface identifies a response transport.
type Surface string

const (
	SurfaceHTTP Surface = "http"
	SurfaceMCP  Surface = "mcp"
)

// HTTPError is the normalized HTTP error envelope emitted by core codecs.
type HTTPError struct {
	Status        int
	Code          string
	Message       string
	SuppressWrite bool
}

// ResponseEnvelope is the normalized response contract emitted by core codecs
// before transport-specific write paths are applied.
type ResponseEnvelope struct {
	HTTPError   *HTTPError
	MCPError    error
	HTTPSuccess any
	MCPSuccess  any
}

// EnvelopeBuilder builds transportwriter response envelopes for a specific
// response surface.
type EnvelopeBuilder interface {
	BuildResponseEnvelope(surface Surface) *ResponseEnvelope
}

// EnvelopeBuilderFunc is a function adapter for EnvelopeBuilder.
type EnvelopeBuilderFunc func(surface Surface) *ResponseEnvelope

// BuildResponseEnvelope implements EnvelopeBuilder.
func (f EnvelopeBuilderFunc) BuildResponseEnvelope(surface Surface) *ResponseEnvelope {
	if f == nil {
		return nil
	}
	return f(surface)
}

// WriterKernel is the shared HTTP/MCP transport writer kernel.
//
// Core codecs emit ResponseEnvelope values; concrete HTTP/MCP renderers provide
// writer callbacks to preserve existing external response behavior.
type WriterKernel struct {
	WriteHTTPError   func(*HTTPError)
	WriteMCPError    func(error)
	WriteHTTPSuccess func(any)
	WriteMCPSuccess  func(any)
}

// WriteForSurface routes an already-normalized response envelope through the
// configured transport callbacks. It returns true when an error path was
// handled (including suppressed HTTP writes).
func WriteForSurface(surface Surface, envelope *ResponseEnvelope, kernel WriterKernel) bool {
	if envelope == nil {
		return false
	}

	switch surface {
	case SurfaceHTTP:
		if envelope.HTTPError != nil {
			if !envelope.HTTPError.SuppressWrite && kernel.WriteHTTPError != nil {
				kernel.WriteHTTPError(envelope.HTTPError)
			}
			return true
		}
		if kernel.WriteHTTPSuccess != nil {
			kernel.WriteHTTPSuccess(envelope.HTTPSuccess)
		}
		return false
	case SurfaceMCP:
		if envelope.MCPError != nil {
			if kernel.WriteMCPError != nil {
				kernel.WriteMCPError(envelope.MCPError)
			}
			return true
		}
		if kernel.WriteMCPSuccess != nil {
			kernel.WriteMCPSuccess(envelope.MCPSuccess)
		}
		return false
	default:
		return false
	}
}

// WriteFromBuilder builds an envelope for the target surface and routes it
// through the shared writer kernel.
func WriteFromBuilder(surface Surface, builder EnvelopeBuilder, kernel WriterKernel) bool {
	if builder == nil {
		return false
	}
	return WriteForSurface(surface, builder.BuildResponseEnvelope(surface), kernel)
}
