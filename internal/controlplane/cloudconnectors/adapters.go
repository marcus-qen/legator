package cloudconnectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"time"
)

// Scanner discovers cloud assets for a connector.
type Scanner interface {
	Scan(ctx context.Context, connector Connector) ([]Asset, error)
}

// CommandRunner executes fixed provider CLI commands.
type CommandRunner interface {
	Run(ctx context.Context, command string, args ...string) (stdout []byte, stderr []byte, err error)
}

// ExecCommandRunner runs commands via os/exec.
type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, command string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// CLIAdapter implements provider scans using fixed command allowlists.
type CLIAdapter struct {
	runner CommandRunner
}

func NewCLIAdapter() *CLIAdapter {
	return &CLIAdapter{runner: ExecCommandRunner{}}
}

func NewCLIAdapterWithRunner(runner CommandRunner) *CLIAdapter {
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	return &CLIAdapter{runner: runner}
}

func (a *CLIAdapter) Scan(ctx context.Context, connector Connector) ([]Asset, error) {
	provider := normalizeProvider(connector.Provider)
	connector.Provider = provider

	switch provider {
	case ProviderAWS:
		return a.scanAWS(ctx, connector)
	case ProviderGCP:
		return a.scanGCP(ctx, connector)
	case ProviderAzure:
		return a.scanAzure(ctx, connector)
	default:
		return nil, &ScanError{Code: "unsupported_provider", Message: fmt.Sprintf("unsupported provider: %s", provider)}
	}
}

func (a *CLIAdapter) scanAWS(ctx context.Context, connector Connector) ([]Asset, error) {
	identityOut, identityErr, err := a.runner.Run(ctx, "aws", "sts", "get-caller-identity", "--output", "json")
	if err != nil {
		return nil, classifyProviderError(ProviderAWS, "aws", err, identityErr)
	}

	instancesOut, instancesErr, err := a.runner.Run(ctx, "aws", "ec2", "describe-instances", "--output", "json")
	if err != nil {
		return nil, classifyProviderError(ProviderAWS, "aws", err, instancesErr)
	}

	var identity map[string]any
	if err := json.Unmarshal(identityOut, &identity); err != nil {
		return nil, &ScanError{Code: "parse_error", Message: "failed to parse aws caller identity", Detail: err.Error()}
	}
	accountID := stringField(identity, "Account")
	now := time.Now().UTC()

	assets := make([]Asset, 0, 32)
	assets = append(assets, Asset{
		ConnectorID:  connector.ID,
		Provider:     ProviderAWS,
		ScopeID:      accountID,
		Region:       "global",
		AssetType:    "account",
		AssetID:      accountID,
		DisplayName:  firstNonEmpty(stringField(identity, "Arn"), accountID),
		Status:       "active",
		RawJSON:      mustMarshal(identity),
		DiscoveredAt: now,
	})

	var payload map[string]any
	if err := json.Unmarshal(instancesOut, &payload); err != nil {
		return nil, &ScanError{Code: "parse_error", Message: "failed to parse aws instances", Detail: err.Error()}
	}

	for _, reservation := range sliceField(payload, "Reservations") {
		reservationMap, ok := reservation.(map[string]any)
		if !ok {
			continue
		}
		for _, item := range sliceField(reservationMap, "Instances") {
			instance, ok := item.(map[string]any)
			if !ok {
				continue
			}
			instanceID := stringField(instance, "InstanceId")
			if strings.TrimSpace(instanceID) == "" {
				continue
			}
			state := stringField(mapField(instance, "State"), "Name")
			zone := stringField(mapField(instance, "Placement"), "AvailabilityZone")
			name := awsNameTag(instance)
			if name == "" {
				name = instanceID
			}

			assets = append(assets, Asset{
				ConnectorID:  connector.ID,
				Provider:     ProviderAWS,
				ScopeID:      accountID,
				Region:       normalizeRegionFromZone(zone),
				AssetType:    "instance",
				AssetID:      instanceID,
				DisplayName:  name,
				Status:       firstNonEmpty(state, "unknown"),
				RawJSON:      mustMarshal(instance),
				DiscoveredAt: now,
			})
		}
	}

	return assets, nil
}

