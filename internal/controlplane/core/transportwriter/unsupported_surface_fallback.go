package transportwriter

// UnsupportedSurfaceFallbackWriter provides HTTP/MCP callbacks used when a
// surface is unsupported. Fallback precedence is HTTP-first, MCP-second.
type UnsupportedSurfaceFallbackWriter struct {
	WriteHTTPError func(*HTTPError)
	WriteMCPError  func(error)
}

// WriteUnsupportedSurfaceFallback emits unsupported-surface errors with the
// shared precedence policy: HTTP callback first, then MCP callback.
func WriteUnsupportedSurfaceFallback(envelope *ResponseEnvelope, writer UnsupportedSurfaceFallbackWriter) bool {
	if envelope == nil {
		return false
	}
	if envelope.HTTPError != nil && writer.WriteHTTPError != nil {
		writer.WriteHTTPError(envelope.HTTPError)
		return true
	}
	if envelope.MCPError != nil && writer.WriteMCPError != nil {
		writer.WriteMCPError(envelope.MCPError)
		return true
	}
	return false
}
