package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/marcus-qen/legator/internal/controlplane/modeldock"
	"github.com/marcus-qen/legator/internal/controlplane/providerproxy"
)

func (s *Server) resolveProviderProxyCredentials(_ context.Context, _ string, requestedModel string) (providerproxy.ProviderCredentials, error) {
	creds := providerproxy.ProviderCredentials{}

	if s.modelProviderMgr != nil {
		snapshot := s.modelProviderMgr.Snapshot()
		creds.Provider = strings.TrimSpace(snapshot.Provider)
		creds.BaseURL = strings.TrimSpace(snapshot.BaseURL)
		creds.Model = strings.TrimSpace(snapshot.Model)
	}

	if s.modelDockStore != nil {
		if profile, err := s.modelDockStore.GetActiveProfile(); err == nil {
			creds.Provider = strings.TrimSpace(profile.Provider)
			creds.BaseURL = strings.TrimSpace(profile.BaseURL)
			creds.APIKey = strings.TrimSpace(profile.APIKey)
			if m := strings.TrimSpace(profile.Model); m != "" {
				creds.Model = m
			}
		} else if !modeldock.IsNotFound(err) {
			return providerproxy.ProviderCredentials{}, fmt.Errorf("resolve active model profile: %w", err)
		}
	}

	if creds.Provider == "" {
		creds.Provider = strings.TrimSpace(s.cfg.LLM.Provider)
	}
	if creds.BaseURL == "" {
		creds.BaseURL = strings.TrimSpace(s.cfg.LLM.BaseURL)
	}
	if creds.Model == "" {
		creds.Model = strings.TrimSpace(s.cfg.LLM.Model)
	}
	if creds.APIKey == "" {
		creds.APIKey = strings.TrimSpace(s.cfg.LLM.APIKey)
	}

	if creds.Provider == "" {
		creds.Provider = strings.TrimSpace(os.Getenv("LEGATOR_LLM_PROVIDER"))
	}
	if creds.BaseURL == "" {
		creds.BaseURL = strings.TrimSpace(os.Getenv("LEGATOR_LLM_BASE_URL"))
	}
	if creds.Model == "" {
		creds.Model = strings.TrimSpace(os.Getenv("LEGATOR_LLM_MODEL"))
	}
	if creds.APIKey == "" {
		creds.APIKey = strings.TrimSpace(os.Getenv("LEGATOR_LLM_API_KEY"))
	}

	if model := strings.TrimSpace(requestedModel); model != "" {
		creds.Model = model
	}
	if creds.Provider == "" {
		creds.Provider = "openai"
	}
	if creds.BaseURL == "" {
		creds.BaseURL = "https://api.openai.com/v1"
	}
	if creds.Model == "" {
		return providerproxy.ProviderCredentials{}, fmt.Errorf("provider model is not configured")
	}
	if creds.APIKey == "" {
		return providerproxy.ProviderCredentials{}, fmt.Errorf("provider api key is not configured")
	}

	return creds, nil
}

func (s *Server) handleProviderProxy(w http.ResponseWriter, r *http.Request) {
	if s.providerProxy == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "provider proxy unavailable")
		return
	}
	runID := strings.TrimSpace(r.PathValue("id"))
	if runID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "run id required")
		return
	}
	s.providerProxy.HandleHTTP(w, r, runID)
}
