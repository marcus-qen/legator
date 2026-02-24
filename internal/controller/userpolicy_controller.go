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
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

// UserPolicyReconciler reconciles a UserPolicy object.
type UserPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=legator.io,resources=userpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=legator.io,resources=userpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=legator.io,resources=userpolicies/finalizers,verbs=update

// Reconcile validates UserPolicy specs and projects readiness state into status.
func (r *UserPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	policy := &corev1alpha1.UserPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		if errors.IsNotFound(err) {
			log.Info("UserPolicy deleted", "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	validationErrs := validateUserPolicySpec(policy.Spec)
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.EffectiveRole = policy.Spec.Role
	policy.Status.MatchedSubjects = int32(len(policy.Spec.Subjects))
	policy.Status.ValidationErrors = validationErrs
	policy.Status.Ready = len(validationErrs) == 0

	condition := metav1.Condition{
		Type:               "Ready",
		ObservedGeneration: policy.Generation,
	}
	if policy.Status.Ready {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "Validated"
		condition.Message = fmt.Sprintf("Policy validated for %d subject matcher(s)", len(policy.Spec.Subjects))
	} else {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "InvalidSpec"
		condition.Message = strings.Join(validationErrs, "; ")
	}
	meta.SetStatusCondition(&policy.Status.Conditions, condition)

	if err := r.Status().Update(ctx, policy); err != nil {
		log.Error(err, "Failed to update UserPolicy status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func validateUserPolicySpec(spec corev1alpha1.UserPolicySpec) []string {
	var errs []string

	if len(spec.Subjects) == 0 {
		errs = append(errs, "at least one subject matcher is required")
	}

	for i, subject := range spec.Subjects {
		if strings.TrimSpace(subject.Claim) == "" {
			errs = append(errs, fmt.Sprintf("subjects[%d].claim must not be empty", i))
		}
		if strings.TrimSpace(subject.Value) == "" {
			errs = append(errs, fmt.Sprintf("subjects[%d].value must not be empty", i))
		}
	}

	switch spec.Role {
	case corev1alpha1.UserPolicyRoleViewer:
		if spec.Scope.MaxAutonomy != "" && spec.Scope.MaxAutonomy != corev1alpha1.UserPolicyAutonomyObserve {
			errs = append(errs, "viewer role only supports maxAutonomy=observe")
		}
	case corev1alpha1.UserPolicyRoleOperator:
		if spec.Scope.MaxAutonomy == corev1alpha1.UserPolicyAutonomyAutomateDestructive {
			errs = append(errs, "operator role cannot grant maxAutonomy=automate-destructive")
		}
	}

	return errs
}

// SetupWithManager sets up the controller with the Manager.
func (r *UserPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.UserPolicy{}).
		Named("userpolicy").
		Complete(r)
}
