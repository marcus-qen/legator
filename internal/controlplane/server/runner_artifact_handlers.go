package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/artifacts"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"go.uber.org/zap"
)

const (
	runnerArtifactTokenQueryKey = "token"
)

func (s *Server) handlePresignRunnerArtifact(w http.ResponseWriter, r *http.Request) {
	if s.artifactPresigner == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "runner artifact presigner unavailable")
		return
	}

	runID := strings.TrimSpace(r.PathValue("id"))
	if runID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "run id required")
		return
	}

	sessionID, actor, ok := runnerSessionContext(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "session_required", "session context required")
		return
	}

	var req struct {
		Path       string `json:"path"`
		Scope      string `json:"scope"`
		Operation  string `json:"operation"`
		TTLSeconds int64  `json:"ttl_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	if req.TTLSeconds < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "ttl_seconds must be >= 0")
		return
	}

	issued, err := s.artifactPresigner.Presign(artifacts.PresignRequest{
		RunID:       runID,
		SessionID:   sessionID,
		ScopePrefix: strings.TrimSpace(req.Scope),
		Path:        strings.TrimSpace(req.Path),
		Operation:   artifacts.Operation(strings.TrimSpace(req.Operation)),
		TTL:         time.Duration(req.TTLSeconds) * time.Second,
	})
	if err != nil {
		s.writeArtifactPresignError(w, err)
		return
	}

	transferURL := s.buildRunnerArtifactURL(r, issued.RunID, issued.Path, issued.Token)

	s.recordAudit(audit.Event{
		Type:    audit.EventRunnerArtifactURLIssued,
		Actor:   actor,
		Summary: fmt.Sprintf("Runner artifact URL issued: %s", issued.RunID),
		Detail: map[string]any{
			"run_id":       issued.RunID,
			"path":         issued.Path,
			"scope":        issued.ScopePrefix,
			"operation":    issued.Operation,
			"expires_at":   issued.ExpiresAt,
			"session_id":   sessionID,
			"transfer_url": transferURL,
		},
	})

	resp := struct {
		RunID      string              `json:"run_id"`
		SessionID  string              `json:"session_id"`
		Path       string              `json:"path"`
		Scope      string              `json:"scope"`
		Operation  artifacts.Operation `json:"operation"`
		URL        string              `json:"url"`
		Token      string              `json:"token"`
		IssuedAt   time.Time           `json:"issued_at"`
		ExpiresAt  time.Time           `json:"expires_at"`
		TTLSeconds int64               `json:"ttl_seconds"`
	}{
		RunID:      issued.RunID,
		SessionID:  issued.SessionID,
		Path:       issued.Path,
		Scope:      issued.ScopePrefix,
		Operation:  issued.Operation,
		URL:        transferURL,
		Token:      issued.Token,
		IssuedAt:   issued.IssuedAt,
		ExpiresAt:  issued.ExpiresAt,
		TTLSeconds: int64(issued.ExpiresAt.Sub(issued.IssuedAt) / time.Second),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleUploadRunnerArtifact(w http.ResponseWriter, r *http.Request) {
	s.handleRunnerArtifactTransfer(w, r, artifacts.OperationUpload)
}

func (s *Server) handleDownloadRunnerArtifact(w http.ResponseWriter, r *http.Request) {
	s.handleRunnerArtifactTransfer(w, r, artifacts.OperationDownload)
}

func (s *Server) handleRunnerArtifactTransfer(w http.ResponseWriter, r *http.Request, op artifacts.Operation) {
	if s.artifactPresigner == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "runner artifact presigner unavailable")
		return
	}

	runID := strings.TrimSpace(r.PathValue("id"))
	artifactPath := strings.TrimSpace(r.PathValue("path"))
	token := strings.TrimSpace(r.URL.Query().Get(runnerArtifactTokenQueryKey))

	claims, err := s.artifactPresigner.Validate(artifacts.ValidateRequest{
		Token:     token,
		RunID:     runID,
		Path:      artifactPath,
		Operation: op,
	})
	if err != nil {
		s.recordRunnerArtifactDenied(runID, artifactPath, op, err)
		s.writeArtifactTransferError(w, err)
		return
	}

	storagePath, err := s.runnerArtifactStoragePath(claims.RunID, claims.Path)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	switch op {
	case artifacts.OperationUpload:
		if err := os.MkdirAll(filepath.Dir(storagePath), 0o750); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to create artifact directory")
			return
		}

		f, err := os.OpenFile(storagePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to open artifact target")
			return
		}
		defer f.Close()

		written, err := io.Copy(f, r.Body)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to store artifact")
			return
		}

		s.recordAudit(audit.Event{
			Type:    audit.EventRunnerArtifactUploaded,
			Actor:   "runner",
			Summary: fmt.Sprintf("Runner artifact uploaded: %s", claims.RunID),
			Detail: map[string]any{
				"run_id":    claims.RunID,
				"path":      claims.Path,
				"scope":     claims.ScopePrefix,
				"operation": op,
				"bytes":     written,
			},
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run_id":        claims.RunID,
			"path":          claims.Path,
			"bytes_written": written,
		})
	case artifacts.OperationDownload:
		f, err := os.Open(storagePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSONError(w, http.StatusNotFound, "not_found", "artifact not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to open artifact")
			return
		}
		defer f.Close()

		stat, err := f.Stat()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to stat artifact")
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
		w.Header().Set("X-Legator-Artifact-Path", claims.Path)
		if _, err := io.Copy(w, f); err != nil {
			s.logger.Warn("runner artifact download copy failed", zap.String("run_id", claims.RunID), zap.String("path", claims.Path), zap.Error(err))
			return
		}

		s.recordAudit(audit.Event{
			Type:    audit.EventRunnerArtifactDownloaded,
			Actor:   "runner",
			Summary: fmt.Sprintf("Runner artifact downloaded: %s", claims.RunID),
			Detail: map[string]any{
				"run_id":    claims.RunID,
				"path":      claims.Path,
				"scope":     claims.ScopePrefix,
				"operation": op,
				"bytes":     stat.Size(),
			},
		})
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "unsupported artifact operation")
	}
}

func (s *Server) recordRunnerArtifactDenied(runID, artifactPath string, op artifacts.Operation, err error) {
	runID = strings.TrimSpace(runID)
	artifactPath = strings.TrimSpace(artifactPath)

	s.logger.Warn("runner artifact access denied",
		zap.String("run_id", runID),
		zap.String("path", artifactPath),
		zap.String("operation", string(op)),
		zap.Error(err),
	)

	s.recordAudit(audit.Event{
		Type:    audit.EventRunnerArtifactAccessDenied,
		Actor:   "runner",
		Summary: fmt.Sprintf("Runner artifact access denied: %s", runID),
		Detail: map[string]any{
			"run_id":    runID,
			"path":      artifactPath,
			"operation": op,
			"error":     err.Error(),
		},
	})
}

func (s *Server) writeArtifactPresignError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, artifacts.ErrRunIDRequired),
		errors.Is(err, artifacts.ErrSessionIDRequired),
		errors.Is(err, artifacts.ErrPathRequired),
		errors.Is(err, artifacts.ErrPathInvalid),
		errors.Is(err, artifacts.ErrScopeInvalid),
		errors.Is(err, artifacts.ErrOperationRequired),
		errors.Is(err, artifacts.ErrOperationInvalid):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, artifacts.ErrScopeRejected):
		writeJSONError(w, http.StatusForbidden, "artifact_scope_rejected", err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (s *Server) writeArtifactTransferError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, artifacts.ErrRunIDRequired),
		errors.Is(err, artifacts.ErrPathRequired),
		errors.Is(err, artifacts.ErrPathInvalid),
		errors.Is(err, artifacts.ErrOperationRequired),
		errors.Is(err, artifacts.ErrOperationInvalid):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, artifacts.ErrTokenRequired), errors.Is(err, artifacts.ErrTokenInvalid):
		writeJSONError(w, http.StatusUnauthorized, "invalid_artifact_token", err.Error())
	case errors.Is(err, artifacts.ErrTokenExpired):
		writeJSONError(w, http.StatusUnauthorized, "expired_artifact_token", err.Error())
	case errors.Is(err, artifacts.ErrScopeRejected):
		writeJSONError(w, http.StatusForbidden, "artifact_scope_rejected", err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (s *Server) runnerArtifactStoragePath(runID, artifactPath string) (string, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "", artifacts.ErrRunIDRequired
	}
	if strings.Contains(runID, "/") || strings.Contains(runID, "\\") || strings.Contains(runID, "..") {
		return "", fmt.Errorf("run_id contains invalid characters")
	}
	artifactPath = strings.TrimSpace(strings.ReplaceAll(artifactPath, "\\", "/"))
	if artifactPath == "" {
		return "", artifacts.ErrPathRequired
	}
	for _, part := range strings.Split(artifactPath, "/") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", artifacts.ErrPathInvalid
		}
	}

	root := filepath.Clean(filepath.Join(s.runnerArtifactsDir, runID))
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(artifactPath)))
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", artifacts.ErrPathInvalid
	}
	return target, nil
}

func (s *Server) buildRunnerArtifactURL(r *http.Request, runID, artifactPath, token string) string {
	base := strings.TrimSpace(s.cfg.ExternalURL)
	if base == "" {
		scheme := "http"
		if r != nil {
			if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
				scheme = forwarded
			} else if r.TLS != nil {
				scheme = "https"
			}
		}
		host := "localhost"
		if r != nil {
			host = strings.TrimSpace(r.Host)
			if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
				host = forwardedHost
			}
		}
		base = fmt.Sprintf("%s://%s", scheme, host)
	}
	base = strings.TrimRight(base, "/")

	escapedRunID := url.PathEscape(strings.TrimSpace(runID))
	pathParts := strings.Split(strings.Trim(strings.ReplaceAll(artifactPath, "\\", "/"), "/"), "/")
	escapedParts := make([]string, 0, len(pathParts))
	for _, part := range pathParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		escapedParts = append(escapedParts, url.PathEscape(part))
	}
	encodedPath := strings.Join(escapedParts, "/")

	return fmt.Sprintf("%s/artifacts/runs/%s/%s?%s=%s",
		base,
		escapedRunID,
		encodedPath,
		runnerArtifactTokenQueryKey,
		url.QueryEscape(strings.TrimSpace(token)),
	)
}
