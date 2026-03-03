package server

import (
	"fmt"
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/sandbox"
	"go.uber.org/zap"
)

// SandboxesPageData is passed to the sandboxes.html template.
type SandboxesPageData struct {
	BasePage
}

// SandboxDetailPageData is passed to the sandbox-detail.html template.
type SandboxDetailPageData struct {
	BasePage
	Session *sandbox.SandboxSession
	Tasks   []*sandbox.Task
}

// handleSandboxesPage serves GET /sandboxes.
func (s *Server) handleSandboxesPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Sandboxes</h1><p>Template not loaded</p>")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := SandboxesPageData{
		BasePage: BasePage{
			CurrentUser: s.currentTemplateUser(r),
			Version:     Version,
			ActiveNav:   "sandboxes",
		},
	}
	if err := s.pages.Render(w, "sandboxes", data); err != nil {
		s.logger.Error("failed to render sandboxes page", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

// handleSandboxDetailPage serves GET /sandboxes/{id}.
func (s *Server) handleSandboxDetailPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	id := r.PathValue("id")

	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<h1>Sandbox: %s</h1><p>Template not loaded</p>", id)
		return
	}

	if s.sandboxStore == nil {
		http.Error(w, "sandbox store unavailable", http.StatusServiceUnavailable)
		return
	}

	sess, err := s.sandboxStore.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var tasks []*sandbox.Task
	if s.sandboxTaskStore != nil {
		tasks, err = s.sandboxTaskStore.ListTasks(sandbox.TaskListFilter{
			SandboxID: id,
			Limit:     100,
		})
		if err != nil {
			s.logger.Warn("failed to list sandbox tasks for detail page",
				zap.String("sandbox_id", id), zap.Error(err))
		}
	}
	if tasks == nil {
		tasks = []*sandbox.Task{}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := SandboxDetailPageData{
		BasePage: BasePage{
			CurrentUser: s.currentTemplateUser(r),
			Version:     Version,
			ActiveNav:   "sandboxes",
		},
		Session: sess,
		Tasks:   tasks,
	}
	if err := s.pages.Render(w, "sandbox-detail", data); err != nil {
		s.logger.Error("failed to render sandbox detail page",
			zap.String("sandbox_id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}
