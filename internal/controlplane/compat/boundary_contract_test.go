package compat

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const boundaryContractFilePath = "docs/contracts/architecture-boundaries.yaml"

type boundaryContractFile struct {
	Version            string                      `yaml:"version"`
	ContractID         string                      `yaml:"contract_id"`
	Stage              string                      `yaml:"stage"`
	Summary            string                      `yaml:"summary"`
	ModuleRoot         string                      `yaml:"module_root"`
	ExceptionRegistry  string                      `yaml:"exception_registry"`
	RequiredBoundaries []string                    `yaml:"required_boundaries"`
	Boundaries         []boundaryDefinition        `yaml:"boundaries"`
	DependencyPolicy   boundaryDependencyPolicy    `yaml:"dependency_policy"`
	Ownership          boundaryOwnershipDefinition `yaml:"ownership"`
	EnforcementModel   boundaryEnforcementModel    `yaml:"enforcement_model"`
}

type boundaryDefinition struct {
	ID              string   `yaml:"id"`
	Name            string   `yaml:"name"`
	Description     string   `yaml:"description"`
	PackagePatterns []string `yaml:"package_patterns"`
}

type boundaryDependencyPolicy struct {
	DefaultEffect string               `yaml:"default_effect"`
	Allow         []boundaryDependency `yaml:"allow"`
	Deny          []boundaryDependency `yaml:"deny"`
}

type boundaryDependency struct {
	From      string `yaml:"from"`
	To        string `yaml:"to"`
	Rationale string `yaml:"rationale"`
}

type boundaryOwnershipDefinition struct {
	Owners      []boundaryOwner      `yaml:"owners"`
	Assignments []boundaryAssignment `yaml:"assignments"`
}

type boundaryOwner struct {
	ID          string `yaml:"id"`
	DisplayName string `yaml:"display_name"`
	Scope       string `yaml:"scope"`
}

type boundaryAssignment struct {
	BoundaryID string   `yaml:"boundary_id"`
	OwnerID    string   `yaml:"owner_id"`
	KeyModules []string `yaml:"key_modules"`
}

type boundaryEnforcementModel struct {
	Stage361     string   `yaml:"stage_3_6_1"`
	Stage362     string   `yaml:"stage_3_6_2"`
	Stage363     string   `yaml:"stage_3_6_3"`
	Stage364     string   `yaml:"stage_3_6_4"`
	RolloutNotes []string `yaml:"rollout_notes"`
}

type boundaryExceptionRegistryFile struct {
	Version    string              `yaml:"version"`
	ContractID string              `yaml:"contract_id"`
	Summary    string              `yaml:"summary"`
	Exceptions []boundaryException `yaml:"exceptions"`
}

type boundaryException struct {
	ID                  string `yaml:"id"`
	FromBoundary        string `yaml:"from_boundary"`
	ToBoundary          string `yaml:"to_boundary"`
	Scope               string `yaml:"scope"`
	Rationale           string `yaml:"rationale"`
	ReviewerSignoff     string `yaml:"reviewer_signoff"`
	TrackingIssue       string `yaml:"tracking_issue"`
	ApprovedOn          string `yaml:"approved_on"`
	ExpiresOn           string `yaml:"expires_on"`
	RemovalExpectations string `yaml:"removal_expectations"`
}

