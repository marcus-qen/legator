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

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	_ "github.com/marcus-qen/legator/internal/assembler" // used transitively by runner
	"github.com/marcus-qen/legator/internal/provider"
	"github.com/marcus-qen/legator/internal/resolver"
	"github.com/marcus-qen/legator/internal/runner"
	"github.com/marcus-qen/legator/internal/tools"
)

const (
	// AnnotationRunNow triggers a manual agent run when set to "true".
	AnnotationRunNow = "legator.io/run-now"
)

// LegatorAgentReconciler reconciles an LegatorAgent object.
type LegatorAgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Runner executes agent runs. Nil in Phase 0 stubs.
	Runner *runner.Runner

	// ProviderFactory creates LLM providers for agent runs.
	// Nil means manual triggers are disabled (Phase 0 mode).
	ProviderFactory func(agent *corev1alpha1.LegatorAgent, model *corev1alpha1.ModelTierConfig) (provider.Provider, error)

	// ToolRegistryFactory builds the tool registry for an agent run.
	// Nil means manual triggers are disabled.
	ToolRegistryFactory func(agent *corev1alpha1.LegatorAgent, env *resolver.ResolvedEnvironment) (*tools.Registry, error)

	// OnReconcile is called after successful reconciliation with the agent.
	// Used to register webhook triggers with the scheduler.
	OnReconcile func(agent *corev1alpha1.LegatorAgent)
}

// +kubebuilder:rbac:groups=legator.io,resources=legatoragents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=legator.io,resources=legatoragents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=legator.io,resources=legatoragents/finalizers,verbs=update

// Reconcile handles LegatorAgent create/update/delete events.
// Phase 0: logs reconciliation and sets basic status. No execution logic yet.
func (r *LegatorAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the LegatorAgent instance
	agent := &corev1alpha1.LegatorAgent{}
	if err := r.Get(ctx, req.NamespacedName, agent); err != nil {
		if errors.IsNotFound(err) {
			log.Info("LegatorAgent deleted", "name", req.Name, "namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	log.Info("Reconciling LegatorAgent",
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
						// Resolve environment for credential-aware HTTP tools
						var resolvedEnv *resolver.ResolvedEnvironment
						if agent.Spec.EnvironmentRef != "" {
							envResolver := resolver.NewEnvironmentResolver(r.Client, agent.Namespace)
							var envErr error
							resolvedEnv, envErr = envResolver.Resolve(runCtx, agent.Spec.EnvironmentRef)
							if envErr != nil {
								runLog.Error(envErr, "Failed to resolve environment for credentials", "env", agent.Spec.EnvironmentRef)
							} else if resolvedEnv != nil {
								runLog.Info("Resolved environment for credentials",
									"env", agent.Spec.EnvironmentRef,
									"credentialCount", len(resolvedEnv.Credentials))
							}
						}
						reg, err := r.ToolRegistryFactory(agent, resolvedEnv)
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
	phase := corev1alpha1.LegatorAgentPhaseReady
	if agent.Spec.Paused {
		phase = corev1alpha1.LegatorAgentPhasePaused
	}

	// Check that the referenced LegatorEnvironment exists
	env := &corev1alpha1.LegatorEnvironment{}
	envKey := client.ObjectKey{
		Namespace: agent.Namespace,
		Name:      agent.Spec.EnvironmentRef,
	}
	if err := r.Get(ctx, envKey, env); err != nil {
		if errors.IsNotFound(err) {
			log.Info("LegatorEnvironment not found",
				"environmentRef", agent.Spec.EnvironmentRef,
			)
			phase = corev1alpha1.LegatorAgentPhasePending
			meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:               "EnvironmentReady",
				Status:             metav1.ConditionFalse,
				Reason:             "EnvironmentNotFound",
				Message:            fmt.Sprintf("LegatorEnvironment %q not found in namespace %q", agent.Spec.EnvironmentRef, agent.Namespace),
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
			Message:            fmt.Sprintf("LegatorEnvironment %q is available", agent.Spec.EnvironmentRef),
			ObservedGeneration: agent.Generation,
		})
	}

	// Update status
	agent.Status.Phase = phase
	if err := r.Status().Update(ctx, agent); err != nil {
		log.Error(err, "Failed to update LegatorAgent status")
		return ctrl.Result{}, err
	}

	// Notify scheduler of trigger registrations (webhook sources, etc.)
	if r.OnReconcile != nil {
		r.OnReconcile(agent)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LegatorAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.LegatorAgent{}).
		Named("legator").
		Complete(r)
}
