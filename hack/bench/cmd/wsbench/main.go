package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type config struct {
	mode        string
	serverURL   string
	adminAPIKey string
	connections int
	workers     int
	duration    time.Duration
	insecure    bool
	dialTimeout time.Duration
	writeTO     time.Duration
	msgBytes    int
}

type registerResponse struct {
	ProbeID string `json:"probe_id"`
	APIKey  string `json:"api_key"`
}

type tokenResponse struct {
	Token string `json:"token"`
}

type probeConn struct {
	id   string
	conn *websocket.Conn
}

type errorCollector struct {
	mu   sync.Mutex
	errC map[string]int
}

func (c *errorCollector) add(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if msg == "" {
		msg = "unknown error"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.errC == nil {
		c.errC = map[string]int{}
	}
	c.errC[msg]++
}

func (c *errorCollector) top(n int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	type pair struct {
		msg   string
		count int
	}
	items := make([]pair, 0, len(c.errC))
	for msg, count := range c.errC {
		items = append(items, pair{msg: msg, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].msg < items[j].msg
		}
		return items[i].count > items[j].count
	})
	if n > len(items) {
		n = len(items)
	}
	out := make([]string, 0, n)
	for _, item := range items[:n] {
		out = append(out, fmt.Sprintf("%dx %s", item.count, item.msg))
	}
	return out
}

func main() {
	cfg := parseFlags()
	if err := validateConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(2)
	}

	ctx := context.Background()
	client := &http.Client{Timeout: 20 * time.Second}

	token, err := issueMultiUseToken(ctx, client, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "issue registration token: %v\n", err)
		os.Exit(1)
	}

	probes, setupDuration, setupErrs := setupProbeConnections(ctx, cfg, client, token)
	defer closeAll(probes)

	if len(probes) == 0 {
		fmt.Fprintf(os.Stderr, "no websocket probes connected\n")
		for _, line := range setupErrs.top(5) {
			fmt.Fprintf(os.Stderr, "  %s\n", line)
		}
		os.Exit(1)
	}

	switch cfg.mode {
	case "connections":
		runConnectionsMode(cfg, probes, setupDuration, setupErrs)
	case "throughput":
		runThroughputMode(cfg, probes, setupDuration, setupErrs)
	default:
		fmt.Fprintf(os.Stderr, "unsupported mode %q\n", cfg.mode)
		os.Exit(2)
	}
}

func parseFlags() config {
	cfg := config{}
	flag.StringVar(&cfg.mode, "mode", "connections", "Benchmark mode: connections|throughput")
	flag.StringVar(&cfg.serverURL, "server", "http://127.0.0.1:8080", "Control-plane base URL")
	flag.StringVar(&cfg.adminAPIKey, "admin-api-key", "", "Optional admin API key for auth-protected /api/v1/tokens")
	flag.IntVar(&cfg.connections, "connections", 1000, "Target websocket probe connections")
	flag.IntVar(&cfg.workers, "workers", runtime.NumCPU()*4, "Concurrent setup workers for register+dial")
	flag.DurationVar(&cfg.duration, "duration", 15*time.Second, "Benchmark duration / hold time")
	flag.BoolVar(&cfg.insecure, "insecure", false, "Skip TLS verification for HTTPS/WSS")
	flag.DurationVar(&cfg.dialTimeout, "dial-timeout", 8*time.Second, "WebSocket dial timeout")
	flag.DurationVar(&cfg.writeTO, "write-timeout", 2*time.Second, "Per-message write timeout")
	flag.IntVar(&cfg.msgBytes, "message-bytes", 256, "Approximate payload bytes per message (throughput mode)")
	flag.Parse()
	return cfg
}

