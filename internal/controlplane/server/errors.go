package server

import (
	"encoding/json"
	"net/http"
)

// APIError is the standard error response format.
type APIError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// writeJSONError writes a consistent JSON error response.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIError{Error: message, Code: code})
}
