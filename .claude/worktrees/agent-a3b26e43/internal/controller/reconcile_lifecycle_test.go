//go:build integration

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
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/notifier"
)

var _ = Describe("Exam Lifecycle", func() {
	Describe("Happy-path lifecycle", func() {
		var (
			ctx         context.Context
			examName    string
			nn          types.NamespacedName
			unlock      time.Time
			lockTime    time.Time
			retDeadline time.Time
			fakeSender  *notifier.FakeSender
			reconciler  *ExamReconciler
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("lifecycle")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			// lockTime = unlock + duration * multiplier = unlock + 2h * 1.5 = unlock + 3h
			lockTime = unlock.Add(3 * time.Hour)
			retDeadline = lockTime.Add(24 * time.Hour)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 1)
			preseedSlugs(ctx, nn)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should transition through all 6 phases", func() {
			// ---- Phase 1: Provisioning ----
			By("Reconciling after provisionTime but before unlock -> Provisioning")
			clockTime := unlock.Add(-30 * time.Minute)
			reconciler = newReconciler(func() time.Time { return clockTime }, fakeSender, nil)

			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Finalizers).To(ContainElement(finalizerName))
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseProvisioning))
			Expect(exam.Status.ComputedLockTime).NotTo(BeNil())
			Expect(exam.Status.ProvisionTime).NotTo(BeNil())
			Expect(exam.Status.EmailTime).NotTo(BeNil())
			Expect(exam.Status.RetentionDeadline).NotTo(BeNil())

			By("Verifying student statuses are populated")
			Expect(exam.Status.Students).To(HaveLen(2))
			for _, s := range exam.Status.Students {
				Expect(s.Slug).NotTo(BeEmpty())
				Expect(s.URL).To(HavePrefix("https://"))
				Expect(s.URL).To(ContainSubstring("exam.test.com"))
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseProvisioned))
			}

			By("Verifying spare statuses are populated")
			Expect(exam.Status.Spares).To(HaveLen(1))
			Expect(exam.Status.Spares[0].Slug).NotTo(BeEmpty())
			Expect(exam.Status.Spares[0].URL).To(HavePrefix("https://"))
			Expect(exam.Status.Spares[0].Phase).To(Equal(examv1alpha1.StudentPhaseProvisioned))

			By("Verifying student namespace exists")
			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: examNamespace(examName)}, ns)).To(Succeed())

			// ---- Phase 2: Ready ----
			By("Patching deployments to be ready and reconciling -> Ready")
			patchDeploymentsReady(ctx, examNamespace(examName), examName)

			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

			cond := meta.FindStatusCondition(exam.Status.Conditions, "Provisioned")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))

			// ---- Phase 3: Unlocked ----
			By("Advancing clock past unlock time -> Unlocked")
			reconciler.Now = func() time.Time { return unlock.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))

			By("Verifying student phases are Unlocked")
			for _, s := range exam.Status.Students {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseUnlocked))
			}

			By("Verifying spare phases are Unlocked")
			for _, s := range exam.Status.Spares {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseUnlocked))
			}

			By("Verifying ingresses were created")
			var ingresses networkingv1.IngressList
			Expect(k8sClient.List(ctx, &ingresses,
				client.InNamespace(examNamespace(examName)),
				client.MatchingLabels{"exam.otu.ca/exam": examName},
			)).To(Succeed())
			// 2 students + 1 spare = 3 ingresses
			Expect(ingresses.Items).To(HaveLen(3))

			// ---- Phase 4: Locked ----
			By("Advancing clock past lock time -> Locked")
			reconciler.Now = func() time.Time { return lockTime.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseLocked))

			By("Verifying student phases are Locked")
			for _, s := range exam.Status.Students {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseLocked))
			}

			By("Verifying spare phases are Locked")
			for _, s := range exam.Status.Spares {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseLocked))
			}

			By("Verifying ingresses were deleted")
			Expect(k8sClient.List(ctx, &ingresses,
				client.InNamespace(examNamespace(examName)),
				client.MatchingLabels{"exam.otu.ca/exam": examName},
			)).To(Succeed())
			Expect(ingresses.Items).To(BeEmpty())

			// ---- Phase 5: TearingDown ----
			By("Advancing clock past retention deadline -> TearingDown")
			reconciler.Now = func() time.Time { return retDeadline.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseTearingDown))
			Expect(exam.Status.Message).To(Equal("Namespace deleted"))
		})
	})
})
