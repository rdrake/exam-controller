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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/notifier"
)

var _ = Describe("State transition depth tests", func() {

	// -----------------------------------------------------------------------
	// Re-reconciliation idempotency
	// -----------------------------------------------------------------------
	Describe("Re-reconciliation idempotency", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("idemp")
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

		It("should produce identical status when reconciled multiple times in Ready phase", func() {
			By("Driving to Ready phase")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)

			By("Draining emails to reach a stable state")
			drainEmails(ctx, reconciler, nn)

			By("Capturing status snapshot after first stable reconcile")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			snap := snapshotStatus(exam)

			By("Reconciling 3 more times in the same phase")
			for i := 0; i < 3; i++ {
				_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
				Expect(err).NotTo(HaveOccurred())
			}

			By("Verifying status is unchanged")
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			assertStatusUnchanged(snap, exam)
		})

		It("should produce identical status when reconciled multiple times in Unlocked phase", func() {
			By("Driving to Unlocked phase")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseUnlocked, unlock, fakeSender, nil)

			By("Capturing status snapshot")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			snap := snapshotStatus(exam)

			By("Reconciling 3 more times in the same phase")
			for i := 0; i < 3; i++ {
				_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
				Expect(err).NotTo(HaveOccurred())
			}

			By("Verifying status is unchanged")
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			assertStatusUnchanged(snap, exam)
		})

		It("should produce identical status when reconciled multiple times in Locked phase", func() {
			lockTime := computeLockTime(unlock, 2*time.Hour, 1.5)

			By("Driving to Locked phase")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseLocked, unlock, fakeSender, nil)

			By("Reconciling once more to stabilize any notifications")
			reconciler.Now = func() time.Time { return lockTime.Add(10 * time.Minute) }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Capturing status snapshot")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			snap := snapshotStatus(exam)

			By("Reconciling 3 more times")
			for i := 0; i < 3; i++ {
				_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
				Expect(err).NotTo(HaveOccurred())
			}

			By("Verifying status is unchanged")
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			assertStatusUnchanged(snap, exam)
		})

		It("should not duplicate conditions on repeated reconciles", func() {
			By("Driving to Ready phase and draining emails")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)
			drainEmails(ctx, reconciler, nn)

			By("Counting conditions")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			initialConditionCount := len(exam.Status.Conditions)
			Expect(initialConditionCount).To(BeNumerically(">", 0))

			By("Reconciling 5 more times")
			for i := 0; i < 5; i++ {
				_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
				Expect(err).NotTo(HaveOccurred())
			}

			By("Verifying condition count is unchanged")
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Conditions).To(HaveLen(initialConditionCount),
				"conditions should not duplicate on repeated reconciles")

			By("Verifying each condition type appears exactly once")
			seen := map[string]bool{}
			for _, c := range exam.Status.Conditions {
				Expect(seen[c.Type]).To(BeFalse(),
					"condition %q appears more than once", c.Type)
				seen[c.Type] = true
			}
		})
	})

	// -----------------------------------------------------------------------
	// Failed student persistence through phases
	// -----------------------------------------------------------------------
	Describe("Failed student persistence through phases", func() {
		var (
			ctx         context.Context
			examName    string
			nn          types.NamespacedName
			unlock      time.Time
			lockTime    time.Time
			retDeadline time.Time
			fakeSender  *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("failpersist")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			lockTime = computeLockTime(unlock, 2*time.Hour, 1.5)
			retDeadline = lockTime.Add(24 * time.Hour)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
				{ID: "carol", Email: "carol@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 1)
			preseedSlugs(ctx, nn)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should keep failed students and spares as Failed through Unlocked -> Locked -> TearingDown", func() {
			By("Driving to Unlocked phase")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseUnlocked, unlock, fakeSender, nil)

			By("Marking alice and the spare as Failed")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			for i := range exam.Status.Students {
				if exam.Status.Students[i].ID == "alice" {
					exam.Status.Students[i].Phase = examv1alpha1.StudentPhaseFailed
				}
			}
			exam.Status.Spares[0].Phase = examv1alpha1.StudentPhaseFailed
			Expect(k8sClient.Status().Update(ctx, exam)).To(Succeed())

			By("Reconciling in Unlocked phase — failed should stay failed")
			reconciler.Now = func() time.Time { return unlock.Add(30 * time.Minute) }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))
			assertStudentPhase(exam, "alice", examv1alpha1.StudentPhaseFailed)
			assertStudentPhase(exam, "bob", examv1alpha1.StudentPhaseUnlocked)
			assertStudentPhase(exam, "carol", examv1alpha1.StudentPhaseUnlocked)
			Expect(exam.Status.Spares[0].Phase).To(Equal(examv1alpha1.StudentPhaseFailed))

			By("Transitioning to Locked — failed should stay failed")
			reconciler.Now = func() time.Time { return lockTime.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseLocked))
			assertStudentPhase(exam, "alice", examv1alpha1.StudentPhaseFailed)
			assertStudentPhase(exam, "bob", examv1alpha1.StudentPhaseLocked)
			assertStudentPhase(exam, "carol", examv1alpha1.StudentPhaseLocked)
			Expect(exam.Status.Spares[0].Phase).To(Equal(examv1alpha1.StudentPhaseFailed))

			By("Transitioning to TearingDown — failed should still be failed")
			reconciler.Now = func() time.Time { return retDeadline.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseTearingDown))
			assertStudentPhase(exam, "alice", examv1alpha1.StudentPhaseFailed)
			assertStudentPhase(exam, "bob", examv1alpha1.StudentPhaseLocked)
			assertStudentPhase(exam, "carol", examv1alpha1.StudentPhaseLocked)
			Expect(exam.Status.Spares[0].Phase).To(Equal(examv1alpha1.StudentPhaseFailed))
		})
	})

	// -----------------------------------------------------------------------
	// Per-student phase trace
	// -----------------------------------------------------------------------
	Describe("Per-student phase trace through all exam phases", func() {
		var (
			ctx         context.Context
			examName    string
			nn          types.NamespacedName
			unlock      time.Time
			lockTime    time.Time
			retDeadline time.Time
			fakeSender  *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("trace")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			lockTime = computeLockTime(unlock, 2*time.Hour, 1.5)
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

		It("should trace each student and spare through Provisioned -> Unlocked -> Locked", func() {
			exam := &examv1alpha1.Exam{}

			// ---- Provisioning ----
			By("Provisioning phase: students should be Provisioned")
			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseProvisioning))
			for _, s := range exam.Status.Students {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseProvisioned),
					"student %s should be Provisioned in Provisioning phase", s.ID)
				Expect(s.Slug).NotTo(BeEmpty())
				Expect(s.URL).NotTo(BeEmpty())
			}
			for i, s := range exam.Status.Spares {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseProvisioned),
					"spare %d should be Provisioned in Provisioning phase", i)
				Expect(s.Slug).NotTo(BeEmpty())
				Expect(s.URL).NotTo(BeEmpty())
			}

			// ---- Ready ----
			By("Ready phase: students should still be Provisioned")
			patchDeploymentsReady(ctx, examNamespace(examName, examCRNamespace), examName)
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))
			for _, s := range exam.Status.Students {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseProvisioned),
					"student %s should still be Provisioned in Ready phase", s.ID)
			}
			for i, s := range exam.Status.Spares {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseProvisioned),
					"spare %d should still be Provisioned in Ready phase", i)
			}

			// ---- Unlocked ----
			By("Unlocked phase: students should become Unlocked")
			reconciler.Now = func() time.Time { return unlock.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))
			for _, s := range exam.Status.Students {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseUnlocked),
					"student %s should be Unlocked in Unlocked phase", s.ID)
			}
			for i, s := range exam.Status.Spares {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseUnlocked),
					"spare %d should be Unlocked in Unlocked phase", i)
			}

			// ---- Locked ----
			By("Locked phase: students should become Locked")
			reconciler.Now = func() time.Time { return lockTime.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseLocked))
			for _, s := range exam.Status.Students {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseLocked),
					"student %s should be Locked in Locked phase", s.ID)
			}
			for i, s := range exam.Status.Spares {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseLocked),
					"spare %d should be Locked in Locked phase", i)
			}

			// ---- TearingDown ----
			By("TearingDown phase: student phases should remain Locked")
			reconciler.Now = func() time.Time { return retDeadline.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseTearingDown))
			// Student phases should stay at their last value (Locked)
			for _, s := range exam.Status.Students {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseLocked),
					"student %s should remain Locked in TearingDown phase", s.ID)
			}
			for i, s := range exam.Status.Spares {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseLocked),
					"spare %d should remain Locked in TearingDown phase", i)
			}
		})

		It("should preserve slug and URL across all phase transitions", func() {
			By("Driving to Provisioning to capture initial slugs/URLs")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseProvisioning, unlock, fakeSender, nil)

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			type identity struct {
				slug string
				url  string
			}
			studentIDs := make(map[string]identity)
			for _, s := range exam.Status.Students {
				studentIDs[s.ID] = identity{slug: s.Slug, url: s.URL}
			}
			spareIDs := make([]identity, len(exam.Status.Spares))
			for i, s := range exam.Status.Spares {
				spareIDs[i] = identity{slug: s.Slug, url: s.URL}
			}

			By("Driving through Ready -> Unlocked -> Locked -> TearingDown")
			patchDeploymentsReady(ctx, examNamespace(examName, examCRNamespace), examName)
			phases := []struct {
				phase examv1alpha1.ExamPhase
				clock time.Time
			}{
				{examv1alpha1.ExamPhaseReady, unlock.Add(-30 * time.Minute)},
				{examv1alpha1.ExamPhaseUnlocked, unlock.Add(5 * time.Minute)},
				{examv1alpha1.ExamPhaseLocked, lockTime.Add(5 * time.Minute)},
				{examv1alpha1.ExamPhaseTearingDown, retDeadline.Add(5 * time.Minute)},
			}
			for _, p := range phases {
				reconciler.Now = func() time.Time { return p.clock }
				_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
				Expect(err).NotTo(HaveOccurred())

				Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
				Expect(exam.Status.Phase).To(Equal(p.phase))

				for _, s := range exam.Status.Students {
					orig := studentIDs[s.ID]
					Expect(s.Slug).To(Equal(orig.slug),
						"student %s slug changed in phase %s", s.ID, p.phase)
					Expect(s.URL).To(Equal(orig.url),
						"student %s URL changed in phase %s", s.ID, p.phase)
				}
				for i, s := range exam.Status.Spares {
					Expect(s.Slug).To(Equal(spareIDs[i].slug),
						"spare %d slug changed in phase %s", i, p.phase)
					Expect(s.URL).To(Equal(spareIDs[i].url),
						"spare %d URL changed in phase %s", i, p.phase)
				}
			}
		})
	})

	// -----------------------------------------------------------------------
	// Condition detail verification
	// -----------------------------------------------------------------------
	Describe("Condition detail verification", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("conddetail")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should set ObservedGeneration on conditions", func() {
			By("Driving to Ready phase")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)
			drainEmails(ctx, reconciler, nn)

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			By("Verifying all conditions have ObservedGeneration matching the exam's Generation")
			Expect(exam.Status.Conditions).NotTo(BeEmpty())
			for _, c := range exam.Status.Conditions {
				Expect(c.ObservedGeneration).To(Equal(exam.Generation),
					"condition %q should have ObservedGeneration=%d", c.Type, exam.Generation)
			}
		})

		It("should preserve LastTransitionTime when condition status is unchanged", func() {
			By("Driving to Ready phase and draining emails")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)
			drainEmails(ctx, reconciler, nn)

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			By("Capturing LastTransitionTime for each condition")
			transitionTimes := make(map[string]metav1.Time)
			for _, c := range exam.Status.Conditions {
				transitionTimes[c.Type] = c.LastTransitionTime
			}

			By("Reconciling again without changing state")
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Verifying LastTransitionTime did not change")
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			for _, c := range exam.Status.Conditions {
				orig, ok := transitionTimes[c.Type]
				if ok {
					Expect(c.LastTransitionTime).To(Equal(orig),
						"condition %q LastTransitionTime should not change on re-reconcile", c.Type)
				}
			}
		})

		It("should set Reason and Message on Provisioned condition", func() {
			By("Driving to Ready phase")
			_ = driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			cond := meta.FindStatusCondition(exam.Status.Conditions, "Provisioned")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("AllHealthy"))
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})

		It("should set Reason and Message on ProvisioningDegraded condition", func() {
			By("Pre-creating the namespace")
			nsName := examNamespace(examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			By("Setting an invalid slug to trigger provisioning failure")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			exam.Status.Students[0].Slug = "-invalid-slug"
			Expect(k8sClient.Status().Update(ctx, exam)).To(Succeed())

			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			cond := meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionProvisioningDegraded)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("SomeInstancesFailed"))
			Expect(cond.Message).To(Equal("One or more instances failed to provision"))
		})
	})

	// -----------------------------------------------------------------------
	// Boundary timing
	// -----------------------------------------------------------------------
	Describe("Boundary timing", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("boundary")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should transition Ready -> Unlocked at exactly the unlock time", func() {
			By("Driving to Ready phase")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)
			drainEmails(ctx, reconciler, nn)

			By("Reconciling at exactly the unlock time")
			reconciler.Now = func() time.Time { return unlock }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked),
				"should transition at exact unlock time")
		})

		It("should stay Ready 1ms before unlock time", func() {
			By("Driving to Ready phase")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)
			drainEmails(ctx, reconciler, nn)

			By("Reconciling 1ms before unlock")
			reconciler.Now = func() time.Time { return unlock.Add(-time.Millisecond) }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady),
				"should stay Ready 1ms before unlock")
		})

		It("should transition Unlocked -> Locked at exactly the lock time", func() {
			lockTime := computeLockTime(unlock, 2*time.Hour, 1.5)

			By("Driving to Unlocked phase")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseUnlocked, unlock, fakeSender, nil)

			By("Reconciling at exactly the lock time")
			reconciler.Now = func() time.Time { return lockTime }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseLocked),
				"should transition at exact lock time")
		})

		It("should transition Locked -> TearingDown at exactly the retention deadline", func() {
			lockTime := computeLockTime(unlock, 2*time.Hour, 1.5)
			retDeadline := lockTime.Add(24 * time.Hour)

			By("Driving to Locked phase")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseLocked, unlock, fakeSender, nil)

			By("Reconciling at exactly the retention deadline")
			reconciler.Now = func() time.Time { return retDeadline }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseTearingDown),
				"should transition at exact retention deadline")
		})
	})

	// -----------------------------------------------------------------------
	// Email status persistence across phases
	// -----------------------------------------------------------------------
	Describe("Email status persistence across phases", func() {
		var (
			ctx         context.Context
			examName    string
			nn          types.NamespacedName
			unlock      time.Time
			lockTime    time.Time
			retDeadline time.Time
			fakeSender  *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("emailpersist")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			lockTime = computeLockTime(unlock, 2*time.Hour, 1.5)
			retDeadline = lockTime.Add(24 * time.Hour)
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

		It("should preserve EmailStatus and EmailSentAt through Unlocked -> Locked -> TearingDown", func() {
			By("Driving to Ready and draining all emails")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)
			drainEmails(ctx, reconciler, nn)

			By("Capturing email status after emails are sent")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			type emailSnapshot struct {
				status examv1alpha1.EmailStatus
				sentAt *metav1.Time
			}
			emailSnaps := make(map[string]emailSnapshot)
			for _, s := range exam.Status.Students {
				Expect(s.EmailStatus).To(Equal(examv1alpha1.EmailStatusSent),
					"student %s should have Sent email status before transitions", s.ID)
				Expect(s.EmailSentAt).NotTo(BeNil(),
					"student %s should have EmailSentAt set", s.ID)
				emailSnaps[s.ID] = emailSnapshot{
					status: s.EmailStatus,
					sentAt: s.EmailSentAt.DeepCopy(),
				}
			}

			By("Transitioning to Unlocked")
			reconciler.Now = func() time.Time { return unlock.Add(5 * time.Minute) }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))
			for _, s := range exam.Status.Students {
				snap := emailSnaps[s.ID]
				Expect(s.EmailStatus).To(Equal(snap.status),
					"student %s EmailStatus should be preserved in Unlocked", s.ID)
				Expect(s.EmailSentAt.Time).To(BeTemporally("~", snap.sentAt.Time, time.Second),
					"student %s EmailSentAt should be preserved in Unlocked", s.ID)
			}

			By("Transitioning to Locked")
			reconciler.Now = func() time.Time { return lockTime.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseLocked))
			for _, s := range exam.Status.Students {
				snap := emailSnaps[s.ID]
				Expect(s.EmailStatus).To(Equal(snap.status),
					"student %s EmailStatus should be preserved in Locked", s.ID)
				Expect(s.EmailSentAt.Time).To(BeTemporally("~", snap.sentAt.Time, time.Second),
					"student %s EmailSentAt should be preserved in Locked", s.ID)
			}

			By("Transitioning to TearingDown")
			reconciler.Now = func() time.Time { return retDeadline.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseTearingDown))
			for _, s := range exam.Status.Students {
				snap := emailSnaps[s.ID]
				Expect(s.EmailStatus).To(Equal(snap.status),
					"student %s EmailStatus should be preserved in TearingDown", s.ID)
				Expect(s.EmailSentAt.Time).To(BeTemporally("~", snap.sentAt.Time, time.Second),
					"student %s EmailSentAt should be preserved in TearingDown", s.ID)
			}
		})
	})

	// -----------------------------------------------------------------------
	// Condition persistence (ProvisioningDegraded survives to Ready)
	// -----------------------------------------------------------------------
	Describe("Condition persistence across phases", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("condflap")
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

		It("should retain ProvisioningDegraded after recovery and transition to Ready", func() {
			By("Pre-creating the namespace")
			nsName := examNamespace(examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			By("Setting alice's slug to an invalid value")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			exam.Status.Students[0].Slug = "-invalid-slug"
			Expect(k8sClient.Status().Update(ctx, exam)).To(Succeed())

			By("Reconciling to trigger ProvisioningDegraded")
			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			cond := meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionProvisioningDegraded)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			degradedTime := cond.LastTransitionTime

			By("Fixing alice's slug and re-reconciling")
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			exam.Status.Students[0].Slug = "salic00"
			exam.Status.Students[0].Phase = ""
			Expect(k8sClient.Status().Update(ctx, exam)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Patching deployments ready and reconciling to reach Ready")
			patchDeploymentsReady(ctx, nsName, examName)
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

			By("Verifying ProvisioningDegraded still exists from first reconcile")
			cond = meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionProvisioningDegraded)
			Expect(cond).NotTo(BeNil(), "ProvisioningDegraded should persist even after recovery")
			Expect(cond.LastTransitionTime).To(Equal(degradedTime),
				"LastTransitionTime should not change since status stayed True")

			By("Verifying Provisioned condition also exists")
			provCond := meta.FindStatusCondition(exam.Status.Conditions, "Provisioned")
			Expect(provCond).NotTo(BeNil())
			Expect(provCond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	// -----------------------------------------------------------------------
	// Status.Metrics field verification
	// -----------------------------------------------------------------------
	Describe("Status.Metrics field verification", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("statusmetrics")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
				{ID: "carol", Email: "carol@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 2)
			preseedSlugs(ctx, nn)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should populate MetricsSummary with correct counts after provisioning", func() {
			By("Driving to Provisioning phase")
			_ = driveToPhase(ctx, nn, examv1alpha1.ExamPhaseProvisioning, unlock, fakeSender, nil)

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			Expect(exam.Status.Metrics).NotTo(BeNil(), "Metrics summary should be set")
			Expect(exam.Status.Metrics.TotalStudents).To(Equal(3))
			Expect(exam.Status.Metrics.TotalSpares).To(Equal(2))
			Expect(exam.Status.Metrics.InstancesHealthy).To(Equal(5),
				"3 students + 2 spares all healthy")
			Expect(exam.Status.Metrics.InstancesFailed).To(Equal(0))
			Expect(exam.Status.Metrics.EmailsSent).To(Equal(0),
				"no emails sent yet in Provisioning")
			Expect(exam.Status.Metrics.EmailsFailed).To(Equal(0))
		})

		It("should update MetricsSummary after emails are sent", func() {
			By("Driving to Ready and draining emails")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)
			drainEmails(ctx, reconciler, nn)

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			Expect(exam.Status.Metrics).NotTo(BeNil())
			Expect(exam.Status.Metrics.TotalStudents).To(Equal(3))
			Expect(exam.Status.Metrics.TotalSpares).To(Equal(2))
			Expect(exam.Status.Metrics.EmailsSent).To(Equal(3),
				"all 3 students should have emails sent")
			Expect(exam.Status.Metrics.EmailsFailed).To(Equal(0))
		})

		It("should reflect failed instances in MetricsSummary", func() {
			By("Pre-creating the namespace and setting an invalid slug")
			nsName := examNamespace(examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			exam.Status.Students[0].Slug = "-invalid-slug"
			Expect(k8sClient.Status().Update(ctx, exam)).To(Succeed())

			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Metrics).NotTo(BeNil())
			Expect(exam.Status.Metrics.InstancesFailed).To(Equal(1),
				"alice should be counted as failed")
			Expect(exam.Status.Metrics.InstancesHealthy).To(Equal(4),
				"bob + carol + 2 spares should be healthy")
		})

		It("should not set Metrics during TearingDown phase", func() {
			lockTime := computeLockTime(unlock, 2*time.Hour, 1.5)
			retDeadline := lockTime.Add(24 * time.Hour)

			By("Driving to Locked and verifying metrics exist")
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseLocked, unlock, fakeSender, nil)

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Metrics).NotTo(BeNil(), "Metrics should exist in Locked phase")

			By("Capturing metrics before teardown")
			metricsBefore := *exam.Status.Metrics

			By("Transitioning to TearingDown")
			reconciler.Now = func() time.Time { return retDeadline.Add(5 * time.Minute) }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseTearingDown))

			// Metrics should still be present (from last update before teardown)
			// but should not have been re-computed (code skips updateMetricsSummary in TearingDown)
			Expect(exam.Status.Metrics).NotTo(BeNil())
			Expect(exam.Status.Metrics.TotalStudents).To(Equal(metricsBefore.TotalStudents))
		})
	})
})

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// statusSnapshot captures the fields we care about for idempotency checks.
type statusSnapshot struct {
	phase          examv1alpha1.ExamPhase
	message        string
	conditionCount int
	conditionTypes map[string]metav1.ConditionStatus
	studentPhases  map[string]examv1alpha1.StudentPhase
	sparePhases    []examv1alpha1.StudentPhase
}

