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

var _ = Describe("ModelTierConfig Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-model-config"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: resourceName,
		}
		config := &corev1alpha1.ModelTierConfig{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind ModelTierConfig")
			err := k8sClient.Get(ctx, typeNamespacedName, config)
			if err != nil && errors.IsNotFound(err) {
				resource := &corev1alpha1.ModelTierConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
					Spec: corev1alpha1.ModelTierConfigSpec{
						Tiers: []corev1alpha1.TierMapping{
							{
								Tier:     corev1alpha1.ModelTierFast,
								Provider: "anthropic",
								Model:    "claude-haiku-3-5-20241022",
							},
							{
								Tier:     corev1alpha1.ModelTierStandard,
								Provider: "anthropic",
								Model:    "claude-sonnet-4-20250514",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &corev1alpha1.ModelTierConfig{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance ModelTierConfig")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &ModelTierConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify tier status was populated
			updated := &corev1alpha1.ModelTierConfig{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Ready).To(BeTrue())
			Expect(updated.Status.TierStatus).To(HaveKey("fast"))
			Expect(updated.Status.TierStatus).To(HaveKey("standard"))
		})
	})
})
