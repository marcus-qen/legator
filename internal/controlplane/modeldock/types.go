package modeldock

import (
	"strings"
	"time"
)

const (
	FeatureProbeChat = "probe-chat"
	FeatureFleetChat = "fleet-chat"
	FeatureTask      = "task"

	SourceDB  = "db"
	SourceEnv = "env"

	EnvProfileID = "env"
)

// Profile is a persisted model provider profile.
type Profile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider"`
	BaseURL   string    `json:"base_url"`
	Model     string    `json:"model"`
	APIKey    string    `json:"-"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    string    `json:"source,omitempty"`
}

// ProfileResponse is API-safe profile output with masked key only.
type ProfileResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Provider     string    `json:"provider"`
	BaseURL      string    `json:"base_url"`
	Model        string    `json:"model"`
	APIKeyMasked string    `json:"api_key_masked"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Source       string    `json:"source,omitempty"`
}

// UsageRecord is a raw per-completion usage entry.
type UsageRecord struct {
	ID               string    `json:"id"`
	TS               time.Time `json:"ts"`
	ProfileID        string    `json:"profile_id"`
	Feature          string    `json:"feature"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
}

// UsageAggregate is grouped usage totals.
type UsageAggregate struct {
	ProfileID        string `json:"profile_id"`
	ProfileName      string `json:"profile_name,omitempty"`
	Feature          string `json:"feature"`
	Requests         int    `json:"requests"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
}

func (p Profile) ToResponse() ProfileResponse {
	return ProfileResponse{
		ID:           p.ID,
		Name:         p.Name,
		Provider:     p.Provider,
		BaseURL:      p.BaseURL,
		Model:        p.Model,
		APIKeyMasked: MaskAPIKey(p.APIKey),
		IsActive:     p.IsActive,
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
		Source:       p.Source,
	}
}

func MaskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return "****"
	}

	prefixLen := 3
	if len(key) < prefixLen {
		prefixLen = len(key)
	}
	suffixLen := 4
	if len(key) < suffixLen {
		suffixLen = len(key)
	}

	return key[:prefixLen] + "..." + key[len(key)-suffixLen:]
}

func IsValidFeature(feature string) bool {
	switch feature {
	case FeatureProbeChat, FeatureFleetChat, FeatureTask:
		return true
	default:
		return false
	}
}