func validateConfig(cfg config) error {
	if cfg.connections <= 0 {
		return fmt.Errorf("connections must be > 0")
	}
	if cfg.workers <= 0 {
		return fmt.Errorf("workers must be > 0")
	}
	if cfg.duration <= 0 {
		return fmt.Errorf("duration must be > 0")
	}
	if cfg.msgBytes < 64 {
		return fmt.Errorf("message-bytes must be >= 64")
	}
	if _, err := url.Parse(cfg.serverURL); err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}
	return nil
}

func runConnectionsMode(cfg config, probes []probeConn, setupDuration time.Duration, setupErrs *errorCollector) {
	fmt.Printf("mode=connections\n")
	fmt.Printf("target_connections=%d\n", cfg.connections)
	fmt.Printf("connected=%d\n", len(probes))
	fmt.Printf("failed=%d\n", cfg.connections-len(probes))
	fmt.Printf("setup_seconds=%.2f\n", setupDuration.Seconds())
	fmt.Printf("connection_setup_rate=%.2f conn/s\n", float64(len(probes))/setupDuration.Seconds())
	fmt.Printf("hold_duration=%s\n", cfg.duration)
	fmt.Printf("max_concurrent_observed=%d\n", len(probes))
	for _, line := range setupErrs.top(5) {
		fmt.Printf("error_top=%s\n", line)
	}

	<-time.After(cfg.duration)
}

func runThroughputMode(cfg config, probes []probeConn, setupDuration time.Duration, setupErrs *errorCollector) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()

	payload := buildPayload(cfg.msgBytes)
	var (
		sentCount  atomic.Uint64
		errCount   atomic.Uint64
		readerWG   sync.WaitGroup
		writerWG   sync.WaitGroup
	)

	for i := range probes {
		pc := probes[i]
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for {
				_, _, err := pc.conn.ReadMessage()
				if err != nil {
					return
				}
			}
		}()
	}

	start := time.Now()
	for i := range probes {
		pc := probes[i]
		writerWG.Add(1)
		go func() {
			defer writerWG.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				_ = pc.conn.SetWriteDeadline(time.Now().Add(cfg.writeTO))
				if err := pc.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
					if ctx.Err() != nil {
						return
					}
					errCount.Add(1)
					return
				}
				sentCount.Add(1)
			}
		}()
	}

	writerWG.Wait()
	elapsed := time.Since(start)
	closeAll(probes)
	readerWG.Wait()

	messages := sentCount.Load()
	bytesSent := float64(messages * uint64(len(payload)))
	msgRate := float64(messages) / elapsed.Seconds()
	mbps := (bytesSent / (1024 * 1024)) / elapsed.Seconds()

	fmt.Printf("mode=throughput\n")
	fmt.Printf("connections=%d\n", len(probes))
	fmt.Printf("message_size_bytes=%d\n", len(payload))
	fmt.Printf("duration_seconds=%.2f\n", elapsed.Seconds())
	fmt.Printf("messages_sent=%d\n", messages)
	fmt.Printf("messages_per_second=%.2f\n", msgRate)
	fmt.Printf("payload_throughput_mib_per_sec=%.2f\n", mbps)
	fmt.Printf("writer_errors=%d\n", errCount.Load())
	fmt.Printf("setup_seconds=%.2f\n", setupDuration.Seconds())
	fmt.Printf("setup_failures=%d\n", cfg.connections-len(probes))
	for _, line := range setupErrs.top(5) {
		fmt.Printf("setup_error_top=%s\n", line)
	}
}

func setupProbeConnections(ctx context.Context, cfg config, client *http.Client, token string) ([]probeConn, time.Duration, *errorCollector) {
	start := time.Now()
	errs := &errorCollector{}
	jobs := make(chan int)
	results := make(chan probeConn, cfg.connections)

	workerCount := cfg.workers
	if workerCount > cfg.connections {
		workerCount = cfg.connections
	}

	var wg sync.WaitGroup
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				probe, err := registerProbe(ctx, client, cfg, token, idx)
				if err != nil {
					errs.add(fmt.Errorf("register: %w", err))
					continue
				}
				conn, err := dialProbeWS(ctx, cfg, probe)
				if err != nil {
					errs.add(fmt.Errorf("dial ws: %w", err))
					continue
				}
				results <- probeConn{id: probe.ProbeID, conn: conn}
			}
		}()
	}

	for i := 0; i < cfg.connections; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(results)

	out := make([]probeConn, 0, cfg.connections)
	for pc := range results {
		out = append(out, pc)
	}
	return out, time.Since(start), errs
}

