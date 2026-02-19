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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
)

// AgentEnvironmentReconciler reconciles an AgentEnvironment object.
type AgentEnvironmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core.infraagent.io,resources=agentenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.infraagent.io,resources=agentenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.infraagent.io,resources=agentenvironments/finalizers,verbs=update

// Reconcile handles AgentEnvironment create/update/delete events.
// Phase 0: logs reconciliation and sets basic status.
func (r *AgentEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	env := &corev1alpha1.AgentEnvironment{}
	if err := r.Get(ctx, req.NamespacedName, env); err != nil {
		if errors.IsNotFound(err) {
			log.Info("AgentEnvironment deleted", "name", req.Name, "namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	endpointCount := len(env.Spec.Endpoints)
	mcpServerCount := len(env.Spec.MCPServers)

	log.Info("Reconciling AgentEnvironment",
		"name", env.Name,
		"namespace", env.Namespace,
		"endpoints", endpointCount,
		"mcpServers", mcpServerCount,
	)

	// Set status to Ready (basic validation — real validation comes in Phase 1)
	env.Status.Phase = corev1alpha1.AgentEnvironmentPhaseReady
	meta.SetStatusCondition(&env.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            "AgentEnvironment reconciled successfully",
		ObservedGeneration: env.Generation,
	})

	if err := r.Status().Update(ctx, env); err != nil {
		log.Error(err, "Failed to update AgentEnvironment status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.AgentEnvironment{}).
		Named("agentenvironment").
		Complete(r)
}
