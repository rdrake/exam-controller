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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/metrics"
	"github.com/rdrake/exam-controller/internal/notifier"
)

var _ = Describe("Metrics", func() {
	var (
		ctx        context.Context
		examName   string
		nn         types.NamespacedName
		unlock     time.Time
		fakeSender *notifier.FakeSender
	)

	BeforeEach(func() {
		ctx = context.Background()
		examName = uniqueExamName("metrics")
		nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
		unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
		fakeSender = &notifier.FakeSender{}
	})

	AfterEach(func() {
		cleanupExam(ctx, examName, examCRNamespace)
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
		_ = k8sClient.Delete(ctx, ns)
	})

	It("updates phase transition counter on phase change", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reg := prometheus.NewRegistry()
		m := metrics.NewExamMetrics(reg)

		// Clock is after provision time but before unlock -> should transition "" -> Provisioning
		clockTime := unlock.Add(-30 * time.Minute)
		reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, m)

		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		val := testutil.ToFloat64(m.PhaseTransitions.WithLabelValues(examName, examCRNamespace, "", "Provisioning"))
		Expect(val).To(Equal(float64(1)))
	})

	It("sets countdown gauges", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reg := prometheus.NewRegistry()
		m := metrics.NewExamMetrics(reg)

		// Drive to Ready phase so countdown gauges are set with exam not yet unlocked.
		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, m)
		_ = reconciler

		unlockGauge := testutil.ToFloat64(m.SecondsUntilUnlock.WithLabelValues(examName, examCRNamespace))
		Expect(unlockGauge).To(BeNumerically(">", 0), "SecondsUntilUnlock should be > 0 before unlock time")
	})

	It("updates instance counts", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
			{ID: "bob", Email: "bob@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 1)
		preseedSlugs(ctx, nn)

		reg := prometheus.NewRegistry()
		m := metrics.NewExamMetrics(reg)

		// Drive to Ready phase — all deployments patched healthy.
		driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, m)

		total := testutil.ToFloat64(m.InstancesTotal.WithLabelValues(examName, examCRNamespace))
		Expect(total).To(Equal(float64(3)), "InstancesTotal should be 3 (2 students + 1 spare)")

		healthy := testutil.ToFloat64(m.InstancesHealthy.WithLabelValues(examName, examCRNamespace))
		Expect(healthy).To(Equal(float64(3)), "InstancesHealthy should be 3")

		failed := testutil.ToFloat64(m.InstancesFailed.WithLabelValues(examName, examCRNamespace))
		Expect(failed).To(Equal(float64(0)), "InstancesFailed should be 0")
	})

	It("cleans up metrics on teardown", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reg := prometheus.NewRegistry()
		m := metrics.NewExamMetrics(reg)

		// Drive to Provisioning so resources exist and metrics are populated.
		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseProvisioning, unlock, fakeSender, m)

		// Verify metrics exist before deletion.
		Expect(testutil.CollectAndCount(m.InstancesTotal)).To(BeNumerically(">", 0))

		// Delete the Exam object to trigger finalizer-based teardown.
		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(k8sClient.Delete(ctx, exam)).To(Succeed())

		// Reconcile triggers finalizer cleanup which calls CleanupExam.
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		// After cleanup, label series for this exam should be removed.
		Expect(testutil.CollectAndCount(m.InstancesTotal)).To(Equal(0))
		Expect(testutil.CollectAndCount(m.InstancesHealthy)).To(Equal(0))
		Expect(testutil.CollectAndCount(m.InstancesFailed)).To(Equal(0))
		Expect(testutil.CollectAndCount(m.SecondsUntilUnlock)).To(Equal(0))
		Expect(testutil.CollectAndCount(m.SecondsUntilLock)).To(Equal(0))
		Expect(testutil.CollectAndCount(m.PhaseTransitions)).To(Equal(0))
	})

	It("records reconcile duration", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reg := prometheus.NewRegistry()
		m := metrics.NewExamMetrics(reg)

		clockTime := unlock.Add(-30 * time.Minute)
		reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, m)

		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		// Gather metrics from the registry and verify the histogram has at least one observation.
		mfs, err := reg.Gather()
		Expect(err).NotTo(HaveOccurred())

		var found bool
		for _, mf := range mfs {
			if mf.GetName() == "exam_reconcile_duration_seconds" {
				found = true
				for _, metric := range mf.GetMetric() {
					h := metric.GetHistogram()
					Expect(h).NotTo(BeNil())
					Expect(h.GetSampleCount()).To(BeNumerically(">=", uint64(1)))
				}
			}
		}
		Expect(found).To(BeTrue(), "exam_reconcile_duration_seconds metric family not found")
	})

	It("sets PhaseEntryTime on phase transition", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reg := prometheus.NewRegistry()
		m := metrics.NewExamMetrics(reg)

		// Clock is after provision time but before unlock -> "" -> Provisioning
		clockTime := unlock.Add(-30 * time.Minute)
		reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, m)

		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(exam.Status.PhaseEntryTime).NotTo(BeNil(), "PhaseEntryTime should be set after phase transition")
		Expect(exam.Status.PhaseEntryTime.Time).To(BeTemporally("~", clockTime, time.Second))
	})

	It("updates PhaseEntryTime on subsequent phase transitions", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reg := prometheus.NewRegistry()
		m := metrics.NewExamMetrics(reg)

		// Drive to Ready (Provisioning -> Ready transition happens inside reconcileProvisioning)
		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, m)

		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))
		Expect(exam.Status.PhaseEntryTime).NotTo(BeNil(), "PhaseEntryTime should be set after Ready transition")
		readyEntryTime := exam.Status.PhaseEntryTime.Time

		// Now transition to Unlocked
		reconciler.Now = func() time.Time { return unlock.Add(5 * time.Minute) }
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))
		Expect(exam.Status.PhaseEntryTime).NotTo(BeNil())
		Expect(exam.Status.PhaseEntryTime.Time).NotTo(Equal(readyEntryTime),
			"PhaseEntryTime should update on each transition")
	})

	It("reports PhaseDuration gauge", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reg := prometheus.NewRegistry()
		m := metrics.NewExamMetrics(reg)

		// First reconcile: enter Provisioning
		clockTime := unlock.Add(-30 * time.Minute)
		reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, m)
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		// Second reconcile 60 seconds later: still Provisioning, PhaseDuration should reflect elapsed time
		clockTime2 := clockTime.Add(60 * time.Second)
		reconciler.Now = func() time.Time { return clockTime2 }
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		val := testutil.ToFloat64(m.PhaseDuration.WithLabelValues(examName, examCRNamespace, "Provisioning"))
		Expect(val).To(BeNumerically(">=", 60), "PhaseDuration should reflect time in current phase")
	})

	It("observes ProvisionDuration for students", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
			{ID: "bob", Email: "bob@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reg := prometheus.NewRegistry()
		m := metrics.NewExamMetrics(reg)

		// Clock is 30 minutes after provision time (which is unlock - 1h)
		// So provision duration should be about 30 minutes (1800 seconds)
		clockTime := unlock.Add(-30 * time.Minute)
		reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, m)

		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		// Gather metrics to verify histogram observations
		mfs, err := reg.Gather()
		Expect(err).NotTo(HaveOccurred())

		var found bool
		for _, mf := range mfs {
			if mf.GetName() == "exam_provision_duration_seconds" {
				found = true
				// Should have observations for both students
				Expect(mf.GetMetric()).To(HaveLen(2))
				for _, metric := range mf.GetMetric() {
					h := metric.GetHistogram()
					Expect(h).NotTo(BeNil())
					Expect(h.GetSampleCount()).To(Equal(uint64(1)))
					// Value should be approximately 1800 seconds (30 min)
					Expect(h.GetSampleSum()).To(BeNumerically("~", 1800, 1))
				}
			}
		}
		Expect(found).To(BeTrue(), "exam_provision_duration_seconds metric family not found")
	})

	It("observes ProvisionDuration for spares", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 1)
		preseedSlugs(ctx, nn)

		reg := prometheus.NewRegistry()
		m := metrics.NewExamMetrics(reg)

		clockTime := unlock.Add(-30 * time.Minute)
		reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, m)

		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		// Gather metrics: should have 2 observations (1 student + 1 spare)
		mfs, err := reg.Gather()
		Expect(err).NotTo(HaveOccurred())

		var found bool
		for _, mf := range mfs {
			if mf.GetName() == "exam_provision_duration_seconds" {
				found = true
				// 1 student + 1 spare = 2 label sets
				Expect(mf.GetMetric()).To(HaveLen(2))
			}
		}
		Expect(found).To(BeTrue(), "exam_provision_duration_seconds metric family not found")
	})

	It("records Provisioning->Ready phase transition counter", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reg := prometheus.NewRegistry()
		m := metrics.NewExamMetrics(reg)

		// Drive to Ready — this should record both "" -> Provisioning and Provisioning -> Ready transitions
		driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, m)

		provToReady := testutil.ToFloat64(m.PhaseTransitions.WithLabelValues(
			examName, examCRNamespace, "Provisioning", "Ready"))
		Expect(provToReady).To(Equal(float64(1)),
			"Provisioning->Ready phase transition should be counted")
	})

	Describe("re-creation with same name", func() {
		const fixedName = "metrics-recreate"
		var (
			fixedNN     types.NamespacedName
			reg         *prometheus.Registry
			m           *metrics.ExamMetrics
			fixedSender *notifier.FakeSender
		)

		BeforeEach(func() {
			fixedNN = types.NamespacedName{Name: fixedName, Namespace: examCRNamespace}
			reg = prometheus.NewRegistry()
			m = metrics.NewExamMetrics(reg)
			fixedSender = &notifier.FakeSender{}
		})

		AfterEach(func() {
			cleanupExam(ctx, fixedName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(fixedName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("does not carry over stale metrics after CleanupExam and re-creation", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}

			// --- First exam lifecycle ---
			createExamCR(ctx, fixedName, unlock, students, 0)
			preseedSlugs(ctx, fixedNN)

			// Drive to Ready: populates InstancesTotal=2, InstancesHealthy=2
			driveToPhase(ctx, fixedNN, examv1alpha1.ExamPhaseReady, unlock, fixedSender, m)

			// Verify metrics are populated after reaching Ready
			healthy1 := testutil.ToFloat64(m.InstancesHealthy.WithLabelValues(fixedName, examCRNamespace))
			Expect(healthy1).To(Equal(float64(2)), "InstancesHealthy should be 2 after first exam reaches Ready")
			total1 := testutil.ToFloat64(m.InstancesTotal.WithLabelValues(fixedName, examCRNamespace))
			Expect(total1).To(Equal(float64(2)), "InstancesTotal should be 2 after first exam reaches Ready")
			transitions1 := testutil.ToFloat64(m.PhaseTransitions.WithLabelValues(fixedName, examCRNamespace, "", "Provisioning"))
			Expect(transitions1).To(BeNumerically(">", 0), "PhaseTransitions should record at least one transition")

			// Call CleanupExam directly (simulating teardown metrics cleanup)
			m.CleanupExam(fixedName, examCRNamespace)

			// Verify gauge metrics are reset after cleanup
			Expect(testutil.CollectAndCount(m.InstancesTotal)).To(Equal(0),
				"InstancesTotal series should be removed after CleanupExam")
			Expect(testutil.CollectAndCount(m.InstancesHealthy)).To(Equal(0),
				"InstancesHealthy series should be removed after CleanupExam")
			Expect(testutil.CollectAndCount(m.InstancesFailed)).To(Equal(0),
				"InstancesFailed series should be removed after CleanupExam")

			// --- Simulate re-creation: setting metrics for the same exam name ---
			// After cleanup, WithLabelValues should return a fresh zero-value gauge
			healthyAfterCleanup := testutil.ToFloat64(m.InstancesHealthy.WithLabelValues(fixedName, examCRNamespace))
			Expect(healthyAfterCleanup).To(Equal(float64(0)),
				"InstancesHealthy should be 0 after cleanup, not carried over")

			totalAfterCleanup := testutil.ToFloat64(m.InstancesTotal.WithLabelValues(fixedName, examCRNamespace))
			Expect(totalAfterCleanup).To(Equal(float64(0)),
				"InstancesTotal should be 0 after cleanup, not carried over")

			// Setting new values should work correctly (no stale state)
			m.InstancesHealthy.WithLabelValues(fixedName, examCRNamespace).Set(3)
			m.InstancesTotal.WithLabelValues(fixedName, examCRNamespace).Set(3)
			Expect(testutil.ToFloat64(m.InstancesHealthy.WithLabelValues(fixedName, examCRNamespace))).To(Equal(float64(3)),
				"InstancesHealthy should reflect newly set value, not old value of 2")
			Expect(testutil.ToFloat64(m.InstancesTotal.WithLabelValues(fixedName, examCRNamespace))).To(Equal(float64(3)),
				"InstancesTotal should reflect newly set value, not old value of 2")
		})
	})
})
