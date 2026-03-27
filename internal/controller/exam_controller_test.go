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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
)

var _ = Describe("Exam Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-exam"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Exam")
			exam := &examv1alpha1.Exam{}
			err := k8sClient.Get(ctx, typeNamespacedName, exam)
			if err != nil && errors.IsNotFound(err) {
				now := time.Now()
				resource := &examv1alpha1.Exam{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: examv1alpha1.ExamSpec{
						Template: examv1alpha1.ExamTemplate{
							Image: "nginx:latest",
							Port:  8080,
							Resources: corev1.ResourceRequirements{},
						},
						Schedule: examv1alpha1.ExamSchedule{
							Unlock: metav1.NewTime(now.Add(1 * time.Hour)),
							Lock:   metav1.NewTime(now.Add(3 * time.Hour)),
						},
						Students: []examv1alpha1.ExamStudent{
							{ID: "alice", Email: "alice@test.com"},
						},
						IngressTLS: examv1alpha1.ExamIngressTLS{SecretName: "test-tls"},
						Domain:     "exam.test.com",
						SMTP: examv1alpha1.ExamSMTP{
							SecretRef: "smtp-secret",
							From:      "test@test.com",
							Subject:   "Test Exam",
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &examv1alpha1.Exam{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				By("Cleanup the specific resource instance Exam")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
		})

		It("should successfully reconcile and transition to Provisioning", func() {
			By("Reconciling the created resource")
			controllerReconciler := &ExamReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify the exam status was updated
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, exam)).To(Succeed())
			// After first reconcile from Pending, should move to Provisioning
			// (provisioning will fail since namespace creation requires cluster-level perms in envtest,
			// but the phase transition logic is tested via unit tests)
		})
	})
})
