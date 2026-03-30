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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/metrics"
	"github.com/rdrake/exam-controller/internal/notifier"
	"github.com/rdrake/exam-controller/internal/provisioner"
)

var _ = Describe("Concurrent Exam Reconciliation", func() {
	var (
		ctx        context.Context
		examNameA  string
		examNameB  string
		nnA        types.NamespacedName
		nnB        types.NamespacedName
		unlockA    time.Time
		unlockB    time.Time
		fakeSender *notifier.FakeSender
		reconciler *ExamReconciler
		reg        *prometheus.Registry
		m          *metrics.ExamMetrics
		clockTime  time.Time
	)

	BeforeEach(func() {
		ctx = context.Background()
		examNameA = uniqueExamName("conc-a")
		examNameB = uniqueExamName("conc-b")
		nnA = types.NamespacedName{Name: examNameA, Namespace: examCRNamespace}
		nnB = types.NamespacedName{Name: examNameB, Namespace: examCRNamespace}

		// Exam A unlocks at T+60min, Exam B unlocks at T+90min.
		// Use a fixed base time so T+30min is after both provision times
		// (provisionBefore = 1h, so provisionTime = unlock - 1h).
		baseTime := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
		unlockA = baseTime.Add(60 * time.Minute) // 13:00
		unlockB = baseTime.Add(90 * time.Minute) // 13:30

		fakeSender = &notifier.FakeSender{}
		reg = prometheus.NewRegistry()
		m = metrics.NewExamMetrics(reg)

		// Exam A: 2 students, no spares
		studentsA := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
			{ID: "bob", Email: "bob@test.com"},
		}
		createExamCR(ctx, examNameA, unlockA, studentsA, 0)
		preseedSlugs(ctx, nnA)

		// Exam B: 1 student, no spares
		studentsB := []examv1alpha1.ExamStudent{
			{ID: "charlie", Email: "charlie@test.com"},
		}
		createExamCR(ctx, examNameB, unlockB, studentsB, 0)
		preseedSlugs(ctx, nnB)

		// Clock at T+30min — past both provision times, before both unlocks.
		clockTime = baseTime.Add(30 * time.Minute) // 12:30
		reconciler = newReconciler(func() time.Time { return clockTime }, fakeSender, m)
	})

	AfterEach(func() {
		cleanupExam(ctx, examNameA, examCRNamespace)
		cleanupExam(ctx, examNameB, examCRNamespace)
		nsA := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examNameA, nnA.Namespace)}}
		_ = k8sClient.Delete(ctx, nsA)
		nsB := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examNameB, nnB.Namespace)}}
		_ = k8sClient.Delete(ctx, nsB)
	})

	It("should reconcile two exams independently through their lifecycles", func() {
		// ---- Step 4: Reconcile exam-a -> Provisioning ----
		By("Reconciling exam-a into Provisioning")
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nnA))
		Expect(err).NotTo(HaveOccurred())

		examA := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nnA, examA)).To(Succeed())
		Expect(examA.Status.Phase).To(Equal(examv1alpha1.ExamPhaseProvisioning))

		nsA := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: examNamespace(examNameA, nnA.Namespace)}, nsA)).To(Succeed())

		// ---- Step 5: Reconcile exam-b -> Provisioning ----
		By("Reconciling exam-b into Provisioning")
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nnB))
		Expect(err).NotTo(HaveOccurred())

		examB := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nnB, examB)).To(Succeed())
		Expect(examB.Status.Phase).To(Equal(examv1alpha1.ExamPhaseProvisioning))

		nsB := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: examNamespace(examNameB, nnB.Namespace)}, nsB)).To(Succeed())

		// ---- Step 6: Verify namespaces are different and deployment counts ----
		By("Verifying namespaces are different")
		Expect(examNamespace(examNameA, nnA.Namespace)).NotTo(Equal(examNamespace(examNameB, nnB.Namespace)))

		By("Verifying exam-a has 2 deployments (2 students)")
		var depsA appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &depsA,
			client.InNamespace(examNamespace(examNameA, nnA.Namespace)),
			client.MatchingLabels{provisioner.LabelExam: examNameA},
		)).To(Succeed())
		Expect(depsA.Items).To(HaveLen(2))

		By("Verifying exam-b has 1 deployment (1 student)")
		var depsB appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &depsB,
			client.InNamespace(examNamespace(examNameB, nnB.Namespace)),
			client.MatchingLabels{provisioner.LabelExam: examNameB},
		)).To(Succeed())
		Expect(depsB.Items).To(HaveLen(1))

		// ---- Step 7: Patch deployments ready for both ----
		By("Patching deployments ready for both exams")
		patchDeploymentsReady(ctx, examNamespace(examNameA, nnA.Namespace), examNameA)
		patchDeploymentsReady(ctx, examNamespace(examNameB, nnB.Namespace), examNameB)

		// ---- Step 8: Reconcile both -> Ready ----
		By("Reconciling exam-a into Ready")
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nnA))
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, nnA, examA)).To(Succeed())
		Expect(examA.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

		By("Reconciling exam-b into Ready")
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nnB))
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, nnB, examB)).To(Succeed())
		Expect(examB.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

		// ---- Step 9-10: Advance clock past exam-a's unlock, reconcile exam-a -> Unlocked ----
		By("Advancing clock past exam-a's unlock but before exam-b's unlock")
		reconciler.Now = func() time.Time { return unlockA.Add(1 * time.Minute) }

		By("Reconciling exam-a into Unlocked")
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nnA))
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, nnA, examA)).To(Succeed())
		Expect(examA.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))

		// ---- Step 11: Reconcile exam-b -> still Ready (not yet unlocked) ----
		By("Reconciling exam-b which should remain Ready")
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nnB))
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, nnB, examB)).To(Succeed())
		Expect(examB.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

		// ---- Step 12: Verify exam-a has ingresses, exam-b does not ----
		By("Verifying exam-a has ingresses")
		var ingressesA networkingv1.IngressList
		Expect(k8sClient.List(ctx, &ingressesA,
			client.InNamespace(examNamespace(examNameA, nnA.Namespace)),
			client.MatchingLabels{provisioner.LabelExam: examNameA},
		)).To(Succeed())
		Expect(ingressesA.Items).To(HaveLen(2), "exam-a should have 2 ingresses (2 students)")

		By("Verifying exam-b has no ingresses")
		var ingressesB networkingv1.IngressList
		Expect(k8sClient.List(ctx, &ingressesB,
			client.InNamespace(examNamespace(examNameB, nnB.Namespace)),
			client.MatchingLabels{provisioner.LabelExam: examNameB},
		)).To(Succeed())
		Expect(ingressesB.Items).To(BeEmpty(), "exam-b should have no ingresses yet")

		// ---- Step 13: Verify metrics are per-exam ----
		By("Verifying phase transition metrics are per-exam")
		// Note: Provisioning→Ready transition is done inside reconcileProvisioning
		// without going through determineDesiredPhase, so only transitions detected
		// by determineDesiredPhase are counted: ""→Provisioning, Ready→Unlocked, etc.

		// Both exams should have "" -> Provisioning
		valA := testutil.ToFloat64(m.PhaseTransitions.WithLabelValues(examNameA, nnA.Namespace, "", "Provisioning"))
		Expect(valA).To(Equal(float64(1)))
		valB := testutil.ToFloat64(m.PhaseTransitions.WithLabelValues(examNameB, nnB.Namespace, "", "Provisioning"))
		Expect(valB).To(Equal(float64(1)))

		// Only exam-a should have Ready -> Unlocked
		valA = testutil.ToFloat64(m.PhaseTransitions.WithLabelValues(examNameA, nnA.Namespace, "Ready", "Unlocked"))
		Expect(valA).To(Equal(float64(1)))
		valB = testutil.ToFloat64(m.PhaseTransitions.WithLabelValues(examNameB, nnB.Namespace, "Ready", "Unlocked"))
		Expect(valB).To(Equal(float64(0)))

		By("Verifying instance count metrics are per-exam")
		totalA := testutil.ToFloat64(m.InstancesTotal.WithLabelValues(examNameA, nnA.Namespace))
		Expect(totalA).To(Equal(float64(2)), "exam-a should have 2 instances")

		totalB := testutil.ToFloat64(m.InstancesTotal.WithLabelValues(examNameB, nnB.Namespace))
		Expect(totalB).To(Equal(float64(1)), "exam-b should have 1 instance")

		Expect(totalA).NotTo(Equal(totalB), "per-exam instance counts should differ")
	})
})

