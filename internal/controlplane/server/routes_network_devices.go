package server

import (
	"fmt"
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
)

func (s *Server) handleNetworkDevicesPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Network Devices</h1><p>Template not loaded</p>")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := BasePage{
		CurrentUser: s.currentTemplateUser(r),
		Version:     Version,
		ActiveNav:   "network-devices",
	}
	if err := s.pages.Render(w, "network-devices", data); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (s *Server) handleNetworkDevicesUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "network devices unavailable")
}
