package transportwriter

import (
	"errors"
	"net/http"
)

// UnsupportedSurfaceEnvelope builds the shared unsupported-surface fallback
// envelope used by core approval/command codecs and adapters.
func UnsupportedSurfaceEnvelope(message string) *ResponseEnvelope {
	return &ResponseEnvelope{
		HTTPError: &HTTPError{
			Status:  http.StatusInternalServerError,
			Code:    "internal_error",
			Message: message,
		},
		MCPError: errors.New(message),
	}
}
