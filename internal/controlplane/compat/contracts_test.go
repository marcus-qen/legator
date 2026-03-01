package compat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const (
	stableAPIRoutesFile    = "docs/contracts/api-v1-stable-routes.txt"
	stableMCPToolsFile     = "docs/contracts/mcp-stable-tools.txt"
	stableMCPResourcesFile = "docs/contracts/mcp-stable-resources.txt"
	deprecationsFilePath   = "docs/contracts/deprecations.json"
)

type deprecationsFile struct {
	APIRoutes    []deprecationEntry `json:"api_routes"`
	MCPTools     []deprecationEntry `json:"mcp_tools"`
	MCPResources []deprecationEntry `json:"mcp_resources"`
}

type deprecationEntry struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	DeprecatedIn    string `json:"deprecated_in"`
	RemovalNotBefore string `json:"removal_not_before"`
	Replacement     string `json:"replacement"`
	ChangeNote      string `json:"change_note"`
}

func TestCompatibilityContract_APIRoutes(t *testing.T) {
	repoRoot := mustRepoRoot(t)
	stable := mustReadStableList(t, filepath.Join(repoRoot, stableAPIRoutesFile))
	current := mustExtractAPIRoutes(t, repoRoot)
	deprecations := mustReadDeprecations(t, filepath.Join(repoRoot, deprecationsFilePath)).APIRoutes

	assertSurfaceContract(t, "API route", stable, current, deprecations, stableAPIRoutesFile)
}

func TestCompatibilityContract_MCPTools(t *testing.T) {
	repoRoot := mustRepoRoot(t)
	stable := mustReadStableList(t, filepath.Join(repoRoot, stableMCPToolsFile))
	current := mustExtractMCPTools(t, repoRoot)
	deprecations := mustReadDeprecations(t, filepath.Join(repoRoot, deprecationsFilePath)).MCPTools

	assertSurfaceContract(t, "MCP tool", stable, current, deprecations, stableMCPToolsFile)
}

func TestCompatibilityContract_MCPResources(t *testing.T) {
	repoRoot := mustRepoRoot(t)
	stable := mustReadStableList(t, filepath.Join(repoRoot, stableMCPResourcesFile))
	current := mustExtractMCPResources(t, repoRoot)
	deprecations := mustReadDeprecations(t, filepath.Join(repoRoot, deprecationsFilePath)).MCPResources

	assertSurfaceContract(t, "MCP resource", stable, current, deprecations, stableMCPResourcesFile)
}