func snapshotStatus(exam *examv1alpha1.Exam) statusSnapshot {
	s := statusSnapshot{
		phase:          exam.Status.Phase,
		message:        exam.Status.Message,
		conditionCount: len(exam.Status.Conditions),
		conditionTypes: make(map[string]metav1.ConditionStatus),
		studentPhases:  make(map[string]examv1alpha1.StudentPhase),
	}
	for _, c := range exam.Status.Conditions {
		s.conditionTypes[c.Type] = c.Status
	}
	for _, st := range exam.Status.Students {
		s.studentPhases[st.ID] = st.Phase
	}
	for _, sp := range exam.Status.Spares {
		s.sparePhases = append(s.sparePhases, sp.Phase)
	}
	return s
}

func assertStatusUnchanged(snap statusSnapshot, exam *examv1alpha1.Exam) {
	Expect(exam.Status.Phase).To(Equal(snap.phase), "phase should not change")
	Expect(exam.Status.Message).To(Equal(snap.message), "message should not change")
	Expect(exam.Status.Conditions).To(HaveLen(snap.conditionCount), "condition count should not change")
	for _, c := range exam.Status.Conditions {
		Expect(c.Status).To(Equal(snap.conditionTypes[c.Type]),
			"condition %q status should not change", c.Type)
	}
	for _, st := range exam.Status.Students {
		Expect(st.Phase).To(Equal(snap.studentPhases[st.ID]),
			"student %s phase should not change", st.ID)
	}
	for i, sp := range exam.Status.Spares {
		Expect(sp.Phase).To(Equal(snap.sparePhases[i]),
			"spare %d phase should not change", i)
	}
}

func assertStudentPhase(exam *examv1alpha1.Exam, studentID string, expected examv1alpha1.StudentPhase) {
	found := false
	for _, s := range exam.Status.Students {
		if s.ID == studentID {
			ExpectWithOffset(1, s.Phase).To(Equal(expected),
				"student %s phase", studentID)
			found = true
			break
		}
	}
	ExpectWithOffset(1, found).To(BeTrue(), "student %s not found in status", studentID)
}

// Ensure meta import is used.
var _ = meta.FindStatusCondition
