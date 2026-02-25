package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"os"
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

// Register connects to the control plane and registers with a token.
func Register(ctx context.Context, serverURL, token string, logger *zap.Logger) (*Config, error) {
	hostname, _ := os.Hostname()

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

	url := serverURL + "/api/v1/register"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
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
		json.NewDecoder(resp.Body).Decode(&errResp)
		return nil, fmt.Errorf("registration failed (%d): %s", resp.StatusCode, errResp.Error)
	}

	var regResp registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
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
