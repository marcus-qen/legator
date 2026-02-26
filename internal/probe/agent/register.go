package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"go.uber.org/zap"
)

type registerRequest struct {
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
}

type registerResponse struct {
	ProbeID  string `json:"probe_id"`
	APIKey   string `json:"api_key"`
	PolicyID string `json:"policy_id"`
}

// RegisterOptions controls optional registration behavior.
type RegisterOptions struct {
	HostnameOverride string
	Tags             []string
}

// Register connects to the control plane and registers with a token.
func Register(ctx context.Context, serverURL, token string, logger *zap.Logger) (*Config, error) {
	return RegisterWithOptions(ctx, serverURL, token, logger, RegisterOptions{})
}

// RegisterWithOptions connects to the control plane and registers with optional hostname and tags.
func RegisterWithOptions(ctx context.Context, serverURL, token string, logger *zap.Logger, opts RegisterOptions) (*Config, error) {
	hostname := strings.TrimSpace(opts.HostnameOverride)
	if hostname == "" {
		hostname, _ = os.Hostname()
	}

	req := registerRequest{
		Token:    token,
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Version:  "dev",
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	url := strings.TrimRight(serverURL, "/") + "/api/v1/register"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errResp struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		return nil, fmt.Errorf("registration failed (%d): %s", resp.StatusCode, errResp.Error)
	}

	var regResp registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	tags := normalizeTags(opts.Tags)
	if len(tags) > 0 {
		if err := setProbeTags(ctx, serverURL, regResp.ProbeID, regResp.APIKey, tags); err != nil {
			return nil, fmt.Errorf("set probe tags: %w", err)
		}
	}

	logger.Info("registered successfully",
		zap.String("probe_id", regResp.ProbeID),
	)

	return &Config{
		ServerURL: serverURL,
		ProbeID:   regResp.ProbeID,
		APIKey:    regResp.APIKey,
		PolicyID:  regResp.PolicyID,
	}, nil
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}

	result := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func setProbeTags(ctx context.Context, serverURL, probeID, apiKey string, tags []string) error {
	payload := struct {
		Tags []string `json:"tags"`
	}{
		Tags: tags,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal tags request: %w", err)
	}

	url := strings.TrimRight(serverURL, "/") + "/api/v1/probes/" + probeID + "/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create tags request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send tags request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var errResp struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		if errResp.Error == "" {
			errResp.Error = resp.Status
		}
		return fmt.Errorf("tags update failed (%d): %s", resp.StatusCode, errResp.Error)
	}

	return nil
}
