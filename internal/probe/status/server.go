// Package status provides a local HTTP status endpoint for the probe.
// Used by monitoring tools, health checks, and local diagnostics.
package status

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// Info represents the probe's current status.
type Info struct {
	ProbeID    string    `json:"probe_id"`
	ServerURL  string    `json:"server_url"`
	Connected  bool      `json:"connected"`
	StartedAt  time.Time `json:"started_at"`
	Uptime     string    `json:"uptime"`
	GoVersion  string    `json:"go_version"`
	NumGoroutine int    `json:"goroutines"`
	MemAlloc   uint64    `json:"mem_alloc_bytes"`
}

// Server provides a local HTTP status endpoint.
type Server struct {
	probeID   string
	serverURL string
	startedAt time.Time
	connCheck func() bool
	mu        sync.RWMutex
}

// NewServer creates a status server.
func NewServer(probeID, serverURL string, connCheck func() bool) *Server {
	return &Server{
		probeID:   probeID,
		serverURL: serverURL,
		startedAt: time.Now(),
		connCheck: connCheck,
	}
}

// Handler returns an HTTP handler for the status endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.connCheck != nil && s.connCheck() {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ok")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, "disconnected")
		}
	})

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		info := Info{
			ProbeID:      s.probeID,
			ServerURL:    s.serverURL,
			Connected:    s.connCheck != nil && s.connCheck(),
			StartedAt:    s.startedAt,
			Uptime:       time.Since(s.startedAt).Round(time.Second).String(),
			GoVersion:    runtime.Version(),
			NumGoroutine: runtime.NumGoroutine(),
			MemAlloc:     mem.Alloc,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info)
	})

	return mux
}
