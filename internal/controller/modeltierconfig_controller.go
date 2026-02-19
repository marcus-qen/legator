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
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
)

// ModelTierConfigReconciler reconciles a ModelTierConfig object.
type ModelTierConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core.infraagent.io,resources=modeltierconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.infraagent.io,resources=modeltierconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.infraagent.io,resources=modeltierconfigs/finalizers,verbs=update

// Reconcile handles ModelTierConfig create/update/delete events.
// Phase 0: logs reconciliation, validates tier mappings, and sets status.
func (r *ModelTierConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	config := &corev1alpha1.ModelTierConfig{}
	if err := r.Get(ctx, req.NamespacedName, config); err != nil {
		if errors.IsNotFound(err) {
			log.Info("ModelTierConfig deleted", "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	log.Info("Reconciling ModelTierConfig",
		"name", config.Name,
		"tiers", len(config.Spec.Tiers),
	)

	// Build tier status map
	tierStatus := make(map[string]string)
	for _, t := range config.Spec.Tiers {
		tierStatus[string(t.Tier)] = fmt.Sprintf("%s/%s", t.Provider, t.Model)
	}
	config.Status.TierStatus = tierStatus
	config.Status.Ready = len(config.Spec.Tiers) > 0

	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            fmt.Sprintf("%d tier mappings configured", len(config.Spec.Tiers)),
		ObservedGeneration: config.Generation,
	})

	if err := r.Status().Update(ctx, config); err != nil {
		log.Error(err, "Failed to update ModelTierConfig status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ModelTierConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.ModelTierConfig{}).
		Named("modeltierconfig").
		Complete(r)
}
