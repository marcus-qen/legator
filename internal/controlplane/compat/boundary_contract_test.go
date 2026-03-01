package compat

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const boundaryContractFilePath = "docs/contracts/architecture-boundaries.yaml"

type boundaryContractFile struct {
	Version            string                      `yaml:"version"`
	ContractID         string                      `yaml:"contract_id"`
	Stage              string                      `yaml:"stage"`
	Summary            string                      `yaml:"summary"`
	ModuleRoot         string                      `yaml:"module_root"`
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
	DefaultEffect string                 `yaml:"default_effect"`
	Allow         []boundaryDependency   `yaml:"allow"`
	Deny          []boundaryDependency   `yaml:"deny"`
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
	Stage361    string   `yaml:"stage_3_6_1"`
	Stage362    string   `yaml:"stage_3_6_2"`
	RolloutNote []string `yaml:"rollout_notes"`
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

