/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
)

// AgentRunReconciler reconciles an AgentRun object.
type AgentRunReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core.infraagent.io,resources=agentruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.infraagent.io,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.infraagent.io,resources=agentruns/finalizers,verbs=update

// Reconcile handles AgentRun create/update/delete events.
// Phase 0: logs reconciliation only. AgentRun is primarily a status record
// created by the agent runner (Phase 2). The reconciler here is for
// observing state transitions and enforcing immutability of terminal runs.
func (r *AgentRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	run := &corev1alpha1.AgentRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	log.Info("Reconciling AgentRun",
		"name", run.Name,
		"namespace", run.Namespace,
		"agent", run.Spec.AgentRef,
		"trigger", run.Spec.Trigger,
		"phase", run.Status.Phase,
	)

	// In Phase 2+, this reconciler will:
	// - Enforce immutability of terminal runs (Succeeded/Failed/Escalated/Blocked)
	// - Update parent InfraAgent status (lastRunTime, runCount, consecutiveFailures)
	// - Handle retention/cleanup of old AgentRuns

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.AgentRun{}).
		Named("agentrun").
		Complete(r)
}
