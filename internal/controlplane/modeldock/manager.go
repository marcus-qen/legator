package modeldock

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/llm"
)

var ErrNoActiveProvider = errors.New("no active LLM provider configured")

type ProviderSnapshot struct {
	ProfileID string `json:"profile_id"`
	Source    string `json:"source"`
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
}

type usageRecorder interface {
	RecordUsage(record UsageRecord) error
}

type runtimeProvider struct {
	snapshot ProviderSnapshot
	provider llm.Provider
}

// ProviderManager manages active LLM provider with atomic swaps.
type ProviderManager struct {
	mu     sync.RWMutex
	envCfg llm.ProviderConfig
	hasEnv bool
	active *runtimeProvider
}

func NewProviderManager(envCfg llm.ProviderConfig) *ProviderManager {
	mgr := &ProviderManager{
		envCfg: normalizeConfig(envCfg),
	}
	mgr.hasEnv = mgr.envCfg.Name != ""
	if mgr.hasEnv {
		mgr.active = mgr.runtimeFromConfig(EnvProfileID, SourceEnv, mgr.envCfg)
	}
	return mgr
}

func (m *ProviderManager) HasEnvFallback() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.hasEnv
}

func (m *ProviderManager) HasActiveProvider() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active != nil
}

func (m *ProviderManager) Snapshot() ProviderSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.active == nil {
		return ProviderSnapshot{}
	}
	return m.active.snapshot
}

func (m *ProviderManager) ActivateProfile(profile *Profile) error {
	if profile == nil {
		return fmt.Errorf("profile is required")
	}
	cfg := llm.ProviderConfig{
		Name:    strings.TrimSpace(profile.Provider),
		BaseURL: strings.TrimSpace(profile.BaseURL),
		APIKey:  strings.TrimSpace(profile.APIKey),
		Model:   strings.TrimSpace(profile.Model),
	}
	cfg = normalizeConfig(cfg)
	if cfg.Name == "" || cfg.BaseURL == "" || cfg.Model == "" {
		return fmt.Errorf("provider, base_url, and model are required")
	}

	runtime := m.runtimeFromConfig(profile.ID, SourceDB, cfg)

	m.mu.Lock()
	m.active = runtime
	m.mu.Unlock()
	return nil
}

func (m *ProviderManager) UseEnvFallback() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasEnv {
		m.active = nil
		return ErrNoActiveProvider
	}
	m.active = m.runtimeFromConfig(EnvProfileID, SourceEnv, m.envCfg)
	return nil
}

func (m *ProviderManager) SyncFromStore(store *Store) error {
	if store == nil {
		if m.hasEnv {
			return m.UseEnvFallback()
		}
		return ErrNoActiveProvider
	}

	hasProfiles, err := store.HasProfiles()
	if err != nil {
		return err
	}
	if !hasProfiles {
		if m.hasEnv {
			return m.UseEnvFallback()
		}
		m.mu.Lock()
		m.active = nil
		m.mu.Unlock()
		return ErrNoActiveProvider
	}

	profile, err := store.GetActiveProfile()
	if err != nil {
		if IsNotFound(err) {
			if m.hasEnv {
				return m.UseEnvFallback()
			}
			m.mu.Lock()
			m.active = nil
			m.mu.Unlock()
			return ErrNoActiveProvider
		}
		return err
	}

	return m.ActivateProfile(profile)
}

func (m *ProviderManager) Provider(feature string, recorder usageRecorder) llm.Provider {
	return &featureProvider{
		manager:  m,
		feature:  feature,
		recorder: recorder,
	}
}

func (m *ProviderManager) getActive() (*runtimeProvider, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.active == nil {
		return nil, ErrNoActiveProvider
	}
	return m.active, nil
}

func (m *ProviderManager) runtimeFromConfig(profileID, source string, cfg llm.ProviderConfig) *runtimeProvider {
	cfg = normalizeConfig(cfg)
	provider := llm.NewOpenAIProvider(cfg)
	return &runtimeProvider{
		snapshot: ProviderSnapshot{
			ProfileID: profileID,
			Source:    source,
			Provider:  cfg.Name,
			BaseURL:   cfg.BaseURL,
			Model:     cfg.Model,
		},
		provider: provider,
	}
}

func normalizeConfig(cfg llm.ProviderConfig) llm.ProviderConfig {
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Model = strings.TrimSpace(cfg.Model)
	return cfg
}

type featureProvider struct {
	manager  *ProviderManager
	feature  string
	recorder usageRecorder
}

func (f *featureProvider) Name() string {
	snapshot := f.manager.Snapshot()
	if snapshot.Provider == "" {
		return "unconfigured"
	}
	return snapshot.Provider
}

func (f *featureProvider) Complete(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	runtime, err := f.manager.getActive()
	if err != nil {
		return nil, err
	}

	resp, err := runtime.provider.Complete(ctx, req)
	if err != nil {
		return nil, err
	}

	if f.recorder != nil && IsValidFeature(f.feature) {
		_ = f.recorder.RecordUsage(UsageRecord{
			TS:               time.Now().UTC(),
			ProfileID:        runtime.snapshot.ProfileID,
			Feature:          f.feature,
			PromptTokens:     resp.PromptTokens,
			CompletionTokens: resp.CompTokens,
			TotalTokens:      resp.PromptTokens + resp.CompTokens,
		})
	}

	return resp, nil
}
