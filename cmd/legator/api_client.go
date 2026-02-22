package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type legatorAPIClient struct {
	baseURL    string
	bearer     string
	httpClient *http.Client
}

type whoAmIResponse struct {
	Subject       string   `json:"subject"`
	Email         string   `json:"email"`
	Name          string   `json:"name"`
	Groups        []string `json:"groups"`
	EffectiveRole string   `json:"effectiveRole"`
	Permissions   map[string]struct {
		Allowed bool   `json:"allowed"`
		Reason  string `json:"reason"`
	} `json:"permissions"`
}

func tryAPIClient() (*legatorAPIClient, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false, fmt.Errorf("failed to resolve home directory: %w", err)
	}

	path := filepath.Join(home, ".config", "legator", "token.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to read token cache %s: %w", path, err)
	}

	var tok tokenCache
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, false, fmt.Errorf("failed to parse token cache %s: %w", path, err)
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return nil, false, fmt.Errorf("token cache %s has no access_token; run 'legator login'", path)
	}
	if !tok.ExpiresAt.IsZero() && time.Now().After(tok.ExpiresAt.Add(-30*time.Second)) {
		if strings.TrimSpace(tok.RefreshToken) == "" {
			return nil, false, fmt.Errorf("cached token is expired and has no refresh token; run 'legator login' again")
		}
		if err := refreshCachedToken(&tok); err != nil {
			return nil, false, fmt.Errorf("cached token is expired and refresh failed: %w; run 'legator login'", err)
		}
	}

	apiURL := strings.TrimSpace(tok.APIURL)
	if apiURL == "" {
		apiURL = envOr("LEGATOR_API_URL", "")
	}
	if apiURL == "" {
		return nil, false, errors.New("token cache missing api_url; run 'legator login --api-url <url>'")
	}

	return &legatorAPIClient{
		baseURL: strings.TrimSuffix(apiURL, "/"),
		bearer:  strings.TrimSpace(tok.AccessToken),
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}, true, nil
}

func (c *legatorAPIClient) getJSON(path string, out any) error {
	return c.doJSON(http.MethodGet, path, nil, out)
}

func (c *legatorAPIClient) postJSON(path string, body any, out any) error {
	return c.doJSON(http.MethodPost, path, body, out)
}

func fetchWhoAmI(apiClient *legatorAPIClient) (*whoAmIResponse, error) {
	var resp whoAmIResponse
	if err := apiClient.getJSON("/api/v1/me", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *legatorAPIClient) doJSON(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to encode request body: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.bearer)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("api request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("api unauthorized (%d): %s; run 'legator login'", resp.StatusCode, msg)
		}
		return fmt.Errorf("api error (%d): %s", resp.StatusCode, msg)
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("failed to parse api response: %w", err)
	}
	return nil
}

func refreshCachedToken(tok *tokenCache) error {
	issuer := strings.TrimSpace(tok.OIDCIssuer)
	if issuer == "" {
		return errors.New("token cache missing oidc_issuer")
	}
	clientID := strings.TrimSpace(tok.OIDCClientID)
	if clientID == "" {
		return errors.New("token cache missing oidc_client_id")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	discovery, err := fetchOIDCDiscovery(ctx, issuer)
	if err != nil {
		return fmt.Errorf("oidc discovery failed: %w", err)
	}

	clientSecret := envOr("LEGATOR_OIDC_CLIENT_SECRET", "")
	resp, err := requestRefreshToken(ctx, discovery.TokenEndpoint, clientID, clientSecret, tok.RefreshToken)
	if err != nil {
		return err
	}
	if resp.AccessToken == "" {
		return errors.New("refresh token response missing access_token")
	}

	tok.AccessToken = resp.AccessToken
	if strings.TrimSpace(resp.RefreshToken) != "" {
		tok.RefreshToken = resp.RefreshToken
	}
	if strings.TrimSpace(resp.TokenType) != "" {
		tok.TokenType = resp.TokenType
	}
	if strings.TrimSpace(resp.Scope) != "" {
		tok.Scope = resp.Scope
	}
	if resp.ExpiresIn <= 0 {
		resp.ExpiresIn = 900
	}
	now := time.Now().UTC()
	tok.IssuedAt = now
	tok.ExpiresAt = now.Add(time.Duration(resp.ExpiresIn) * time.Second)

	if _, err := saveTokenCache(*tok); err != nil {
		return fmt.Errorf("failed to persist refreshed token: %w", err)
	}
	return nil
}

func requestRefreshToken(
	ctx context.Context,
	tokenEndpoint, clientID, clientSecret, refreshToken string,
) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", err)
	}

	var out tokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to parse refresh response: %w", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return &out, nil
	}
	if out.Error == "" {
		out.Error = fmt.Sprintf("http_%d", resp.StatusCode)
		out.ErrorDescription = strings.TrimSpace(string(body))
	}
	detail := out.Error
	if out.ErrorDescription != "" {
		detail += ": " + out.ErrorDescription
	}
	return nil, fmt.Errorf("token refresh failed: %s", detail)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}
