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
	_ "github.com/marcus-qen/infraagent/internal/assembler" // used transitively by runner
	"github.com/marcus-qen/infraagent/internal/provider"
	"github.com/marcus-qen/infraagent/internal/runner"
	"github.com/marcus-qen/infraagent/internal/tools"
)

const (
	// AnnotationRunNow triggers a manual agent run when set to "true".
	AnnotationRunNow = "infraagent.io/run-now"
)

// InfraAgentReconciler reconciles an InfraAgent object.
type InfraAgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Runner executes agent runs. Nil in Phase 0 stubs.
	Runner *runner.Runner

	// ProviderFactory creates LLM providers for agent runs.
	// Nil means manual triggers are disabled (Phase 0 mode).
	ProviderFactory func(agent *corev1alpha1.InfraAgent, model *corev1alpha1.ModelTierConfig) (provider.Provider, error)

	// ToolRegistryFactory builds the tool registry for an agent run.
	// Nil means manual triggers are disabled.
	ToolRegistryFactory func(agent *corev1alpha1.InfraAgent) (*tools.Registry, error)
}

// +kubebuilder:rbac:groups=core.infraagent.io,resources=infraagents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.infraagent.io,resources=infraagents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.infraagent.io,resources=infraagents/finalizers,verbs=update

// Reconcile handles InfraAgent create/update/delete events.
// Phase 0: logs reconciliation and sets basic status. No execution logic yet.
func (r *InfraAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the InfraAgent instance
	agent := &corev1alpha1.InfraAgent{}
	if err := r.Get(ctx, req.NamespacedName, agent); err != nil {
		if errors.IsNotFound(err) {
			log.Info("InfraAgent deleted", "name", req.Name, "namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	log.Info("Reconciling InfraAgent",
		"name", agent.Name,
		"namespace", agent.Namespace,
		"autonomy", agent.Spec.Guardrails.Autonomy,
		"schedule", agent.Spec.Schedule.Cron,
		"modelTier", agent.Spec.Model.Tier,
		"environmentRef", agent.Spec.EnvironmentRef,
		"paused", agent.Spec.Paused,
	)

	// Step 2.25: Check for manual trigger annotation
	if annotations := agent.GetAnnotations(); annotations != nil {
		if annotations[AnnotationRunNow] == "true" {
			log.Info("Manual run triggered via annotation", "agent", agent.Name)

			// Remove the annotation immediately to prevent re-trigger
			delete(annotations, AnnotationRunNow)
			agent.SetAnnotations(annotations)
			if err := r.Update(ctx, agent); err != nil {
				log.Error(err, "Failed to remove run-now annotation")
				return ctrl.Result{}, err
			}

			// Execute the run if runner is configured
			if r.Runner != nil {
				go func() {
					runCtx := context.Background()
					runLog := log.WithValues("trigger", "manual", "agent", agent.Name)

					cfg := runner.RunConfig{
						Trigger: corev1alpha1.RunTriggerManual,
					}

					// Create provider if factory is available
					if r.ProviderFactory != nil {
						p, err := r.ProviderFactory(agent, nil)
						if err != nil {
							runLog.Error(err, "Failed to create LLM provider")
							return
						}
						cfg.Provider = p
					}

					// Create tool registry if factory is available
					if r.ToolRegistryFactory != nil {
						reg, err := r.ToolRegistryFactory(agent)
						if err != nil {
							runLog.Error(err, "Failed to create tool registry")
							return
						}
						cfg.ToolRegistry = reg
					}

					agentRun, err := r.Runner.Execute(runCtx, agent, cfg)
					if err != nil {
						runLog.Error(err, "Agent run failed")
						return
					}
					runLog.Info("Agent run completed",
						"run", agentRun.Name,
						"phase", agentRun.Status.Phase,
					)
				}()
			} else {
				log.Info("Runner not configured — manual trigger acknowledged but no execution",
					"agent", agent.Name)
			}
		}
	}

	// Determine phase
	phase := corev1alpha1.InfraAgentPhaseReady
	if agent.Spec.Paused {
		phase = corev1alpha1.InfraAgentPhasePaused
	}

	// Check that the referenced AgentEnvironment exists
	env := &corev1alpha1.AgentEnvironment{}
	envKey := client.ObjectKey{
		Namespace: agent.Namespace,
		Name:      agent.Spec.EnvironmentRef,
	}
	if err := r.Get(ctx, envKey, env); err != nil {
		if errors.IsNotFound(err) {
			log.Info("AgentEnvironment not found",
				"environmentRef", agent.Spec.EnvironmentRef,
			)
			phase = corev1alpha1.InfraAgentPhasePending
			meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:               "EnvironmentReady",
				Status:             metav1.ConditionFalse,
				Reason:             "EnvironmentNotFound",
				Message:            fmt.Sprintf("AgentEnvironment %q not found in namespace %q", agent.Spec.EnvironmentRef, agent.Namespace),
				ObservedGeneration: agent.Generation,
			})
		} else {
			return ctrl.Result{}, err
		}
	} else {
		meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
			Type:               "EnvironmentReady",
			Status:             metav1.ConditionTrue,
			Reason:             "EnvironmentFound",
			Message:            fmt.Sprintf("AgentEnvironment %q is available", agent.Spec.EnvironmentRef),
			ObservedGeneration: agent.Generation,
		})
	}

	// Update status
	agent.Status.Phase = phase
	if err := r.Status().Update(ctx, agent); err != nil {
		log.Error(err, "Failed to update InfraAgent status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *InfraAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.InfraAgent{}).
		Named("infraagent").
		Complete(r)
}
