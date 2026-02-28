package kubeflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// CommandRunner executes fixed kubectl command invocations.
type CommandRunner interface {
	Run(ctx context.Context, command string, args ...string) (stdout []byte, stderr []byte, err error)
}

// ExecCommandRunner runs commands through os/exec.
type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, command string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// ClientConfig configures the kubectl-based client.
type ClientConfig struct {
	Binary     string
	Kubeconfig string
	Context    string
	Namespace  string
	Timeout    time.Duration
	Runner     CommandRunner
}

// CLIClient implements Kubeflow integration through guarded kubectl reads.
type CLIClient struct {
	binary     string
	kubeconfig string
	context    string
	namespace  string
	timeout    time.Duration
	runner     CommandRunner
}

type trackedResource struct {
	kind string
	name string
}

var defaultTrackedResources = []trackedResource{
	{kind: "Pod", name: "pods"},
	{kind: "Notebook", name: "notebooks.kubeflow.org"},
	{kind: "Pipeline", name: "pipelines.kubeflow.org"},
	{kind: "Run", name: "runs.kubeflow.org"},
	{kind: "Experiment", name: "experiments.kubeflow.org"},
	{kind: "Workflow", name: "workflows.argoproj.io"},
	{kind: "TFJob", name: "tfjobs.kubeflow.org"},
	{kind: "PyTorchJob", name: "pytorchjobs.kubeflow.org"},
	{kind: "MPIJob", name: "mpijobs.kubeflow.org"},
	{kind: "XGBoostJob", name: "xgboostjobs.kubeflow.org"},
}

// NewCLIClient builds a kubectl-backed Kubeflow client.
func NewCLIClient(cfg ClientConfig) *CLIClient {
	binary := strings.TrimSpace(cfg.Binary)
	if binary == "" {
		binary = "kubectl"
	}
	namespace := strings.TrimSpace(cfg.Namespace)
	if namespace == "" {
		namespace = "kubeflow"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	runner := cfg.Runner
	if runner == nil {
		runner = ExecCommandRunner{}
	}

	return &CLIClient{
		binary:     binary,
		kubeconfig: strings.TrimSpace(cfg.Kubeconfig),
		context:    strings.TrimSpace(cfg.Context),
		namespace:  namespace,
		timeout:    timeout,
		runner:     runner,
	}
}

// Status returns connectivity + inventory summary without exposing raw cluster objects.
func (c *CLIClient) Status(ctx context.Context) (Status, error) {
	status := Status{
		Namespace: c.namespace,
		Context:   c.context,
		CheckedAt: time.Now().UTC(),
		Summary: InventoryBrief{
			Counts: make(map[string]int),
		},
	}

	kubectlVersion, err := c.clientVersion(ctx)
	if err != nil {
		return status, err
	}
	status.KubectlVersion = kubectlVersion

	inv, invErr := c.Inventory(ctx)
	if invErr != nil {
		status.Connected = false
		status.LastError = invErr.Error()
		status.Warnings = append(status.Warnings, "inventory unavailable")
		if ce := new(ClientError); errors.As(invErr, &ce) && ce.Code == "cli_missing" {
			return status, invErr
		}
		return status, nil
	}

	status.Connected = true
	status.Summary = toInventoryBrief(inv)
	status.ServerVersion = c.serverVersion(ctx)
	return status, nil
}

// Inventory returns normalized Kubeflow resource snapshots.
func (c *CLIClient) Inventory(ctx context.Context) (Inventory, error) {
	inventory := Inventory{
		Namespace:   c.namespace,
		Context:     c.context,
		CollectedAt: time.Now().UTC(),
		Counts:      make(map[string]int),
		Resources:   make([]ResourceSnapshot, 0, 32),
	}

	if _, err := c.clientVersion(ctx); err != nil {
		return inventory, err
	}

	if err := c.ensureNamespaceReachable(ctx); err != nil {
		return inventory, err
	}

	available, err := c.availableResources(ctx)
	if err != nil {
		return inventory, err
	}

	var (
		hadSuccess bool
		warnings   []string
	)
	for _, resource := range defaultTrackedResources {
		if !available[resource.name] {
			continue
		}

		items, err := c.listResource(ctx, resource)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %s", resource.kind, err.Error()))
			inventory.Partial = true
			continue
		}
		hadSuccess = true
		if len(items) == 0 {
			continue
		}

		inventory.Resources = append(inventory.Resources, items...)
		inventory.Counts[resource.kind] += len(items)
	}

	sort.Slice(inventory.Resources, func(i, j int) bool {
		lhs := inventory.Resources[i]
		rhs := inventory.Resources[j]
		if lhs.Kind != rhs.Kind {
			return lhs.Kind < rhs.Kind
		}
		if lhs.Namespace != rhs.Namespace {
			return lhs.Namespace < rhs.Namespace
		}
		return lhs.Name < rhs.Name
	})

	inventory.Warnings = dedupeAndSort(warnings)
	if !hadSuccess && len(warnings) > 0 {
		return inventory, &ClientError{
			Code:    "inventory_unavailable",
			Message: "failed to collect kubeflow inventory",
			Detail:  strings.Join(inventory.Warnings, "; "),
		}
	}

	return inventory, nil
}