func assertSurfaceContract(t *testing.T, surface string, stable, current []string, deprecations []deprecationEntry, stableFile string) {
	t.Helper()

	stableSet := make(map[string]struct{}, len(stable))
	for _, id := range stable {
		if _, exists := stableSet[id]; exists {
			t.Fatalf("duplicate %s in %s: %q", surface, stableFile, id)
		}
		stableSet[id] = struct{}{}
	}

	currentSet := make(map[string]struct{}, len(current))
	for _, id := range current {
		if _, exists := currentSet[id]; exists {
			t.Fatalf("duplicate current %s: %q", surface, id)
		}
		currentSet[id] = struct{}{}
	}

	removedSet := make(map[string]struct{})
	for _, entry := range deprecations {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			t.Fatalf("deprecation entry missing id for %s", surface)
		}
		if _, ok := stableSet[id]; !ok {
			t.Fatalf("deprecation entry for unknown %s %q; keep stable baselines append-only", surface, id)
		}

		status := strings.TrimSpace(entry.Status)
		if status != "deprecated" && status != "removed" {
			t.Fatalf("deprecation entry for %s %q has invalid status %q (want deprecated|removed)", surface, id, entry.Status)
		}
		if strings.TrimSpace(entry.DeprecatedIn) == "" {
			t.Fatalf("deprecation entry for %s %q missing deprecated_in", surface, id)
		}
		if strings.TrimSpace(entry.RemovalNotBefore) == "" {
			t.Fatalf("deprecation entry for %s %q missing removal_not_before", surface, id)
		}
		if strings.TrimSpace(entry.Replacement) == "" {
			t.Fatalf("deprecation entry for %s %q missing replacement (use \"none\" when no replacement)", surface, id)
		}
		if strings.TrimSpace(entry.ChangeNote) == "" {
			t.Fatalf("deprecation entry for %s %q missing change_note", surface, id)
		}

		if status == "removed" {
			removedSet[id] = struct{}{}
		}
	}

	missing := make([]string, 0)
	for id := range stableSet {
		_, removed := removedSet[id]
		_, present := currentSet[id]

		if removed && present {
			missing = append(missing, fmt.Sprintf("%s (marked removed but still present)", id))
			continue
		}
		if !removed && !present {
			missing = append(missing, id)
		}
	}

	unexpected := make([]string, 0)
	for id := range currentSet {
		if _, ok := stableSet[id]; !ok {
			unexpected = append(unexpected, id)
		}
	}

	sort.Strings(missing)
	sort.Strings(unexpected)

	if len(missing) > 0 {
		t.Fatalf(
			"compatibility contract violation: %d stable %s(s) missing or inconsistent:\n%s\n\nIf intentional, record deprecation metadata in %s (status=removed) and document [compat:deprecate]/[compat:remove] in CHANGELOG + release notes.",
			len(missing), strings.ToLower(surface), strings.Join(missing, "\n"), deprecationsFilePath,
		)
	}

	if len(unexpected) > 0 {
		t.Fatalf(
			"compatibility contract requires explicit baseline updates for new stable %s(s):\n%s\n\nUpdate %s and annotate CHANGELOG/release notes with [compat:additive].",
			strings.ToLower(surface), strings.Join(unexpected, "\n"), stableFile,
		)
	}
}

func mustExtractAPIRoutes(t *testing.T, repoRoot string) []string {
	t.Helper()
	path := filepath.Join(repoRoot, "internal/controlplane/server/routes.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read routes source: %v", err)
	}

	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`)
	matches := re.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		t.Fatalf("no route registrations found in %s", path)
	}

	seen := make(map[string]struct{})
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		pattern := strings.TrimSpace(match[1])
		if pattern == "" {
			continue
		}
		if strings.Contains(pattern, "/api/v1/") || pattern == "GET /healthz" || pattern == "GET /version" || pattern == "GET /mcp" || pattern == "POST /mcp" {
			seen[pattern] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func mustExtractMCPTools(t *testing.T, repoRoot string) []string {
	t.Helper()
	path := filepath.Join(repoRoot, "internal/controlplane/mcpserver/tools.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mcp tools source: %v", err)
	}

	re := regexp.MustCompile(`Name:\s*"([^"]+)"`)
	matches := re.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		t.Fatalf("no MCP tools found in %s", path)
	}

	seen := make(map[string]struct{})
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		name := strings.TrimSpace(match[1])
		if strings.HasPrefix(name, "legator_") {
			seen[name] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func mustExtractMCPResources(t *testing.T, repoRoot string) []string {
	t.Helper()
	path := filepath.Join(repoRoot, "internal/controlplane/mcpserver/resources.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mcp resources source: %v", err)
	}

	re := regexp.MustCompile(`"(legator://[^"]+)"`)
	matches := re.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		t.Fatalf("no MCP resources found in %s", path)
	}

	seen := make(map[string]struct{})
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		uri := strings.TrimSpace(match[1])
		if strings.HasPrefix(uri, "legator://") {
			seen[uri] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func mustReadStableList(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open stable contract %s: %v", path, err)
	}
	defer file.Close()

	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		seen[line] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stable contract %s: %v", path, err)
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func mustReadDeprecations(t *testing.T, path string) deprecationsFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read deprecations file %s: %v", path, err)
	}

	var out deprecationsFile
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode deprecations file %s: %v", path, err)
	}
	return out
}

func mustRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