var _ = Describe("Same-exam rapid re-reconciliation", func() {
	var (
		ctx        context.Context
		examName   string
		nn         types.NamespacedName
		unlock     time.Time
		fakeSender *notifier.FakeSender
		reconciler *ExamReconciler
		reg        *prometheus.Registry
		m          *metrics.ExamMetrics
		clockTime  time.Time
	)

	BeforeEach(func() {
		ctx = context.Background()
		examName = uniqueExamName("rapid")
		nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}

		// Unlock at T+60min. provisionBefore=1h, so provisionTime = unlock-1h = baseTime.
		// Clock at T+30min is past provision time but before unlock.
		baseTime := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
		unlock = baseTime.Add(60 * time.Minute) // 13:00

		fakeSender = &notifier.FakeSender{}
		reg = prometheus.NewRegistry()
		m = metrics.NewExamMetrics(reg)

		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
			{ID: "bob", Email: "bob@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		// Clock at T+30min — past provision time, before unlock.
		clockTime = baseTime.Add(30 * time.Minute) // 12:30
		reconciler = newReconciler(func() time.Time { return clockTime }, fakeSender, m)
	})

	AfterEach(func() {
		cleanupExam(ctx, examName, examCRNamespace)
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, nn.Namespace)}}
		_ = k8sClient.Delete(ctx, ns)
	})

	It("should be idempotent when the same exam is reconciled twice at the same clock time", func() {
		// ---- Step 1: Reconcile into Provisioning ----
		By("Reconciling the exam into Provisioning")
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseProvisioning))

		// Patch deployments ready so next reconcile can reach Ready.
		By("Patching deployments ready")
		patchDeploymentsReady(ctx, examNamespace(examName, nn.Namespace), examName)

		// Reconcile once to reach Ready.
		By("Reconciling into Ready")
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

		// ---- Step 2: Snapshot state, then reconcile twice rapidly at the same clock time ----
		By("Capturing state snapshot before rapid re-reconciliation in Ready phase")
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		phaseBeforeReady := exam.Status.Phase
		conditionsCountBeforeReady := len(exam.Status.Conditions)
		resourceVersionBeforeFirstReady := exam.ResourceVersion

		// Count deployments before.
		var depsBefore appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &depsBefore,
			client.InNamespace(examNamespace(examName, nn.Namespace)),
			client.MatchingLabels{provisioner.LabelExam: examName},
		)).To(Succeed())
		deploymentCountBefore := len(depsBefore.Items)

		// Count services before.
		var svcsBefore corev1.ServiceList
		Expect(k8sClient.List(ctx, &svcsBefore,
			client.InNamespace(examNamespace(examName, nn.Namespace)),
			client.MatchingLabels{provisioner.LabelExam: examName},
		)).To(Succeed())
		serviceCountBefore := len(svcsBefore.Items)

		By("Reconciling the same exam a second time at the same clock time (Ready phase)")
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		By("Reconciling the same exam a third time at the same clock time (Ready phase)")
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		// ---- Step 3: Verify idempotency — same phase, same conditions, no extra resources ----
		By("Verifying phase is unchanged after rapid re-reconciliation")
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(exam.Status.Phase).To(Equal(phaseBeforeReady))

		By("Verifying no duplicate conditions were added")
		Expect(len(exam.Status.Conditions)).To(Equal(conditionsCountBeforeReady),
			"condition count should not change after idempotent reconcile")

		// Verify no duplicate condition types.
		conditionTypes := map[string]int{}
		for _, c := range exam.Status.Conditions {
			conditionTypes[c.Type]++
		}
		for ct, count := range conditionTypes {
			Expect(count).To(Equal(1), "condition %q appears %d times, expected 1", ct, count)
		}

		By("Verifying no extra deployments were created")
		var depsAfter appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &depsAfter,
			client.InNamespace(examNamespace(examName, nn.Namespace)),
			client.MatchingLabels{provisioner.LabelExam: examName},
		)).To(Succeed())
		Expect(len(depsAfter.Items)).To(Equal(deploymentCountBefore))

		By("Verifying no extra services were created")
		var svcsAfter corev1.ServiceList
		Expect(k8sClient.List(ctx, &svcsAfter,
			client.InNamespace(examNamespace(examName, nn.Namespace)),
			client.MatchingLabels{provisioner.LabelExam: examName},
		)).To(Succeed())
		Expect(len(svcsAfter.Items)).To(Equal(serviceCountBefore))

		// ---- Step 4: Advance clock past unlock, reconcile twice rapidly ----
		By("Advancing clock past unlock time")
		reconciler.Now = func() time.Time { return unlock.Add(1 * time.Minute) }

		By("First reconcile after unlock — should transition to Unlocked")
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))

		// Capture state after unlock.
		conditionsCountAfterUnlock := len(exam.Status.Conditions)
		_ = resourceVersionBeforeFirstReady // referenced above for clarity

		By("Second reconcile after unlock — should remain Unlocked (idempotent)")
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		// ---- Step 5: Verify phase transition happened exactly once ----
		By("Verifying phase is still Unlocked after second reconcile")
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))

		By("Verifying no duplicate conditions after unlock re-reconciliation")
		Expect(len(exam.Status.Conditions)).To(Equal(conditionsCountAfterUnlock),
			"condition count should not change after idempotent reconcile post-unlock")

		conditionTypes = map[string]int{}
		for _, c := range exam.Status.Conditions {
			conditionTypes[c.Type]++
		}
		for ct, count := range conditionTypes {
			Expect(count).To(Equal(1), "condition %q appears %d times after unlock, expected 1", ct, count)
		}

		By("Verifying ingresses were created exactly once (2 students)")
		var ingresses networkingv1.IngressList
		Expect(k8sClient.List(ctx, &ingresses,
			client.InNamespace(examNamespace(examName, nn.Namespace)),
			client.MatchingLabels{provisioner.LabelExam: examName},
		)).To(Succeed())
		Expect(ingresses.Items).To(HaveLen(2), "should have exactly 2 ingresses (one per student)")

		By("Verifying Ready->Unlocked phase transition metric counted exactly once")
		val := testutil.ToFloat64(m.PhaseTransitions.WithLabelValues(examName, nn.Namespace, "Ready", "Unlocked"))
		Expect(val).To(Equal(float64(1)), "Ready->Unlocked transition should be counted exactly once")
	})
})