func (a *CLIAdapter) scanGCP(ctx context.Context, connector Connector) ([]Asset, error) {
	projectsOut, projectsErr, err := a.runner.Run(ctx, "gcloud", "projects", "list", "--format=json")
	if err != nil {
		return nil, classifyProviderError(ProviderGCP, "gcloud", err, projectsErr)
	}

	instancesOut, instancesErr, err := a.runner.Run(ctx, "gcloud", "compute", "instances", "list", "--format=json")
	if err != nil {
		return nil, classifyProviderError(ProviderGCP, "gcloud", err, instancesErr)
	}

	var projects []map[string]any
	if err := json.Unmarshal(projectsOut, &projects); err != nil {
		return nil, &ScanError{Code: "parse_error", Message: "failed to parse gcp projects", Detail: err.Error()}
	}

	now := time.Now().UTC()
	assets := make([]Asset, 0, len(projects)+32)
	projectIDs := make([]string, 0, len(projects))
	for _, project := range projects {
		projectID := stringField(project, "projectId")
		if projectID == "" {
			continue
		}
		projectIDs = append(projectIDs, projectID)
		assets = append(assets, Asset{
			ConnectorID:  connector.ID,
			Provider:     ProviderGCP,
			ScopeID:      projectID,
			Region:       "global",
			AssetType:    "project",
			AssetID:      projectID,
			DisplayName:  firstNonEmpty(stringField(project, "name"), projectID),
			Status:       firstNonEmpty(stringField(project, "lifecycleState"), "active"),
			RawJSON:      mustMarshal(project),
			DiscoveredAt: now,
		})
	}

	var instances []map[string]any
	if err := json.Unmarshal(instancesOut, &instances); err != nil {
		return nil, &ScanError{Code: "parse_error", Message: "failed to parse gcp instances", Detail: err.Error()}
	}

	defaultProject := ""
	if len(projectIDs) == 1 {
		defaultProject = projectIDs[0]
	}

	for _, instance := range instances {
		name := stringField(instance, "name")
		instanceID := firstNonEmpty(stringField(instance, "id"), name)
		if strings.TrimSpace(instanceID) == "" {
			continue
		}
		zone := firstNonEmpty(stringField(instance, "zone"), stringField(instance, "location"))
		projectID := firstNonEmpty(
			stringField(instance, "project"),
			projectFromSelfLink(stringField(instance, "selfLink")),
			defaultProject,
		)

		assets = append(assets, Asset{
			ConnectorID:  connector.ID,
			Provider:     ProviderGCP,
			ScopeID:      projectID,
			Region:       normalizeRegionFromZone(zone),
			AssetType:    "instance",
			AssetID:      instanceID,
			DisplayName:  firstNonEmpty(name, instanceID),
			Status:       firstNonEmpty(stringField(instance, "status"), "unknown"),
			RawJSON:      mustMarshal(instance),
			DiscoveredAt: now,
		})
	}

	return assets, nil
}

func (a *CLIAdapter) scanAzure(ctx context.Context, connector Connector) ([]Asset, error) {
	accountOut, accountErr, err := a.runner.Run(ctx, "az", "account", "show", "-o", "json")
	if err != nil {
		return nil, classifyProviderError(ProviderAzure, "az", err, accountErr)
	}

	vmOut, vmErr, err := a.runner.Run(ctx, "az", "vm", "list", "-d", "-o", "json")
	if err != nil {
		return nil, classifyProviderError(ProviderAzure, "az", err, vmErr)
	}

	var account map[string]any
	if err := json.Unmarshal(accountOut, &account); err != nil {
		return nil, &ScanError{Code: "parse_error", Message: "failed to parse azure account", Detail: err.Error()}
	}

	now := time.Now().UTC()
	subscriptionID := stringField(account, "id")
	assets := make([]Asset, 0, 32)
	assets = append(assets, Asset{
		ConnectorID:  connector.ID,
		Provider:     ProviderAzure,
		ScopeID:      subscriptionID,
		Region:       "global",
		AssetType:    "account",
		AssetID:      subscriptionID,
		DisplayName:  firstNonEmpty(stringField(account, "name"), subscriptionID),
		Status:       "active",
		RawJSON:      mustMarshal(account),
		DiscoveredAt: now,
	})

	var vms []map[string]any
	if err := json.Unmarshal(vmOut, &vms); err != nil {
		return nil, &ScanError{Code: "parse_error", Message: "failed to parse azure vm list", Detail: err.Error()}
	}

	for _, vm := range vms {
		vmResourceID := stringField(vm, "id")
		vmName := stringField(vm, "name")
		scopeID := firstNonEmpty(subscriptionID, subscriptionFromAzureResourceID(vmResourceID))

		assets = append(assets, Asset{
			ConnectorID:  connector.ID,
			Provider:     ProviderAzure,
			ScopeID:      scopeID,
			Region:       firstNonEmpty(stringField(vm, "location"), "global"),
			AssetType:    "vm",
			AssetID:      firstNonEmpty(vmResourceID, vmName),
			DisplayName:  firstNonEmpty(vmName, vmResourceID),
			Status:       firstNonEmpty(stringField(vm, "powerState"), stringField(vm, "provisioningState"), "unknown"),
			RawJSON:      mustMarshal(vm),
			DiscoveredAt: now,
		})
	}

	return assets, nil
}

