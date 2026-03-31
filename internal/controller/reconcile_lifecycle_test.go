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
	"github.com/rdrake/exam-controller/internal/provisioner"
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
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
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
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: examNamespace(examName, examCRNamespace)}, ns)).To(Succeed())

			// ---- Phase 2: Ready ----
			By("Patching deployments to be ready and reconciling -> Ready")
			patchDeploymentsReady(ctx, examNamespace(examName, examCRNamespace), examName)

			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

			cond := meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionProvisioned)
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
				client.InNamespace(examNamespace(examName, examCRNamespace)),
				client.MatchingLabels{provisioner.LabelExam: examName},
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
				client.InNamespace(examNamespace(examName, examCRNamespace)),
				client.MatchingLabels{provisioner.LabelExam: examName},
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

	Describe("Locked phase with failed students", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("lockfail")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
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
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("preserves Failed phase for failed students and spares when locking", func() {
			lockTime := computeLockTime(unlock, 2*time.Hour, 1.5)

			By("Driving to Unlocked phase")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseUnlocked, unlock, fakeSender, nil)

			By("Marking alice and the spare as Failed")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			exam.Status.Students[0].Phase = examv1alpha1.StudentPhaseFailed
			exam.Status.Spares[0].Phase = examv1alpha1.StudentPhaseFailed
			Expect(k8sClient.Status().Update(ctx, exam)).To(Succeed())

			By("Advancing clock past lock time -> Locked")
			reconciler.Now = func() time.Time { return lockTime.Add(5 * time.Minute) }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseLocked))

			By("Verifying alice stayed Failed")
			Expect(exam.Status.Students[0].Phase).To(Equal(examv1alpha1.StudentPhaseFailed))

			By("Verifying bob transitioned to Locked")
			Expect(exam.Status.Students[1].Phase).To(Equal(examv1alpha1.StudentPhaseLocked))

			By("Verifying spare stayed Failed")
			Expect(exam.Status.Spares[0].Phase).To(Equal(examv1alpha1.StudentPhaseFailed))
		})
	})

	Describe("Controller restart recovery", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			lockTime   time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("restart")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			lockTime = computeLockTime(unlock, 2*time.Hour, 1.5)
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
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should resume from Unlocked without re-provisioning after a restart", func() {
			By("Driving to Unlocked phase with the original reconciler")
			_ = driveToPhase(ctx, nn, examv1alpha1.ExamPhaseUnlocked, unlock, fakeSender, nil)

			By("Capturing resource versions of existing ingresses")
			var ingressesBefore networkingv1.IngressList
			Expect(k8sClient.List(ctx, &ingressesBefore,
				client.InNamespace(examNamespace(examName, examCRNamespace)),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			Expect(ingressesBefore.Items).To(HaveLen(3))
			rvByName := make(map[string]string, len(ingressesBefore.Items))
			for _, ing := range ingressesBefore.Items {
				rvByName[ing.Name] = ing.ResourceVersion
			}

			By("Creating a brand new reconciler (simulating controller restart)")
			restartClock := unlock.Add(10 * time.Minute) // still past unlock, before lock
			newSender := &notifier.FakeSender{}
			restartReconciler := newReconciler(func() time.Time { return restartClock }, newSender, nil)

			By("Reconciling with the new reconciler")
			_, err := restartReconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Verifying exam is still in Unlocked phase")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))

			By("Verifying student phases are still Unlocked")
			for _, s := range exam.Status.Students {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseUnlocked))
			}

			By("Verifying spare phases are still Unlocked")
			for _, s := range exam.Status.Spares {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseUnlocked))
			}

			By("Verifying ingresses still exist and were not recreated")
			var ingressesAfter networkingv1.IngressList
			Expect(k8sClient.List(ctx, &ingressesAfter,
				client.InNamespace(examNamespace(examName, examCRNamespace)),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			Expect(ingressesAfter.Items).To(HaveLen(3))
			for _, ing := range ingressesAfter.Items {
				Expect(ing.ResourceVersion).To(Equal(rvByName[ing.Name]),
					"Ingress %s should not have been recreated", ing.Name)
			}

			By("Advancing the new reconciler's clock past lock time")
			restartReconciler.Now = func() time.Time { return lockTime.Add(5 * time.Minute) }
			_, err = restartReconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Verifying transition to Locked")
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseLocked))
		})
	})

	Describe("Duration extension while Unlocked", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
			reconciler *ExamReconciler
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("extend")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should recompute lock time when duration is extended", func() {
			By("Driving to Unlocked phase")
			reconciler = driveToPhase(ctx, nn, examv1alpha1.ExamPhaseUnlocked, unlock, fakeSender, nil)

			By("Reading the current exam and noting the original ComputedLockTime")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.ComputedLockTime).NotTo(BeNil())
			originalLockTime := exam.Status.ComputedLockTime.Time
			// Original: unlock + 2h * 1.5 = unlock + 3h
			expectedOriginal := computeLockTime(unlock, 2*time.Hour, 1.5)
			Expect(originalLockTime).To(BeTemporally("~", expectedOriginal, time.Second))

			By("Patching exam duration from 2h to 4h")
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			exam.Spec.Schedule.Duration = metav1.Duration{Duration: 4 * time.Hour}
			Expect(k8sClient.Update(ctx, exam)).To(Succeed())

			By("Reconciling with clock still before the new lock time")
			// New lock time: unlock + 4h * 1.5 = unlock + 6h
			newExpectedLockTime := computeLockTime(unlock, 4*time.Hour, 1.5)
			// Set clock to a point after the OLD lock time but before the NEW lock time
			reconciler.Now = func() time.Time { return unlock.Add(4 * time.Hour) }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ComputedLockTime has been recalculated")
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.ComputedLockTime).NotTo(BeNil())
			Expect(exam.Status.ComputedLockTime.Time).To(BeTemporally("~", newExpectedLockTime, time.Second))
			Expect(exam.Status.ComputedLockTime.Time).NotTo(BeTemporally("~", originalLockTime, time.Second))

			By("Verifying RetentionDeadline has been recalculated based on new lock time")
			newExpectedRetention := newExpectedLockTime.Add(24 * time.Hour)
			Expect(exam.Status.RetentionDeadline).NotTo(BeNil())
			Expect(exam.Status.RetentionDeadline.Time).To(BeTemporally("~", newExpectedRetention, time.Second))

			By("Verifying exam is still Unlocked (not prematurely locked)")
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))

			By("Verifying student phases are still Unlocked")
			for _, s := range exam.Status.Students {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseUnlocked))
			}

			By("Advancing clock past the new lock time")
			reconciler.Now = func() time.Time { return newExpectedLockTime.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Verifying transition to Locked")
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseLocked))
		})
	})
})
