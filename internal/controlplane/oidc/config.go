package oidc

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Config controls optional OIDC login for the control plane.
type Config struct {
	Enabled         bool              `json:"enabled"`
	ProviderURL     string            `json:"provider_url,omitempty"`
	ClientID        string            `json:"client_id,omitempty"`
	ClientSecret    string            `json:"client_secret,omitempty"`
	RedirectURL     string            `json:"redirect_url,omitempty"`
	Scopes          []string          `json:"scopes,omitempty"`
	RoleClaim       string            `json:"role_claim,omitempty"`
	RoleMapping     map[string]string `json:"role_mapping,omitempty"`
	DefaultRole     string            `json:"default_role,omitempty"`
	AutoCreateUsers bool              `json:"auto_create_users"`
	ProviderName    string            `json:"provider_name,omitempty"`
}

// DefaultConfig returns a secure-by-default (disabled) OIDC config.
func DefaultConfig() Config {
	return Config{
		Enabled:         false,
		Scopes:          []string{"openid", "email", "profile", "groups"},
		RoleClaim:       "groups",
		RoleMapping:     map[string]string{},
		DefaultRole:     "viewer",
		AutoCreateUsers: true,
		ProviderName:    "",
	}
}

// ApplyEnv overlays LEGATOR_OIDC_* environment variables onto cfg.
func ApplyEnv(cfg Config) Config {
	cfg = cfg.normalize()

	if v, ok := envBool("LEGATOR_OIDC_ENABLED"); ok {
		cfg.Enabled = v
	}
	if v := strings.TrimSpace(os.Getenv("LEGATOR_OIDC_PROVIDER_URL")); v != "" {
		cfg.ProviderURL = v
	}
	if v := strings.TrimSpace(os.Getenv("LEGATOR_OIDC_CLIENT_ID")); v != "" {
		cfg.ClientID = v
	}
	if v := strings.TrimSpace(os.Getenv("LEGATOR_OIDC_CLIENT_SECRET")); v != "" {
		cfg.ClientSecret = v
	}
	if v := strings.TrimSpace(os.Getenv("LEGATOR_OIDC_REDIRECT_URL")); v != "" {
		cfg.RedirectURL = v
	}
	if v := strings.TrimSpace(os.Getenv("LEGATOR_OIDC_SCOPES")); v != "" {
		cfg.Scopes = parseCSV(v)
	}
	if v := strings.TrimSpace(os.Getenv("LEGATOR_OIDC_ROLE_CLAIM")); v != "" {
		cfg.RoleClaim = v
	}
	if v := strings.TrimSpace(os.Getenv("LEGATOR_OIDC_ROLE_MAPPING")); v != "" {
		if m, err := parseRoleMapping(v); err == nil {
			cfg.RoleMapping = m
		}
	}
	if v := strings.TrimSpace(os.Getenv("LEGATOR_OIDC_DEFAULT_ROLE")); v != "" {
		cfg.DefaultRole = normalizeRole(v)
	}
	if v, ok := envBool("LEGATOR_OIDC_AUTO_CREATE_USERS"); ok {
		cfg.AutoCreateUsers = v
	}
	if v := strings.TrimSpace(os.Getenv("LEGATOR_OIDC_PROVIDER_NAME")); v != "" {
		cfg.ProviderName = v
	}

	return cfg.normalize()
}

// Validate checks required settings when OIDC is enabled.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}

	missing := make([]string, 0, 4)
	if strings.TrimSpace(c.ProviderURL) == "" {
		missing = append(missing, "provider_url")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		missing = append(missing, "client_id")
	}
	if strings.TrimSpace(c.ClientSecret) == "" {
		missing = append(missing, "client_secret")
	}
	if strings.TrimSpace(c.RedirectURL) == "" {
		missing = append(missing, "redirect_url")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("oidc config missing required fields: %s", strings.Join(missing, ", "))
	}

	return nil
}

// EffectiveProviderName returns configured provider_name, or a derived fallback.
func (c Config) EffectiveProviderName() string {
	if strings.TrimSpace(c.ProviderName) != "" {
		return strings.TrimSpace(c.ProviderName)
	}

	u, err := url.Parse(strings.TrimSpace(c.ProviderURL))
	if err == nil {
		host := u.Hostname()
		if host != "" {
			part := strings.Split(host, ".")[0]
			if part != "" {
				return humanizeProvider(part)
			}
		}
	}

	return "OIDC"
}

func (c Config) normalize() Config {
	if len(c.Scopes) == 0 {
		c.Scopes = append([]string{}, DefaultConfig().Scopes...)
	}
	if strings.TrimSpace(c.RoleClaim) == "" {
		c.RoleClaim = DefaultConfig().RoleClaim
	}
	if c.RoleMapping == nil {
		c.RoleMapping = map[string]string{}
	}
	if normalizeRole(c.DefaultRole) == "" {
		c.DefaultRole = DefaultConfig().DefaultRole
	} else {
		c.DefaultRole = normalizeRole(c.DefaultRole)
	}
	for k, v := range c.RoleMapping {
		norm := normalizeRole(v)
		if norm == "" {
			delete(c.RoleMapping, k)
			continue
		}
		c.RoleMapping[k] = norm
	}
	if c.Scopes != nil {
		seen := make(map[string]struct{}, len(c.Scopes))
		norm := make([]string, 0, len(c.Scopes))
		for _, scope := range c.Scopes {
			scope = strings.TrimSpace(scope)
			if scope == "" {
				continue
			}
			if _, ok := seen[scope]; ok {
				continue
			}
			seen[scope] = struct{}{}
			norm = append(norm, scope)
		}
		if len(norm) == 0 {
			norm = append(norm, DefaultConfig().Scopes...)
		}
		c.Scopes = norm
	}
	return c
}

func parseCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func parseRoleMapping(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]string{}, nil
	}

	if strings.HasPrefix(raw, "{") {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return nil, fmt.Errorf("parse role mapping json: %w", err)
		}
		for k, v := range parsed {
			parsed[k] = normalizeRole(v)
			if parsed[k] == "" {
				delete(parsed, k)
			}
		}
		return parsed, nil
	}

	mapping := make(map[string]string)
	pairs := strings.Split(raw, ",")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("invalid role mapping entry %q", pair)
		}
		key := strings.TrimSpace(kv[0])
		value := normalizeRole(kv[1])
		if key == "" || value == "" {
			continue
		}
		mapping[key] = value
	}
	return mapping, nil
}

func envBool(name string) (bool, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false, false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		switch strings.ToLower(raw) {
		case "1", "yes", "y", "on":
			return true, true
		case "0", "no", "n", "off":
			return false, true
		default:
			return false, false
		}
	}
	return v, true
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin":
		return "admin"
	case "operator":
		return "operator"
	case "viewer":
		return "viewer"
	default:
		return ""
	}
}

func humanizeProvider(raw string) string {
	raw = strings.ReplaceAll(raw, "-", " ")
	raw = strings.ReplaceAll(raw, "_", " ")
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return "OIDC"
	}
	for i, p := range parts {
		if p == "" {
			continue
		}
		runes := []rune(strings.ToLower(p))
		runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
		parts[i] = string(runes)
	}
	return strings.Join(parts, " ")
}
