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
	"errors"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/metrics"
	"github.com/rdrake/exam-controller/internal/notifier"
	"github.com/rdrake/exam-controller/internal/provisioner"
	"github.com/rdrake/exam-controller/internal/smoketest"
)

// errorSender is a Sender that always returns an error.
type errorSender struct{}

func (e *errorSender) Send(from string, to []string, msg []byte) error {
	return fmt.Errorf("SMTP connection refused")
}

// createExamCRWithDryRun creates an Exam CR with DryRun spec set on the schedule.
func createExamCRWithDryRun(ctx context.Context, name string, unlock time.Time, students []examv1alpha1.ExamStudent, spares int, dryRunBefore time.Duration) {
	resource := &examv1alpha1.Exam{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: examCRNamespace,
		},
		Spec: examv1alpha1.ExamSpec{
			Template: examv1alpha1.ExamTemplate{
				Image:     "nginx:latest",
				Port:      8080,
				Resources: corev1.ResourceRequirements{},
			},
			Schedule: examv1alpha1.ExamSchedule{
				Unlock:          metav1.NewTime(unlock),
				Duration:        metav1.Duration{Duration: 2 * time.Hour},
				TimeMultiplier:  1.5,
				ProvisionBefore: metav1.Duration{Duration: 1 * time.Hour},
				Retention:       metav1.Duration{Duration: 24 * time.Hour},
				DryRun: &examv1alpha1.ExamDryRunSpec{
					Before: metav1.Duration{Duration: dryRunBefore},
				},
			},
			Students: students,
			Spares:   spares,
			Email: examv1alpha1.ExamEmail{
				Before:          metav1.Duration{Duration: 30 * time.Minute},
				SendInterval:    metav1.Duration{Duration: 100 * time.Millisecond},
				InstructorEmail: "prof@test.com",
				From:            "test@test.com",
				Subject:         "Test Exam",
			},
		},
	}
	Expect(k8sClient.Create(ctx, resource)).To(Succeed())
}