func TestBoundaryContract_FileIntegrity(t *testing.T) {
	repoRoot := mustRepoRoot(t)
	contract := mustReadBoundaryContract(t, filepath.Join(repoRoot, boundaryContractFilePath))

	if strings.TrimSpace(contract.Version) == "" {
		t.Fatalf("boundary contract missing version")
	}
	if strings.TrimSpace(contract.ContractID) == "" {
		t.Fatalf("boundary contract missing contract_id")
	}
	if strings.TrimSpace(contract.Stage) == "" {
		t.Fatalf("boundary contract missing stage")
	}
	if strings.TrimSpace(contract.Summary) == "" {
		t.Fatalf("boundary contract missing summary")
	}
	if strings.TrimSpace(contract.ModuleRoot) == "" {
		t.Fatalf("boundary contract missing module_root")
	}
	if strings.TrimSpace(contract.ExceptionRegistry) == "" {
		t.Fatalf("boundary contract missing exception_registry")
	}
	if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(contract.ExceptionRegistry))); err != nil {
		t.Fatalf("boundary contract exception_registry %q not found: %v", contract.ExceptionRegistry, err)
	}

	if len(contract.Boundaries) == 0 {
		t.Fatalf("boundary contract requires at least one boundary")
	}

	boundaryIDs := map[string]struct{}{}
	patternOwner := map[string]string{}
	for _, boundary := range contract.Boundaries {
		id := strings.TrimSpace(boundary.ID)
		if id == "" {
			t.Fatalf("boundary entry missing id")
		}
		if _, exists := boundaryIDs[id]; exists {
			t.Fatalf("duplicate boundary id %q", id)
		}
		boundaryIDs[id] = struct{}{}

		if strings.TrimSpace(boundary.Name) == "" {
			t.Fatalf("boundary %q missing name", id)
		}
		if strings.TrimSpace(boundary.Description) == "" {
			t.Fatalf("boundary %q missing description", id)
		}
		if len(boundary.PackagePatterns) == 0 {
			t.Fatalf("boundary %q missing package_patterns", id)
		}

		for _, pattern := range boundary.PackagePatterns {
			trimmed := strings.TrimSpace(pattern)
			if trimmed == "" {
				t.Fatalf("boundary %q has empty package pattern", id)
			}
			if owner, exists := patternOwner[trimmed]; exists {
				t.Fatalf("package pattern %q is assigned to multiple boundaries (%s, %s)", trimmed, owner, id)
			}
			patternOwner[trimmed] = id

			base := patternBasePath(trimmed)
			if base == "" {
				t.Fatalf("boundary %q has invalid package pattern %q", id, trimmed)
			}
			if strings.Contains(base, "..") {
				t.Fatalf("boundary %q pattern %q escapes repository root", id, trimmed)
			}
			abs := filepath.Join(repoRoot, filepath.FromSlash(base))
			if _, err := os.Stat(abs); err != nil {
				t.Fatalf("boundary %q pattern %q resolves to missing path %q: %v", id, trimmed, base, err)
			}
		}
	}

	if len(contract.RequiredBoundaries) == 0 {
		t.Fatalf("boundary contract missing required_boundaries")
	}
	for _, required := range contract.RequiredBoundaries {
		id := strings.TrimSpace(required)
		if id == "" {
			t.Fatalf("required_boundaries contains empty value")
		}
		if _, ok := boundaryIDs[id]; !ok {
			t.Fatalf("required boundary %q is not defined in boundaries list", id)
		}
	}
}

func TestBoundaryContract_DependencyPolicyConsistency(t *testing.T) {
	repoRoot := mustRepoRoot(t)
	contract := mustReadBoundaryContract(t, filepath.Join(repoRoot, boundaryContractFilePath))

	if contract.DependencyPolicy.DefaultEffect != "deny" {
		t.Fatalf("dependency_policy.default_effect must be %q, got %q", "deny", contract.DependencyPolicy.DefaultEffect)
	}
	if len(contract.DependencyPolicy.Allow) == 0 {
		t.Fatalf("dependency_policy.allow must include at least one rule")
	}
	if len(contract.DependencyPolicy.Deny) == 0 {
		t.Fatalf("dependency_policy.deny must include at least one rule")
	}

	boundaryIDs := map[string]struct{}{}
	for _, boundary := range contract.Boundaries {
		boundaryIDs[boundary.ID] = struct{}{}
	}

	seenRules := map[string]string{}
	validateRuleSet := func(effect string, rules []boundaryDependency) {
		t.Helper()
		for _, rule := range rules {
			from := strings.TrimSpace(rule.From)
			to := strings.TrimSpace(rule.To)
			if from == "" || to == "" {
				t.Fatalf("%s rule must define non-empty from/to", effect)
			}
			if _, ok := boundaryIDs[from]; !ok {
				t.Fatalf("%s rule references unknown from boundary %q", effect, from)
			}
			if _, ok := boundaryIDs[to]; !ok {
				t.Fatalf("%s rule references unknown to boundary %q", effect, to)
			}
			if strings.TrimSpace(rule.Rationale) == "" {
				t.Fatalf("%s rule %s -> %s missing rationale", effect, from, to)
			}
			key := from + "->" + to
			if prev, exists := seenRules[key]; exists {
				t.Fatalf("dependency rule %s declared more than once (%s and %s)", key, prev, effect)
			}
			seenRules[key] = effect
		}
	}

	validateRuleSet("allow", contract.DependencyPolicy.Allow)
	validateRuleSet("deny", contract.DependencyPolicy.Deny)
}

