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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
)

var _ = Describe("InfraAgent Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-agent"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		infraagent := &corev1alpha1.InfraAgent{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind InfraAgent")
			err := k8sClient.Get(ctx, typeNamespacedName, infraagent)
			if err != nil && errors.IsNotFound(err) {
				resource := &corev1alpha1.InfraAgent{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: corev1alpha1.InfraAgentSpec{
						Description: "Test agent for unit tests",
						Schedule: corev1alpha1.ScheduleSpec{
							Cron:     "*/5 * * * *",
							Timezone: "UTC",
						},
						Model: corev1alpha1.ModelSpec{
							Tier:        corev1alpha1.ModelTierFast,
							TokenBudget: 8000,
							Timeout:     "60s",
						},
						Skills: []corev1alpha1.SkillRef{
							{Name: "test-skill", Source: "bundled"},
						},
						Guardrails: corev1alpha1.GuardrailsSpec{
							Autonomy:      corev1alpha1.AutonomyObserve,
							MaxIterations: 5,
						},
						EnvironmentRef: "test-env",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &corev1alpha1.InfraAgent{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance InfraAgent")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &InfraAgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify the agent was reconciled and status updated
			updated := &corev1alpha1.InfraAgent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			// Environment doesn't exist, so phase should be Pending
			Expect(updated.Status.Phase).To(Equal(corev1alpha1.InfraAgentPhasePending))
		})
	})
})