var _ = Describe("Error Paths and Dry Run", func() {
	Describe("Provisioning degraded conditions", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("errprov")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			fakeSender = &notifier.FakeSender{}
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("sets ProvisioningDegraded condition when instance fails", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)

			nsName := examNamespace(examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			// Set an invalid DNS-1035 slug to trigger a provisioning failure.
			exam.Status.Students[0].Slug = "-invalid-slug"
			Expect(k8sClient.Status().Update(ctx, exam)).To(Succeed())

			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)

			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			cond := meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionProvisioningDegraded)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal(examv1alpha1.ReasonSomeInstancesFailed))
		})

		It("continues provisioning remaining students after one fails", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
				{ID: "charlie", Email: "charlie@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)

			// Set alice's slug to an invalid value that will fail K8s validation
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			exam.Status.Students[0].Slug = "-invalid-slug"
			Expect(k8sClient.Status().Update(ctx, exam)).To(Succeed())

			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)

			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			// Alice should be Failed
			Expect(exam.Status.Students[0].Phase).To(Equal(examv1alpha1.StudentPhaseFailed))

			// Bob and Charlie should be Provisioned
			Expect(exam.Status.Students[1].Phase).To(Equal(examv1alpha1.StudentPhaseProvisioned))
			Expect(exam.Status.Students[2].Phase).To(Equal(examv1alpha1.StudentPhaseProvisioned))

			// Verify their Deployments actually exist
			nsName := examNamespace(examName, examCRNamespace)
			var deps appsv1.DeploymentList
			Expect(k8sClient.List(ctx, &deps,
				client.InNamespace(nsName),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			// Only bob and charlie deployments should exist (alice failed)
			Expect(deps.Items).To(HaveLen(2))
		})
	})

	Describe("SMTP failure handling", func() {
		var (
			ctx      context.Context
			examName string
			nn       types.NamespacedName
			unlock   time.Time
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("errsmtp")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("handles SMTP failure gracefully", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 1)
			preseedSlugs(ctx, nn)

			reg := prometheus.NewRegistry()
			m := metrics.NewExamMetrics(reg)

			// Drive to Ready with a working sender first
			workingSender := &notifier.FakeSender{}
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, workingSender, m)

			// Now swap in the error sender and set clock past emailTime
			reconciler.Sender = &errorSender{}
			reconciler.Metrics = m
			emailTime := unlock.Add(-30 * time.Minute)
			reconciler.Now = func() time.Time { return emailTime.Add(1 * time.Minute) }

			// Reconcile to send first email (which will fail)
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			// First student should have Failed email status
			Expect(exam.Status.Students[0].EmailStatus).To(Equal(examv1alpha1.EmailStatusFailed))

			// EmailsFailed metric should be incremented
			Expect(counterValue(m.EmailsFailed, examName, examCRNamespace)).To(BeNumerically(">=", 1))

			// Exam should still be in Ready phase (not stuck)
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

			// Reconcile again — second student also fails
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Students[1].EmailStatus).To(Equal(examv1alpha1.EmailStatusFailed))

			// After all students processed, AllEmailsSent should still get set
			// (the condition marks completion, not success).
			// One more reconcile to set the condition.
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			cond := meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionAllEmailsSent)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Describe("Dry run", func() {
		var (
			ctx      context.Context
			examName string
			nn       types.NamespacedName
			unlock   time.Time
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("errdry")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("sets DryRunFailed condition when health checks fail", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}
			createExamCRWithDryRun(ctx, examName, unlock, students, 1, 20*time.Minute)
			preseedSlugs(ctx, nn)

			reg := prometheus.NewRegistry()
			m := metrics.NewExamMetrics(reg)
			fakeSender := &notifier.FakeSender{}

			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, m)
			reconciler.Checker = &smoketest.FakeChecker{
				HealthErr: errors.New("connection refused"),
			}
			reconciler.Metrics = m

			dryRunTime := unlock.Add(-20 * time.Minute)
			reconciler.Now = func() time.Time { return dryRunTime.Add(1 * time.Minute) }

			drainEmails(ctx, reconciler, nn)

			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			cond := meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionDryRunFailed)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal(examv1alpha1.ReasonSomeFailed))

			completeCond := meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionDryRunComplete)
			Expect(completeCond).NotTo(BeNil())
			Expect(completeCond.Status).To(Equal(metav1.ConditionTrue))

			Expect(exam.Status.DryRun).NotTo(BeNil())
			Expect(exam.Status.DryRun.Failed).To(BeNumerically(">", 0))
		})

		It("sets NetworkPolicyEnforced=false when blocked check fails", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCRWithDryRun(ctx, examName, unlock, students, 0, 20*time.Minute)
			preseedSlugs(ctx, nn)

			fakeSender := &notifier.FakeSender{}
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)

			// Health passes but blocked check fails (service is reachable)
			reconciler.Checker = &smoketest.FakeChecker{
				BlockedErr: errors.New("reachable"),
			}

			dryRunTime := unlock.Add(-20 * time.Minute)
			reconciler.Now = func() time.Time { return dryRunTime.Add(1 * time.Minute) }
			drainEmails(ctx, reconciler, nn)

			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			cond := meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionNetworkPolicyEnforced)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(examv1alpha1.ReasonNotEnforced))
		})

		It("runDryRun populates Status.DryRun", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}
			createExamCRWithDryRun(ctx, examName, unlock, students, 1, 20*time.Minute)
			preseedSlugs(ctx, nn)

			reg := prometheus.NewRegistry()
			m := metrics.NewExamMetrics(reg)
			fakeSender := &notifier.FakeSender{}

			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, m)
			reconciler.Checker = &smoketest.FakeChecker{}
			reconciler.Metrics = m

			dryRunTime := unlock.Add(-20 * time.Minute)
			reconciler.Now = func() time.Time { return dryRunTime.Add(1 * time.Minute) }
			drainEmails(ctx, reconciler, nn)

			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

			assertDryRunPreserved(exam, exam.Status.DryRun.CompletedAt.Time, 3, 0)
			Expect(exam.Status.DryRun.Failures).To(BeEmpty())

			cond := meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionDryRunComplete)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))

			npCond := meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionNetworkPolicyEnforced)
			Expect(npCond).NotTo(BeNil())
			Expect(npCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(npCond.Reason).To(Equal(examv1alpha1.ReasonVerified))

			Expect(testutil.ToFloat64(m.DryRunPassed.WithLabelValues(examName, examCRNamespace))).To(Equal(float64(3)))
			Expect(testutil.ToFloat64(m.DryRunFailed.WithLabelValues(examName, examCRNamespace))).To(Equal(float64(0)))
		})

		It("preserves DryRun status across Unlocked, Locked, and TearingDown phases", func() {
			reconciler, _ := triggerDryRun(ctx, examName, nn, unlock)

			By("Verifying DryRun status is populated after completion in Ready phase")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))
			Expect(exam.Status.DryRun.Failures).To(BeEmpty())

			completeCond := meta.FindStatusCondition(exam.Status.Conditions, examv1alpha1.ConditionDryRunComplete)
			Expect(completeCond).NotTo(BeNil())
			Expect(completeCond.Status).To(Equal(metav1.ConditionTrue))

			origCompletedAt := exam.Status.DryRun.CompletedAt.Time

			By("Advancing clock to Unlocked phase")
			reconciler.Now = func() time.Time { return unlock.Add(5 * time.Minute) }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))
			assertDryRunPreserved(exam, origCompletedAt, 3, 0)

			By("Advancing clock to Locked phase")
			lockTime := computeLockTime(unlock, 2*time.Hour, 1.5)
			reconciler.Now = func() time.Time { return lockTime.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseLocked))
			assertDryRunPreserved(exam, origCompletedAt, 3, 0)

			By("Advancing clock to TearingDown phase")
			retentionDeadline := lockTime.Add(24 * time.Hour)
			reconciler.Now = func() time.Time { return retentionDeadline.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseTearingDown))
			assertDryRunPreserved(exam, origCompletedAt, 3, 0)
		})

		It("does not re-trigger dry run after completion in Ready phase", func() {
			reconciler, dryRunTime := triggerDryRun(ctx, examName, nn, unlock)

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			origCompletedAt := exam.Status.DryRun.CompletedAt.Time

			By("Reconciling again in the same Ready phase (still in dry run window)")
			reconciler.Now = func() time.Time { return dryRunTime.Add(5 * time.Minute) }
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))
			assertDryRunPreserved(exam, origCompletedAt, 3, 0)

			By("Reconciling a third time further into the window")
			reconciler.Now = func() time.Time { return dryRunTime.Add(10 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			assertDryRunPreserved(exam, origCompletedAt, 3, 0)
		})
	})
})
