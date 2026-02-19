/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package runner

import (
	"testing"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
)

func TestExtractFindings(t *testing.T) {
	content := `Everything looks healthy.

CRITICAL: Node talos-wk-01 has memory pressure
WARNING: Pod backstage-backend-abc is restarting frequently
INFO: All endpoints responding normally
🔴 Disk usage above 85% on node-2
⚠️ Certificate expires in 7 days
Normal line that shouldn't match.`

	findings := extractFindings(content)

	critCount := 0
	warnCount := 0
	infoCount := 0
	for _, f := range findings {
		switch f.Severity {
		case corev1alpha1.FindingSeverityCritical:
			critCount++
		case corev1alpha1.FindingSeverityWarning:
			warnCount++
		case corev1alpha1.FindingSeverityInfo:
			infoCount++
		}
	}

	if critCount != 2 {
		t.Errorf("expected 2 critical findings, got %d", critCount)
	}
	if warnCount != 2 {
		t.Errorf("expected 2 warning findings, got %d", warnCount)
	}
	if infoCount != 1 {
		t.Errorf("expected 1 info finding, got %d", infoCount)
	}
}

func TestExtractFindings_Empty(t *testing.T) {
	findings := extractFindings("All good, nothing to report.")
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}
