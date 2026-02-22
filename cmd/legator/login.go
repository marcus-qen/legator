package main

import (
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

type oidcDiscovery struct {
	Issuer             string `json:"issuer"`
	TokenEndpoint      string `json:"token_endpoint"`
	DeviceAuthEndpoint string `json:"device_authorization_endpoint"`
}

type deviceAuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type tokenCache struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope,omitempty"`
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`

	OIDCIssuer   string `json:"oidc_issuer"`
	OIDCClientID string `json:"oidc_client_id"`
	APIURL       string `json:"api_url,omitempty"`
}

func handleLogin(args []string) {
	issuer := envOr("LEGATOR_OIDC_ISSUER", "https://keycloak.lab.k-dev.uk/realms/dev-lab")
	clientID := envOr("LEGATOR_OIDC_CLIENT_ID", "legator-cli")
	clientSecret := envOr("LEGATOR_OIDC_CLIENT_SECRET", "")
	apiURL := envOr("LEGATOR_API_URL", "http://127.0.0.1:8090")
	scope := envOr("LEGATOR_OIDC_SCOPE", "openid profile email offline_access")
	verify := true

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--issuer":
			i++
			if i >= len(args) {
				fatal(errors.New("--issuer requires a value"))
			}
			issuer = args[i]
		case "--client-id":
			i++
			if i >= len(args) {
				fatal(errors.New("--client-id requires a value"))
			}
			clientID = args[i]
		case "--client-secret":
			i++
			if i >= len(args) {
				fatal(errors.New("--client-secret requires a value"))
			}
			clientSecret = args[i]
		case "--api-url":
			i++
			if i >= len(args) {
				fatal(errors.New("--api-url requires a value"))
			}
			apiURL = args[i]
		case "--scope":
			i++
			if i >= len(args) {
				fatal(errors.New("--scope requires a value"))
			}
			scope = args[i]
		case "--verify":
			verify = true
		case "--no-verify":
			verify = false
		case "-h", "--help", "help":
			printLoginUsage()
			return
		default:
			fatal(fmt.Errorf("unknown login option: %s", args[i]))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	discovery, err := fetchOIDCDiscovery(ctx, issuer)
	fatal(err)
	if discovery.DeviceAuthEndpoint == "" {
		fatal(fmt.Errorf("issuer does not expose device authorization endpoint: %s", issuer))
	}

	deviceResp, err := requestDeviceCode(ctx, discovery.DeviceAuthEndpoint, clientID, clientSecret, scope)
	fatal(err)

	fmt.Println("🔐 OIDC device login")
	fmt.Println("──────────────────")
	if deviceResp.VerificationURIComplete != "" {
		fmt.Printf("Open this URL in your browser:\n  %s\n\n", deviceResp.VerificationURIComplete)
	} else {
		fmt.Printf("Open this URL in your browser:\n  %s\n", deviceResp.VerificationURI)
		fmt.Printf("Enter this code:\n  %s\n\n", deviceResp.UserCode)
	}
	fmt.Println("Waiting for authorization...")

	tok, err := pollDeviceToken(discovery.TokenEndpoint, clientID, clientSecret, deviceResp)
	fatal(err)

	cache := tokenCache{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.TokenType,
		Scope:        tok.Scope,
		IssuedAt:     time.Now().UTC(),
		ExpiresAt:    time.Now().UTC().Add(time.Duration(tok.ExpiresIn) * time.Second),
		OIDCIssuer:   strings.TrimSuffix(issuer, "/"),
		OIDCClientID: clientID,
		APIURL:       strings.TrimSuffix(apiURL, "/"),
	}

	path, err := saveTokenCache(cache)
	fatal(err)

	fmt.Println("\n✅ Login successful")
	fmt.Printf("Token saved: %s\n", path)
	fmt.Printf("Access token expires: %s\n", cache.ExpiresAt.Format(time.RFC3339))
	fmt.Printf("API URL: %s\n", cache.APIURL)

	if verify {
		apiClient := &legatorAPIClient{
			baseURL: strings.TrimSuffix(cache.APIURL, "/"),
			bearer:  strings.TrimSpace(cache.AccessToken),
			httpClient: &http.Client{
				Timeout: 20 * time.Second,
			},
		}

		me, err := fetchWhoAmI(apiClient)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n⚠️  Login token acquired, but API verification failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "   You can still run: legator whoami (after fixing API URL/reachability)")
			return
		}

		fmt.Println("\nAPI verification")
		fmt.Println("────────────────")
		fmt.Printf("Authenticated as: %s (%s)\n", fallback(me.Email, "unknown"), fallback(me.EffectiveRole, "unknown"))
	}
}

func printLoginUsage() {
	fmt.Print(`Usage: legator login [options]

Options:
  --issuer <url>         OIDC issuer URL (default: env or dev-lab Keycloak)
  --client-id <id>       OIDC client ID (default: legator-cli)
  --client-secret <val>  OIDC client secret (optional; for confidential clients)
  --api-url <url>        Legator API base URL to store with token
  --scope <scopes>       OAuth scopes (default: openid profile email offline_access)
  --verify               Verify token immediately via /api/v1/me (default)
  --no-verify            Skip immediate API verification
`)
}

func fetchOIDCDiscovery(ctx context.Context, issuer string) (*oidcDiscovery, error) {
	issuer = strings.TrimSuffix(issuer, "/")
	wellKnown := issuer + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("oidc discovery failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out oidcDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("invalid oidc discovery response: %w", err)
	}
	if out.TokenEndpoint == "" {
		return nil, errors.New("oidc discovery missing token_endpoint")
	}
	return &out, nil
}

func requestDeviceCode(
	ctx context.Context,
	endpoint, clientID, clientSecret, scope string,
) (*deviceAuthResponse, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	if scope != "" {
		form.Set("scope", scope)
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device authorization request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("device authorization failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out deviceAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("invalid device authorization response: %w", err)
	}
	if out.DeviceCode == "" {
		return nil, errors.New("device authorization response missing device_code")
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return &out, nil
}

func pollDeviceToken(tokenEndpoint, clientID, clientSecret string, device *deviceAuthResponse) (*tokenResponse, error) {
	interval := time.Duration(device.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)

	for {
		if time.Now().After(deadline) {
			return nil, errors.New("device login timed out before authorization completed")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resp, err := requestDeviceTokenOnce(ctx, tokenEndpoint, clientID, clientSecret, device.DeviceCode)
		cancel()
		if err != nil {
			return nil, err
		}

		switch resp.Error {
		case "":
			if resp.AccessToken == "" {
				return nil, errors.New("token endpoint returned success without access_token")
			}
			if resp.TokenType == "" {
				resp.TokenType = "Bearer"
			}
			if resp.ExpiresIn <= 0 {
				resp.ExpiresIn = 900
			}
			return resp, nil
		case "authorization_pending":
			// keep waiting
		case "slow_down":
			interval += 2 * time.Second
		case "expired_token":
			return nil, errors.New("device code expired before login completed")
		default:
			detail := resp.Error
			if resp.ErrorDescription != "" {
				detail = detail + ": " + resp.ErrorDescription
			}
			return nil, fmt.Errorf("token request failed: %s", detail)
		}

		time.Sleep(interval)
	}
}

func requestDeviceTokenOnce(
	ctx context.Context,
	tokenEndpoint, clientID, clientSecret, deviceCode string,
) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("client_id", clientID)
	form.Set("device_code", deviceCode)
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
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	var out tokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return &out, nil
	}
	if out.Error == "" {
		out.Error = fmt.Sprintf("http_%d", resp.StatusCode)
		out.ErrorDescription = strings.TrimSpace(string(body))
	}
	return &out, nil
}

func saveTokenCache(cache tokenCache) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".config", "legator")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create token directory: %w", err)
	}

	path := filepath.Join(dir, "token.json")
	b, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to encode token cache: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", fmt.Errorf("failed to write token cache: %w", err)
	}
	return path, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