func TestBoundaryContract_OwnershipAssignments(t *testing.T) {
	repoRoot := mustRepoRoot(t)
	contract := mustReadBoundaryContract(t, filepath.Join(repoRoot, boundaryContractFilePath))

	ownerIDs := map[string]struct{}{}
	for _, owner := range contract.Ownership.Owners {
		id := strings.TrimSpace(owner.ID)
		if id == "" {
			t.Fatalf("ownership owner entry missing id")
		}
		if _, exists := ownerIDs[id]; exists {
			t.Fatalf("duplicate owner id %q", id)
		}
		ownerIDs[id] = struct{}{}

		if strings.TrimSpace(owner.DisplayName) == "" {
			t.Fatalf("owner %q missing display_name", id)
		}
		if strings.TrimSpace(owner.Scope) == "" {
			t.Fatalf("owner %q missing scope", id)
		}
	}
	if len(ownerIDs) == 0 {
		t.Fatalf("ownership.owners must include at least one owner")
	}

	boundaryPatterns := map[string]map[string]struct{}{}
	for _, boundary := range contract.Boundaries {
		set := map[string]struct{}{}
		for _, pattern := range boundary.PackagePatterns {
			set[pattern] = struct{}{}
		}
		boundaryPatterns[boundary.ID] = set
	}

	assigned := map[string]struct{}{}
	for _, assignment := range contract.Ownership.Assignments {
		boundaryID := strings.TrimSpace(assignment.BoundaryID)
		ownerID := strings.TrimSpace(assignment.OwnerID)
		if boundaryID == "" || ownerID == "" {
			t.Fatalf("ownership assignment requires boundary_id and owner_id")
		}

		if _, ok := boundaryPatterns[boundaryID]; !ok {
			t.Fatalf("ownership assignment references unknown boundary %q", boundaryID)
		}
		if _, ok := ownerIDs[ownerID]; !ok {
			t.Fatalf("ownership assignment references unknown owner %q", ownerID)
		}
		if _, exists := assigned[boundaryID]; exists {
			t.Fatalf("boundary %q has multiple ownership assignments", boundaryID)
		}
		assigned[boundaryID] = struct{}{}

		if len(assignment.KeyModules) == 0 {
			t.Fatalf("ownership assignment for boundary %q requires at least one key_modules entry", boundaryID)
		}
		for _, module := range assignment.KeyModules {
			mod := strings.TrimSpace(module)
			if mod == "" {
				t.Fatalf("ownership assignment for boundary %q has empty key module", boundaryID)
			}
			if _, ok := boundaryPatterns[boundaryID][mod]; !ok {
				all := sortedPatterns(boundaryPatterns[boundaryID])
				t.Fatalf("ownership key module %q for boundary %q is not declared in boundary package_patterns (declared: %s)", mod, boundaryID, strings.Join(all, ", "))
			}
		}
	}

	if len(assigned) != len(contract.Boundaries) {
		missing := make([]string, 0)
		for _, boundary := range contract.Boundaries {
			if _, ok := assigned[boundary.ID]; !ok {
				missing = append(missing, boundary.ID)
			}
		}
		sort.Strings(missing)
		t.Fatalf("ownership assignments missing for boundaries: %s", strings.Join(missing, ", "))
	}

	if strings.TrimSpace(contract.EnforcementModel.Stage361) == "" {
		t.Fatalf("enforcement_model.stage_3_6_1 must be documented")
	}
	if strings.TrimSpace(contract.EnforcementModel.Stage362) == "" {
		t.Fatalf("enforcement_model.stage_3_6_2 must be documented")
	}
	if strings.TrimSpace(contract.EnforcementModel.Stage363) == "" {
		t.Fatalf("enforcement_model.stage_3_6_3 must be documented")
	}
	if strings.TrimSpace(contract.EnforcementModel.Stage364) == "" {
		t.Fatalf("enforcement_model.stage_3_6_4 must be documented")
	}
}

