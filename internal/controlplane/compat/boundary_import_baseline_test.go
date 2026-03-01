package compat

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	boundaryImportBaselineFilePath = "docs/contracts/architecture-cross-boundary-imports.txt"
	updateImportBaselineEnv        = "LEGATOR_UPDATE_ARCH_IMPORT_BASELINE"
)

func TestBoundaryContract_ImportGraphBaselineLock(t *testing.T) {
	repoRoot := mustRepoRoot(t)
	contract := mustReadBoundaryContract(t, filepath.Join(repoRoot, boundaryContractFilePath))
	modulePath := mustModulePath(t, repoRoot)
	packages := mustListRepoPackages(t, repoRoot, modulePath)

	current, classifyViolations := collectCrossBoundaryImports(t, packages, contract)
	if len(classifyViolations) > 0 {
		sortArchitectureViolations(classifyViolations)

		var msg strings.Builder
		fmt.Fprintf(&msg, "architecture baseline lock blocked due to boundary classification issues (%d):\n", len(classifyViolations))
		for _, violation := range classifyViolations {
			fmt.Fprintf(
				&msg,
				"- package=%s rule=%s detail=%s\n",
				violation.FromPackage,
				violation.RuleRef,
				violation.Detail,
			)
		}
		fmt.Fprintf(&msg, "\nSource of truth: %s\n", boundaryContractFilePath)
		t.Fatal(msg.String())
	}

	baselinePath := filepath.Join(repoRoot, boundaryImportBaselineFilePath)
	if os.Getenv(updateImportBaselineEnv) == "1" {
		mustWriteImportBaseline(t, baselinePath, current)
		return
	}

	baseline := mustReadStableList(t, baselinePath)
	unexpected, stale := diffStableLists(current, baseline)
	if len(unexpected) == 0 && len(stale) == 0 {
		return
	}

	var msg strings.Builder
	fmt.Fprintf(&msg, "architecture import baseline drift detected (%s):\n", boundaryImportBaselineFilePath)
	if len(unexpected) > 0 {
		fmt.Fprintf(&msg, "- new cross-boundary imports (%d):\n%s\n", len(unexpected), strings.Join(unexpected, "\n"))
	}
	if len(stale) > 0 {
		fmt.Fprintf(&msg, "- stale baseline entries no longer observed (%d):\n%s\n", len(stale), strings.Join(stale, "\n"))
	}
	fmt.Fprintf(&msg, "\nIf intentional, run:\n")
	fmt.Fprintf(&msg, "%s=1 go test ./internal/controlplane/compat -run TestBoundaryContract_ImportGraphBaselineLock -count=1\n", updateImportBaselineEnv)
	fmt.Fprintf(&msg, "Then commit the baseline update with changelog/release note rationale.\n")
	fmt.Fprintf(&msg, "\nBoundary contract source: %s\n", boundaryContractFilePath)
	t.Fatal(msg.String())
}

func collectCrossBoundaryImports(t *testing.T, packages map[string]repoPackage, contract boundaryContractFile) ([]string, []architectureViolation) {
	t.Helper()

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

	crossBoundary := make([]string, 0)
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

			crossBoundary = append(crossBoundary, fmt.Sprintf(
				"%s (%s) -> %s (%s)",
				fromPkg.ImportPath,
				fromPkg.BoundaryID,
				toPkg.ImportPath,
				toPkg.BoundaryID,
			))
		}
	}

	return dedupeAndSort(crossBoundary), violations
}

func diffStableLists(current, stable []string) (unexpected []string, stale []string) {
	currentSet := map[string]struct{}{}
	for _, item := range current {
		currentSet[item] = struct{}{}
	}

	stableSet := map[string]struct{}{}
	for _, item := range stable {
		stableSet[item] = struct{}{}
	}

	for item := range currentSet {
		if _, ok := stableSet[item]; !ok {
			unexpected = append(unexpected, item)
		}
	}
	for item := range stableSet {
		if _, ok := currentSet[item]; !ok {
			stale = append(stale, item)
		}
	}

	sort.Strings(unexpected)
	sort.Strings(stale)
	return unexpected, stale
}

func mustWriteImportBaseline(t *testing.T, path string, values []string) {
	t.Helper()

	sorted := dedupeAndSort(values)
	var out strings.Builder
	out.WriteString("# Stage 3.6.3 architecture cross-boundary import baseline\n")
	out.WriteString("# Regenerate intentionally via:\n")
	out.WriteString("#   LEGATOR_UPDATE_ARCH_IMPORT_BASELINE=1 go test ./internal/controlplane/compat -run TestBoundaryContract_ImportGraphBaselineLock -count=1\n\n")
	for _, value := range sorted {
		out.WriteString(value)
		out.WriteByte('\n')
	}

	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		t.Fatalf("write architecture import baseline %s: %v", path, err)
	}
}

func sortArchitectureViolations(violations []architectureViolation) {
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
}
