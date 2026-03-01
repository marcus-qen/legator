package compat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type goListPackage struct {
	ImportPath string   `json:"ImportPath"`
	Dir        string   `json:"Dir"`
	Imports    []string `json:"Imports"`
}

type repoPackage struct {
	ImportPath string
	RelPath    string
	Imports    []string
	BoundaryID string
}

type boundaryRuleRef struct {
	Reference string
	Rationale string
}

type architectureViolation struct {
	Kind         string
	FromPackage  string
	FromBoundary string
	ToPackage    string
	ToBoundary   string
	Edge         string
	RuleRef      string
	Detail       string
}

func TestBoundaryContract_ImportGraphEnforcement(t *testing.T) {
	repoRoot := mustRepoRoot(t)
	contract := mustReadBoundaryContract(t, filepath.Join(repoRoot, boundaryContractFilePath))
	modulePath := mustModulePath(t, repoRoot)
	packages := mustListRepoPackages(t, repoRoot, modulePath)

	allowRules := indexBoundaryRules(contract.DependencyPolicy.Allow, "dependency_policy.allow")
	denyRules := indexBoundaryRules(contract.DependencyPolicy.Deny, "dependency_policy.deny")

	violations := make([]architectureViolation, 0)

	packageIDs := sortedRepoPackageIDs(packages)
	for _, importPath := range packageIDs {
		pkg := packages[importPath]
		boundaryID, matches, ambiguous := classifyPackageBoundary(pkg.RelPath, contract)
		if ambiguous {
			violations = append(violations, architectureViolation{
				Kind:         "classify",
				FromPackage:  pkg.ImportPath,
				FromBoundary: "<ambiguous>",
				RuleRef:      "boundaries[*].package_patterns",
				Detail:       fmt.Sprintf("matched multiple boundaries: %s", strings.Join(matches, ", ")),
			})
			continue
		}
		pkg.BoundaryID = boundaryID
		packages[importPath] = pkg
	}

	for _, fromImport := range packageIDs {
		fromPkg := packages[fromImport]
		if fromPkg.BoundaryID == "" {
			continue
		}

		imports := append([]string(nil), fromPkg.Imports...)
		sort.Strings(imports)

		for _, toImport := range imports {
			toPkg, ok := packages[toImport]
			if !ok || toPkg.BoundaryID == "" {
				continue
			}

			if fromPkg.BoundaryID == toPkg.BoundaryID {
				continue
			}

			edge := fromPkg.BoundaryID + "->" + toPkg.BoundaryID
			if deny, denied := denyRules[edge]; denied {
				violations = append(violations, architectureViolation{
					Kind:         "deny",
					FromPackage:  fromPkg.ImportPath,
					FromBoundary: fromPkg.BoundaryID,
					ToPackage:    toPkg.ImportPath,
					ToBoundary:   toPkg.BoundaryID,
					Edge:         edge,
					RuleRef:      deny.Reference,
					Detail:       deny.Rationale,
				})
				continue
			}

			if _, allowed := allowRules[edge]; !allowed {
				violations = append(violations, architectureViolation{
					Kind:         "undeclared",
					FromPackage:  fromPkg.ImportPath,
					FromBoundary: fromPkg.BoundaryID,
					ToPackage:    toPkg.ImportPath,
					ToBoundary:   toPkg.BoundaryID,
					Edge:         edge,
					RuleRef:      "dependency_policy.default_effect",
					Detail:       "cross-boundary edge is not declared in dependency_policy.allow (default deny applies)",
				})
			}
		}
	}

	if len(violations) == 0 {
		return
	}

	sort.Slice(violations, func(i, j int) bool {
		left := violations[i]
		right := violations[j]

		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.FromPackage != right.FromPackage {
			return left.FromPackage < right.FromPackage
		}
		if left.ToPackage != right.ToPackage {
			return left.ToPackage < right.ToPackage
		}
		if left.Edge != right.Edge {
			return left.Edge < right.Edge
		}
		if left.RuleRef != right.RuleRef {
			return left.RuleRef < right.RuleRef
		}
		return left.Detail < right.Detail
	})

	var msg strings.Builder
	fmt.Fprintf(&msg, "architecture boundary guardrail violations (%d):\n", len(violations))
	for _, violation := range violations {
		switch violation.Kind {
		case "classify":
			fmt.Fprintf(
				&msg,
				"- [CLASSIFY] package=%s rule=%s detail=%s\n",
				violation.FromPackage,
				violation.RuleRef,
				violation.Detail,
			)
		case "deny":
			fmt.Fprintf(
				&msg,
				"- [DENY] package=%s (%s) imports=%s (%s) edge=%s rule=%s detail=%s\n",
				violation.FromPackage,
				violation.FromBoundary,
				violation.ToPackage,
				violation.ToBoundary,
				violation.Edge,
				violation.RuleRef,
				violation.Detail,
			)
		default:
			fmt.Fprintf(
				&msg,
				"- [UNDECLARED] package=%s (%s) imports=%s (%s) edge=%s rule=%s detail=%s\n",
				violation.FromPackage,
				violation.FromBoundary,
				violation.ToPackage,
				violation.ToBoundary,
				violation.Edge,
				violation.RuleRef,
				violation.Detail,
			)
		}
	}
	fmt.Fprintf(&msg, "\nSource of truth: %s\n", boundaryContractFilePath)
	fmt.Fprintf(&msg, "Fix by updating package imports or dependency_policy rules in the boundary contract (with rationale).\n")

	t.Fatal(msg.String())
}

