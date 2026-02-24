package anomaly

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDetectAnomalies_ScopeSpike(t *testing.T) {
	now := time.Now().UTC()
	history := []runSnapshot{
		{Agent: "watchman", Timestamp: now.Add(-10 * time.Minute), ActionCount: 2},
		{Agent: "watchman", Timestamp: now.Add(-8 * time.Minute), ActionCount: 3},
		{Agent: "watchman", Timestamp: now.Add(-6 * time.Minute), ActionCount: 2},
	}
	current := runSnapshot{Agent: "watchman", Timestamp: now, ActionCount: 10}

	signals := detectAnomalies(current, history, Config{
		Lookback:             1 * time.Hour,
		FrequencyWindow:      30 * time.Minute,
		FrequencyThreshold:   100,
		ScopeSpikeMultiplier: 2.0,
		MinScopeSpikeDelta:   3,
	})

	if !hasSignal(signals, "scope-spike") {
		t.Fatalf("expected scope-spike signal, got %#v", signals)
	}
}

func TestDetectAnomalies_TargetDrift(t *testing.T) {
	now := time.Now().UTC()
	history := []runSnapshot{
		{Agent: "watchman", Timestamp: now.Add(-50 * time.Minute), TargetClasses: []string{"pod", "service"}},
		{Agent: "watchman", Timestamp: now.Add(-40 * time.Minute), TargetClasses: []string{"pod"}},
		{Agent: "watchman", Timestamp: now.Add(-30 * time.Minute), TargetClasses: []string{"deployment"}},
		{Agent: "watchman", Timestamp: now.Add(-20 * time.Minute), TargetClasses: []string{"pod"}},
		{Agent: "watchman", Timestamp: now.Add(-10 * time.Minute), TargetClasses: []string{"service"}},
	}
	current := runSnapshot{Agent: "watchman", Timestamp: now, TargetClasses: []string{"database", "pod"}}

	signals := detectAnomalies(current, history, Config{
		Lookback:              2 * time.Hour,
		FrequencyWindow:       30 * time.Minute,
		FrequencyThreshold:    100,
		ScopeSpikeMultiplier:  100,
		MinScopeSpikeDelta:    100,
		TargetDriftMinSamples: 5,
	})

	if !hasSignal(signals, "target-drift") {
		t.Fatalf("expected target-drift signal, got %#v", signals)
	}
}

func TestScanOnce_PublishesAndDedupesFrequencyAnomaly(t *testing.T) {
	now := time.Now().UTC()
	runs := []runtime.Object{
		newRun("run-1", now.Add(-20*time.Minute), 2),
		newRun("run-2", now.Add(-10*time.Minute), 2),
		newRun("run-3", now.Add(-2*time.Minute), 2),
	}

	k8s := newFakeClient(t, runs...)
	detector := NewDetector(k8s, Config{
		Namespace:             "agents",
		ScanInterval:          1 * time.Minute,
		Lookback:              24 * time.Hour,
		FrequencyWindow:       30 * time.Minute,
		FrequencyThreshold:    2,
		ScopeSpikeMultiplier:  100,
		MinScopeSpikeDelta:    100,
		TargetDriftMinSamples: 100,
	}, logr.Discard())

	if err := detector.ScanOnce(context.Background()); err != nil {
		t.Fatalf("scan once: %v", err)
	}

	firstCount := countAnomalyEvents(t, k8s)
	if firstCount == 0 {
		t.Fatalf("expected anomaly events after first scan")
	}

	if err := detector.ScanOnce(context.Background()); err != nil {
		t.Fatalf("scan once (second): %v", err)
	}
	secondCount := countAnomalyEvents(t, k8s)
	if secondCount != firstCount {
		t.Fatalf("expected dedupe to keep anomaly count stable (%d), got %d", firstCount, secondCount)
	}
}

func hasSignal(signals []anomalySignal, typ string) bool {
	for _, signal := range signals {
		if signal.Type == typ {
			return true
		}
	}
	return false
}

func newRun(name string, created time.Time, actions int) *corev1alpha1.LegatorRun {
	actionRecords := make([]corev1alpha1.ActionRecord, 0, actions)
	for i := range actions {
		actionRecords = append(actionRecords, corev1alpha1.ActionRecord{
			Seq:       int32(i + 1),
			Timestamp: metav1.NewTime(created.Add(time.Duration(i) * time.Second)),
			Tool:      "kubectl.get",
			Target:    "pod/default",
			Tier:      corev1alpha1.ActionTierRead,
			Status:    corev1alpha1.ActionStatusExecuted,
		})
	}

	return &corev1alpha1.LegatorRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "agents",
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: corev1alpha1.LegatorRunSpec{
			AgentRef:       "watchman",
			EnvironmentRef: "dev-lab",
			Trigger:        corev1alpha1.RunTriggerManual,
		},
		Status: corev1alpha1.LegatorRunStatus{
			Phase:   corev1alpha1.RunPhaseSucceeded,
			Actions: actionRecords,
		},
	}
}

func newFakeClient(t *testing.T, objs ...runtime.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
}

func countAnomalyEvents(t *testing.T, c client.Client) int {
	t.Helper()
	list := &corev1alpha1.AgentEventList{}
	if err := c.List(context.Background(), list, client.InNamespace("agents")); err != nil {
		t.Fatalf("list events: %v", err)
	}
	count := 0
	for _, event := range list.Items {
		if event.Spec.EventType == "anomaly" {
			count++
		}
	}
	return count
}