func issueMultiUseToken(ctx context.Context, client *http.Client, cfg config) (string, error) {
	endpoint := strings.TrimRight(cfg.serverURL, "/") + "/api/v1/tokens?multi_use=true&no_expiry=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.adminAPIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.adminAPIKey))
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("token endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Token) == "" {
		return "", errors.New("token response missing token field")
	}
	return out.Token, nil
}

func registerProbe(ctx context.Context, client *http.Client, cfg config, token string, idx int) (registerResponse, error) {
	endpoint := strings.TrimRight(cfg.serverURL, "/") + "/api/v1/register"
	payload := map[string]any{
		"token":    token,
		"hostname": fmt.Sprintf("bench-probe-%d", idx),
		"os":       "linux",
		"arch":     "amd64",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return registerResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return registerResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return registerResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return registerResponse{}, fmt.Errorf("register returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}

	var out registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return registerResponse{}, err
	}
	if strings.TrimSpace(out.ProbeID) == "" || strings.TrimSpace(out.APIKey) == "" {
		return registerResponse{}, errors.New("register response missing probe_id or api_key")
	}
	return out, nil
}

func dialProbeWS(ctx context.Context, cfg config, reg registerResponse) (*websocket.Conn, error) {
	serverURL, err := url.Parse(cfg.serverURL)
	if err != nil {
		return nil, err
	}

	wsScheme := "ws"
	if strings.EqualFold(serverURL.Scheme, "https") {
		wsScheme = "wss"
	}
	wsURL := url.URL{
		Scheme: wsScheme,
		Host:   serverURL.Host,
		Path:   "/ws/probe",
	}
	q := wsURL.Query()
	q.Set("id", reg.ProbeID)
	wsURL.RawQuery = q.Encode()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+reg.APIKey)

	dialer := websocket.Dialer{
		HandshakeTimeout: cfg.dialTimeout,
		EnableCompression: false,
		TLSClientConfig: &tls.Config{ //nolint:gosec // benchmark tooling may target self-signed test endpoints
			InsecureSkipVerify: cfg.insecure,
		},
	}
	conn, resp, err := dialer.DialContext(ctx, wsURL.String(), headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return nil, fmt.Errorf("dial %s failed: %w (status=%s, body=%s)", wsURL.String(), err, resp.Status, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("dial %s failed: %w", wsURL.String(), err)
	}
	return conn, nil
}

func buildPayload(targetBytes int) []byte {
	if targetBytes < 64 {
		targetBytes = 64
	}
	paddingSize := targetBytes - 80
	if paddingSize < 0 {
		paddingSize = 0
	}
	padding := strings.Repeat("x", paddingSize)
	msg := map[string]any{
		"id":        "bench",
		"type":      "heartbeat",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"payload": map[string]any{
			"probe_id":       "bench",
			"uptime_seconds": 1,
			"padding":        padding,
		},
	}
	out, err := json.Marshal(msg)
	if err != nil {
		return []byte(`{"id":"bench","type":"heartbeat","timestamp":"2026-01-01T00:00:00Z","payload":{"probe_id":"bench"}}`)
	}
	return out
}

func closeAll(probes []probeConn) {
	for i := range probes {
		if probes[i].conn == nil {
			continue
		}
		_ = probes[i].conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bench complete"), time.Now().Add(500*time.Millisecond))
		_ = probes[i].conn.Close()
	}
}
