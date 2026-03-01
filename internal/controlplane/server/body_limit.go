package server

import (
	"net/http"
)

// maxBodyBytes is the maximum allowed size for POST/PUT/PATCH request bodies (1 MiB).
const maxBodyBytes int64 = 1 << 20

// maxBodySizeMiddleware limits POST/PUT/PATCH request body size to maxBodyBytes.
//
// Requests with Content-Length explicitly exceeding the limit are rejected
// immediately with HTTP 413 Request Entity Too Large. All write requests also
// have their body wrapped with http.MaxBytesReader as a safety net against
// chunked or unannounced oversized payloads.
func maxBodySizeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			if r.ContentLength > maxBodyBytes {
				writeJSONError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large (limit 1MB)")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}