func mustModulePath(t *testing.T, repoRoot string) string {
	t.Helper()

	cmd := exec.Command("go", "list", "-m", "-f", "{{.Path}}")
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("determine module path: %v\n%s", err, strings.TrimSpace(string(output)))
	}

	modulePath := strings.TrimSpace(string(output))
	if modulePath == "" {
		t.Fatalf("module path is empty")
	}
	return modulePath
}

func mustListRepoPackages(t *testing.T, repoRoot, modulePath string) map[string]repoPackage {
	t.Helper()

	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list go packages: %v\n%s", err, strings.TrimSpace(string(output)))
	}

	dec := json.NewDecoder(bytes.NewReader(output))
	packages := make(map[string]repoPackage)
	for {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode go list output: %v", err)
		}

		if !strings.HasPrefix(pkg.ImportPath, modulePath) || strings.TrimSpace(pkg.Dir) == "" {
			continue
		}

		relPath, err := filepath.Rel(repoRoot, pkg.Dir)
		if err != nil {
			t.Fatalf("compute repo-relative path for package %s: %v", pkg.ImportPath, err)
		}
		relPath = filepath.ToSlash(filepath.Clean(relPath))
		if relPath == "." {
			continue
		}
		if relPath == ".." || strings.HasPrefix(relPath, "../") {
			t.Fatalf("package %s resolved outside repository root: %s", pkg.ImportPath, relPath)
		}

		packages[pkg.ImportPath] = repoPackage{
			ImportPath: pkg.ImportPath,
			RelPath:    relPath,
			Imports:    dedupeAndSort(pkg.Imports),
		}
	}

	if len(packages) == 0 {
		t.Fatalf("go list returned no repository packages")
	}
	return packages
}

func classifyPackageBoundary(relPath string, contract boundaryContractFile) (boundaryID string, matches []string, ambiguous bool) {
	matched := make(map[string][]string)

	for _, boundary := range contract.Boundaries {
		for _, pattern := range boundary.PackagePatterns {
			if boundaryPatternMatches(relPath, pattern) {
				matched[boundary.ID] = append(matched[boundary.ID], strings.TrimSpace(pattern))
			}
		}
	}

	if len(matched) == 0 {
		return "", nil, false
	}

	boundaryIDs := make([]string, 0, len(matched))
	for id := range matched {
		boundaryIDs = append(boundaryIDs, id)
	}
	sort.Strings(boundaryIDs)

	parts := make([]string, 0, len(boundaryIDs))
	for _, id := range boundaryIDs {
		patterns := dedupeAndSort(matched[id])
		parts = append(parts, fmt.Sprintf("%s via [%s]", id, strings.Join(patterns, ", ")))
	}

	if len(boundaryIDs) > 1 {
		return "", parts, true
	}
	return boundaryIDs[0], parts, false
}

func boundaryPatternMatches(relPath, pattern string) bool {
	rel := strings.Trim(strings.TrimSpace(filepath.ToSlash(relPath)), "/")
	pat := strings.Trim(strings.TrimSpace(pattern), "/")
	if rel == "" || pat == "" {
		return false
	}

	if strings.HasSuffix(pat, "/...") {
		prefix := strings.TrimSuffix(pat, "/...")
		return rel == prefix || strings.HasPrefix(rel, prefix+"/")
	}

	if strings.Contains(pat, "...") {
		prefix := strings.TrimSuffix(pat, "...")
		prefix = strings.TrimSuffix(prefix, "/")
		if prefix == "" {
			return true
		}
		return rel == prefix || strings.HasPrefix(rel, prefix+"/")
	}

	if strings.ContainsAny(pat, "*?[") {
		ok, err := path.Match(pat, rel)
		return err == nil && ok
	}

	return rel == pat
}

func indexBoundaryRules(rules []boundaryDependency, field string) map[string]boundaryRuleRef {
	indexed := make(map[string]boundaryRuleRef, len(rules))
	for i, rule := range rules {
		edge := strings.TrimSpace(rule.From) + "->" + strings.TrimSpace(rule.To)
		indexed[edge] = boundaryRuleRef{
			Reference: fmt.Sprintf("%s[%d]", field, i),
			Rationale: strings.TrimSpace(rule.Rationale),
		}
	}
	return indexed
}

func sortedRepoPackageIDs(packages map[string]repoPackage) []string {
	ids := make([]string, 0, len(packages))
	for id := range packages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func dedupeAndSort(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		set[trimmed] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