func TestBoundaryContract_ExceptionRegistry(t *testing.T) {
	repoRoot := mustRepoRoot(t)
	contract := mustReadBoundaryContract(t, filepath.Join(repoRoot, boundaryContractFilePath))
	registryPath := filepath.Join(repoRoot, filepath.FromSlash(contract.ExceptionRegistry))
	registry := mustReadBoundaryExceptionRegistry(t, registryPath)

	if strings.TrimSpace(registry.Version) == "" {
		t.Fatalf("boundary exception registry missing version")
	}
	if strings.TrimSpace(registry.ContractID) == "" {
		t.Fatalf("boundary exception registry missing contract_id")
	}
	if strings.TrimSpace(registry.Summary) == "" {
		t.Fatalf("boundary exception registry missing summary")
	}

	boundaryIDs := map[string]struct{}{}
	for _, boundary := range contract.Boundaries {
		boundaryIDs[strings.TrimSpace(boundary.ID)] = struct{}{}
	}

	allowEdges := map[string]struct{}{}
	transitionalAllowEdges := map[string]struct{}{}
	for _, rule := range contract.DependencyPolicy.Allow {
		edge := strings.TrimSpace(rule.From) + "->" + strings.TrimSpace(rule.To)
		allowEdges[edge] = struct{}{}
		if strings.Contains(strings.ToLower(rule.Rationale), "transitional") {
			transitionalAllowEdges[edge] = struct{}{}
		}
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	seenIDs := map[string]struct{}{}
	coveredTransitionalEdges := map[string]struct{}{}
	seenExceptionEdges := map[string]string{}

	for i, exception := range registry.Exceptions {
		entryRef := "exceptions[" + strconv.Itoa(i) + "]"

		id := strings.TrimSpace(exception.ID)
		if id == "" {
			t.Fatalf("%s missing id", entryRef)
		}
		if _, exists := seenIDs[id]; exists {
			t.Fatalf("duplicate exception id %q", id)
		}
		seenIDs[id] = struct{}{}

		from := strings.TrimSpace(exception.FromBoundary)
		to := strings.TrimSpace(exception.ToBoundary)
		if from == "" || to == "" {
			t.Fatalf("%s (%s) missing from_boundary/to_boundary", entryRef, id)
		}
		if _, ok := boundaryIDs[from]; !ok {
			t.Fatalf("%s (%s) references unknown from_boundary %q", entryRef, id, from)
		}
		if _, ok := boundaryIDs[to]; !ok {
			t.Fatalf("%s (%s) references unknown to_boundary %q", entryRef, id, to)
		}

		edge := from + "->" + to
		if _, ok := allowEdges[edge]; !ok {
			t.Fatalf("%s (%s) edge %s is not declared in dependency_policy.allow; add/justify allow rule first", entryRef, id, edge)
		}
		if existing, exists := seenExceptionEdges[edge]; exists {
			t.Fatalf("edge %s has multiple active exceptions (%s, %s)", edge, existing, id)
		}
		seenExceptionEdges[edge] = id

		if strings.TrimSpace(exception.Scope) == "" {
			t.Fatalf("%s (%s) missing scope", entryRef, id)
		}
		if strings.TrimSpace(exception.Rationale) == "" {
			t.Fatalf("%s (%s) missing rationale", entryRef, id)
		}
		if strings.TrimSpace(exception.ReviewerSignoff) == "" {
			t.Fatalf("%s (%s) missing reviewer_signoff", entryRef, id)
		}
		if strings.TrimSpace(exception.TrackingIssue) == "" {
			t.Fatalf("%s (%s) missing tracking_issue", entryRef, id)
		}
		if strings.TrimSpace(exception.RemovalExpectations) == "" {
			t.Fatalf("%s (%s) missing removal_expectations", entryRef, id)
		}

		approvedOn := mustParseISODate(t, exception.ApprovedOn, entryRef, "approved_on")
		expiresOn := mustParseISODate(t, exception.ExpiresOn, entryRef, "expires_on")
		if !expiresOn.After(approvedOn) {
			t.Fatalf("%s (%s) expires_on (%s) must be after approved_on (%s)", entryRef, id, exception.ExpiresOn, exception.ApprovedOn)
		}
		if expiresOn.Before(today) {
			t.Fatalf("%s (%s) is expired as of %s; remove the exception or extend with fresh reviewer sign-off", entryRef, id, exception.ExpiresOn)
		}

		if _, isTransitional := transitionalAllowEdges[edge]; isTransitional {
			coveredTransitionalEdges[edge] = struct{}{}
		}
	}

	if len(transitionalAllowEdges) == 0 {
		t.Fatalf("dependency_policy.allow must contain at least one transitional rule to validate exception process")
	}

	missing := make([]string, 0)
	for edge := range transitionalAllowEdges {
		if _, ok := coveredTransitionalEdges[edge]; !ok {
			missing = append(missing, edge)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("missing exception registry entries for transitional allow edges: %s", strings.Join(missing, ", "))
	}
}

func mustReadBoundaryContract(t *testing.T, path string) boundaryContractFile {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read boundary contract %s: %v", path, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var contract boundaryContractFile
	if err := dec.Decode(&contract); err != nil {
		t.Fatalf("decode boundary contract %s: %v", path, err)
	}

	if err := dec.Decode(&struct{}{}); err == nil {
		t.Fatalf("boundary contract %s contains multiple YAML documents", path)
	} else if err != io.EOF {
		t.Fatalf("boundary contract %s trailing YAML decode error: %v", path, err)
	}

	return contract
}

func mustReadBoundaryExceptionRegistry(t *testing.T, path string) boundaryExceptionRegistryFile {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read boundary exception registry %s: %v", path, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var registry boundaryExceptionRegistryFile
	if err := dec.Decode(&registry); err != nil {
		t.Fatalf("decode boundary exception registry %s: %v", path, err)
	}

	if err := dec.Decode(&struct{}{}); err == nil {
		t.Fatalf("boundary exception registry %s contains multiple YAML documents", path)
	} else if err != io.EOF {
		t.Fatalf("boundary exception registry %s trailing YAML decode error: %v", path, err)
	}

	return registry
}

func mustParseISODate(t *testing.T, value, entryRef, field string) time.Time {
	t.Helper()
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		t.Fatalf("%s missing %s", entryRef, field)
	}
	parsed, err := time.Parse("2006-01-02", trimmed)
	if err != nil {
		t.Fatalf("%s has invalid %s %q (expected YYYY-MM-DD): %v", entryRef, field, value, err)
	}
	return parsed.UTC().Truncate(24 * time.Hour)
}

func patternBasePath(pattern string) string {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return ""
	}

	wildcardAt := len(trimmed)
	for _, token := range []string{"...", "*", "?", "["} {
		if idx := strings.Index(trimmed, token); idx >= 0 && idx < wildcardAt {
			wildcardAt = idx
		}
	}

	base := strings.Trim(trimmed[:wildcardAt], "/")
	if base == "" {
		return ""
	}
	return base
}

func sortedPatterns(in map[string]struct{}) []string {
	out := make([]string, 0, len(in))
	for pattern := range in {
		out = append(out, pattern)
	}
	sort.Strings(out)
	return out
}