func classifyProviderError(provider, binary string, err error, stderr []byte) *ScanError {
	stderrText := strings.TrimSpace(string(stderr))

	var execErr *exec.Error
	if errors.As(err, &execErr) {
		if errors.Is(execErr.Err, exec.ErrNotFound) {
			return &ScanError{
				Code:    "cli_missing",
				Message: fmt.Sprintf("%s CLI not found", provider),
				Detail:  fmt.Sprintf("binary %q is not available in PATH", binary),
			}
		}
	}

	if looksLikeAuthError(provider, stderrText) {
		return &ScanError{Code: "auth_failed", Message: fmt.Sprintf("%s CLI is not authenticated", provider), Detail: stderrText}
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &ScanError{Code: "scan_timeout", Message: "provider scan command timed out", Detail: err.Error()}
	}

	if stderrText == "" {
		stderrText = err.Error()
	}

	return &ScanError{Code: "command_failed", Message: fmt.Sprintf("%s CLI command failed", provider), Detail: stderrText}
}

func looksLikeAuthError(provider, stderr string) bool {
	stderr = strings.ToLower(strings.TrimSpace(stderr))
	if stderr == "" {
		return false
	}

	switch provider {
	case ProviderAWS:
		for _, marker := range []string{"unable to locate credentials", "invalidclienttokenid", "expiredtoken", "accessdenied", "security token"} {
			if strings.Contains(stderr, marker) {
				return true
			}
		}
	case ProviderGCP:
		for _, marker := range []string{"re-authenticate", "gcloud auth login", "no credentialed accounts", "active account"} {
			if strings.Contains(stderr, marker) {
				return true
			}
		}
	case ProviderAzure:
		for _, marker := range []string{"az login", "not logged in", "please run", "interaction required"} {
			if strings.Contains(stderr, marker) {
				return true
			}
		}
	}

	return false
}

func awsNameTag(instance map[string]any) string {
	for _, item := range sliceField(instance, "Tags") {
		tag, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(stringField(tag, "Key"), "name") {
			return stringField(tag, "Value")
		}
	}
	return ""
}

func mapField(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	value, ok := m[key]
	if !ok {
		return nil
	}
	out, _ := value.(map[string]any)
	return out
}

func sliceField(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	value, ok := m[key]
	if !ok {
		return nil
	}
	slice, _ := value.([]any)
	return slice
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}

	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	case int, int64, int32, uint, uint64:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func mustMarshal(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if v := strings.TrimSpace(value); v != "" {
			return v
		}
	}
	return ""
}

func normalizeRegionFromZone(raw string) string {
	zone := strings.TrimSpace(raw)
	if zone == "" {
		return "global"
	}
	zone = path.Base(zone)
	if len(zone) >= 2 {
		last := zone[len(zone)-1]
		prev := zone[len(zone)-2]
		if last >= 'a' && last <= 'z' {
			// us-east-1a -> us-east-1
			if prev >= '0' && prev <= '9' {
				return zone[:len(zone)-1]
			}
			// europe-west1-b -> europe-west1
			if prev == '-' {
				return zone[:len(zone)-2]
			}
		}
	}
	return zone
}

func projectFromSelfLink(selfLink string) string {
	selfLink = strings.TrimSpace(selfLink)
	if selfLink == "" {
		return ""
	}
	parts := strings.Split(selfLink, "/")
	for idx := 0; idx < len(parts)-1; idx++ {
		if parts[idx] == "projects" {
			return strings.TrimSpace(parts[idx+1])
		}
	}
	return ""
}

func subscriptionFromAzureResourceID(resourceID string) string {
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		return ""
	}
	parts := strings.Split(resourceID, "/")
	for idx := 0; idx < len(parts)-1; idx++ {
		if strings.EqualFold(parts[idx], "subscriptions") {
			return strings.TrimSpace(parts[idx+1])
		}
	}
	return ""
}
