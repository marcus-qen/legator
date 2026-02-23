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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

var _ = Describe("UserPolicy Controller", func() {
	ctx := context.Background()

	It("marks a valid UserPolicy as ready", func() {
		name := "test-userpolicy-valid"
		key := types.NamespacedName{Name: name}

		resource := &corev1alpha1.UserPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1alpha1.UserPolicySpec{
				Description: "Admin policy for platform team",
				Subjects: []corev1alpha1.UserPolicySubject{
					{Claim: "groups", Value: "platform-team"},
				},
				Role: corev1alpha1.UserPolicyRoleAdmin,
				Scope: corev1alpha1.UserPolicyScope{
					Namespaces: []string{"agents", "legator-system"},
					Agents:     []string{"*"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		DeferCleanup(func() {
			cleanup := &corev1alpha1.UserPolicy{}
			err := k8sClient.Get(ctx, key, cleanup)
			if err == nil {
				Expect(k8sClient.Delete(ctx, cleanup)).To(Succeed())
			}
			if err != nil {
				Expect(errors.IsNotFound(err)).To(BeTrue())
			}
		})

		reconciler := &UserPolicyReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		updated := &corev1alpha1.UserPolicy{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.Ready).To(BeTrue())
		Expect(updated.Status.ValidationErrors).To(BeEmpty())
		Expect(updated.Status.EffectiveRole).To(Equal(corev1alpha1.UserPolicyRoleAdmin))
		Expect(updated.Status.MatchedSubjects).To(Equal(int32(1)))
		Expect(updated.Status.Conditions).NotTo(BeEmpty())
		Expect(updated.Status.Conditions[0].Type).To(Equal("Ready"))
		Expect(updated.Status.Conditions[0].Status).To(Equal(metav1.ConditionTrue))
	})

	It("marks an invalid UserPolicy as not ready", func() {
		name := "test-userpolicy-invalid"
		key := types.NamespacedName{Name: name}

		resource := &corev1alpha1.UserPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1alpha1.UserPolicySpec{
				Subjects: []corev1alpha1.UserPolicySubject{
					{Claim: "email", Value: "ops@example.com"},
				},
				Role: corev1alpha1.UserPolicyRoleViewer,
				Scope: corev1alpha1.UserPolicyScope{
					MaxAutonomy: corev1alpha1.UserPolicyAutonomyAutomateSafe,
				},
			},
		}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		DeferCleanup(func() {
			cleanup := &corev1alpha1.UserPolicy{}
			err := k8sClient.Get(ctx, key, cleanup)
			if err == nil {
				Expect(k8sClient.Delete(ctx, cleanup)).To(Succeed())
			}
			if err != nil {
				Expect(errors.IsNotFound(err)).To(BeTrue())
			}
		})

		reconciler := &UserPolicyReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		updated := &corev1alpha1.UserPolicy{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.Ready).To(BeFalse())
		Expect(updated.Status.ValidationErrors).To(ContainElement(ContainSubstring("viewer role only supports")))
		Expect(updated.Status.Conditions).NotTo(BeEmpty())
		Expect(updated.Status.Conditions[0].Status).To(Equal(metav1.ConditionFalse))
	})
})