// Refresh executes a fresh status + inventory collection, gated by server policy.
func (c *CLIClient) Refresh(ctx context.Context) (RefreshResult, error) {
	status, err := c.Status(ctx)
	if err != nil {
		return RefreshResult{}, err
	}
	inventory, err := c.Inventory(ctx)
	if err != nil {
		return RefreshResult{}, err
	}
	status.Summary = toInventoryBrief(inventory)
	status.CheckedAt = time.Now().UTC()
	status.Connected = true
	if status.ServerVersion == "" {
		status.ServerVersion = c.serverVersion(ctx)
	}
	return RefreshResult{Status: status, Inventory: inventory}, nil
}

func (c *CLIClient) clientVersion(ctx context.Context) (string, error) {
	stdout, stderr, err := c.run(ctx, append(c.baseArgs(), "version", "--client=true", "-o", "json")...)
	if err != nil {
		return "", classifyKubectlError(err, stderr)
	}

	var payload struct {
		ClientVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"clientVersion"`
	}
	if err := json.Unmarshal(stdout, &payload); err != nil {
		return "", &ClientError{Code: "parse_error", Message: "failed to parse kubectl client version", Detail: err.Error()}
	}

	return strings.TrimSpace(payload.ClientVersion.GitVersion), nil
}

func (c *CLIClient) serverVersion(ctx context.Context) string {
	stdout, _, err := c.run(ctx, append(c.baseArgs(), "version", "-o", "json")...)
	if err != nil {
		return ""
	}

	var payload struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if err := json.Unmarshal(stdout, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.ServerVersion.GitVersion)
}

func (c *CLIClient) ensureNamespaceReachable(ctx context.Context) error {
	args := append(c.baseArgs(), "get", "namespace", c.namespace, "-o", "json")
	_, stderr, err := c.run(ctx, args...)
	if err != nil {
		lower := strings.ToLower(string(stderr))
		if strings.Contains(lower, "notfound") || strings.Contains(lower, "not found") {
			return &ClientError{Code: "namespace_missing", Message: "kubeflow namespace not found", Detail: strings.TrimSpace(string(stderr))}
		}
		return classifyKubectlError(err, stderr)
	}
	return nil
}

func (c *CLIClient) availableResources(ctx context.Context) (map[string]bool, error) {
	args := append(c.baseArgs(), "api-resources", "--verbs=list", "-o", "name")
	stdout, stderr, err := c.run(ctx, args...)
	if err != nil {
		return nil, classifyKubectlError(err, stderr)
	}
	result := make(map[string]bool)
	for _, line := range strings.Split(string(stdout), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		result[name] = true
	}
	return result, nil
}

func (c *CLIClient) listResource(ctx context.Context, resource trackedResource) ([]ResourceSnapshot, error) {
	args := append(c.baseArgs(), "get", resource.name, "-n", c.namespace, "-o", "json")
	stdout, stderr, err := c.run(ctx, args...)
	if err != nil {
		return nil, classifyKubectlError(err, stderr)
	}

	var payload struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name              string            `json:"name"`
				Namespace         string            `json:"namespace"`
				CreationTimestamp string            `json:"creationTimestamp"`
				Labels            map[string]string `json:"labels"`
			} `json:"metadata"`
			Status map[string]any `json:"status"`
		} `json:"items"`
	}

	if err := json.Unmarshal(stdout, &payload); err != nil {
		return nil, &ClientError{Code: "parse_error", Message: "failed to parse kubeflow resource list", Detail: err.Error()}
	}

	out := make([]ResourceSnapshot, 0, len(payload.Items))
	for _, item := range payload.Items {
		kind := strings.TrimSpace(item.Kind)
		if kind == "" {
			kind = resource.kind
		}
		rs := ResourceSnapshot{
			Kind:      kind,
			Name:      strings.TrimSpace(item.Metadata.Name),
			Namespace: strings.TrimSpace(item.Metadata.Namespace),
			Status:    deriveStatus(item.Status),
			Labels:    cloneStringMap(item.Metadata.Labels),
		}
		if rs.Namespace == "" {
			rs.Namespace = c.namespace
		}
		if createdAt, err := time.Parse(time.RFC3339, strings.TrimSpace(item.Metadata.CreationTimestamp)); err == nil {
			rs.CreatedAt = createdAt.UTC()
		}
		if rs.Name == "" {
			continue
		}
		out = append(out, rs)
	}

	return out, nil
}

func (c *CLIClient) run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	runCtx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	return c.runner.Run(runCtx, c.binary, args...)
}

func (c *CLIClient) baseArgs() []string {
	args := make([]string, 0, 4)
	if c.kubeconfig != "" {
		args = append(args, "--kubeconfig", c.kubeconfig)
	}
	if c.context != "" {
		args = append(args, "--context", c.context)
	}
	return args
}

func classifyKubectlError(err error, stderr []byte) error {
	stderrText := strings.TrimSpace(string(stderr))

	var execErr *exec.Error
	if errors.As(err, &execErr) {
		if errors.Is(execErr.Err, exec.ErrNotFound) {
			return &ClientError{Code: "cli_missing", Message: "kubectl CLI not found", Detail: "binary is not available in PATH"}
		}
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &ClientError{Code: "timeout", Message: "kubeflow command timed out", Detail: err.Error()}
	}

	lower := strings.ToLower(stderrText)
	if strings.Contains(lower, "unauthorized") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "you must be logged in") {
		return &ClientError{Code: "auth_failed", Message: "kubernetes authentication failed", Detail: stderrText}
	}
	if strings.Contains(lower, "connection refused") || strings.Contains(lower, "no such host") || strings.Contains(lower, "unable to connect") {
		return &ClientError{Code: "cluster_unreachable", Message: "kubernetes cluster unreachable", Detail: stderrText}
	}

	if stderrText == "" {
		stderrText = err.Error()
	}
	return &ClientError{Code: "command_failed", Message: "kubectl command failed", Detail: stderrText}
}

func deriveStatus(status map[string]any) string {
	if status == nil {
		return "unknown"
	}
	for _, key := range []string{"phase", "state", "status"} {
		if value, ok := status[key]; ok {
			if s := strings.TrimSpace(fmt.Sprintf("%v", value)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	if conditions, ok := status["conditions"].([]any); ok {
		for _, rawCondition := range conditions {
			condition, ok := rawCondition.(map[string]any)
			if !ok {
				continue
			}
			condStatus := strings.TrimSpace(strings.ToLower(fmt.Sprintf("%v", condition["status"])))
			condType := strings.TrimSpace(fmt.Sprintf("%v", condition["type"]))
			if condStatus == "true" && condType != "" {
				return condType
			}
		}
	}
	return "unknown"
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func dedupeAndSort(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func toInventoryBrief(inv Inventory) InventoryBrief {
	total := 0
	counts := make(map[string]int, len(inv.Counts))
	for kind, count := range inv.Counts {
		counts[kind] = count
		total += count
	}
	return InventoryBrief{Total: total, Counts: counts, Partial: inv.Partial}
}
