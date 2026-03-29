package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNewExamMetrics_AllFieldsInitialized(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	fields := map[string]any{
		"ReconcileDuration":  m.ReconcileDuration,
		"ReconcileErrors":    m.ReconcileErrors,
		"PhaseTransitions":   m.PhaseTransitions,
		"InstancesTotal":     m.InstancesTotal,
		"InstancesHealthy":   m.InstancesHealthy,
		"InstancesFailed":    m.InstancesFailed,
		"EmailsSent":         m.EmailsSent,
		"EmailsFailed":       m.EmailsFailed,
		"DryRunPassed":       m.DryRunPassed,
		"DryRunFailed":       m.DryRunFailed,
		"SecondsUntilUnlock": m.SecondsUntilUnlock,
		"SecondsUntilLock":   m.SecondsUntilLock,
		"ProvisionDuration":  m.ProvisionDuration,
		"PhaseDuration":      m.PhaseDuration,
		"SpareSwaps":         m.SpareSwaps,
	}
	for name, field := range fields {
		if field == nil {
			t.Errorf("%s is nil, expected non-nil", name)
		}
	}
}

func TestRecordPhaseTransition_IncrementsCounter(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	counter := m.PhaseTransitions.WithLabelValues("exam1", "exam-system", "Pending", "Provisioning")
	counter.Inc()

	val := testutil.ToFloat64(counter)
	if val != 1 {
		t.Errorf("PhaseTransitions counter = %v, want 1", val)
	}
}

func TestReconcileDuration_ObservesHistogram(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	m.ReconcileDuration.Observe(0.5)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	var found bool
	for _, mf := range mfs {
		if mf.GetName() == "exam_reconcile_duration_seconds" {
			found = true
			for _, metric := range mf.GetMetric() {
				h := metric.GetHistogram()
				if h == nil {
					t.Fatal("expected histogram metric, got nil")
				}
				if h.GetSampleCount() != 1 {
					t.Errorf("histogram sample_count = %d, want 1", h.GetSampleCount())
				}
				if h.GetSampleSum() != 0.5 {
					t.Errorf("histogram sample_sum = %v, want 0.5", h.GetSampleSum())
				}
			}
		}
	}
	if !found {
		t.Error("exam_reconcile_duration_seconds metric family not found")
	}
}

func TestCleanupExam_RemovesLabelSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	// Set values for "midterm" exam across several metrics.
	m.InstancesTotal.WithLabelValues("midterm", "exam-system").Set(10)
	m.InstancesHealthy.WithLabelValues("midterm", "exam-system").Set(8)
	m.InstancesFailed.WithLabelValues("midterm", "exam-system").Set(2)
	m.SecondsUntilUnlock.WithLabelValues("midterm", "exam-system").Set(300)
	m.SecondsUntilLock.WithLabelValues("midterm", "exam-system").Set(600)
	m.PhaseTransitions.WithLabelValues("midterm", "exam-system", "Pending", "Provisioning").Inc()
	m.ProvisionDuration.WithLabelValues("midterm", "exam-system", "alice").Observe(42)
	m.PhaseDuration.WithLabelValues("midterm", "exam-system", "Provisioning").Set(15)
	m.SpareSwaps.WithLabelValues("midterm", "exam-system").Inc()

	// Verify the gauge series exist before cleanup.
	beforeTotal := testutil.CollectAndCount(m.InstancesTotal)
	if beforeTotal == 0 {
		t.Fatal("InstancesTotal should have at least one series before cleanup")
	}

	m.CleanupExam("midterm", "exam-system")

	// After cleanup the series for "midterm" should be removed.
	afterTotal := testutil.CollectAndCount(m.InstancesTotal)
	if afterTotal != 0 {
		t.Errorf("InstancesTotal series count after cleanup = %d, want 0", afterTotal)
	}
	afterHealthy := testutil.CollectAndCount(m.InstancesHealthy)
	if afterHealthy != 0 {
		t.Errorf("InstancesHealthy series count after cleanup = %d, want 0", afterHealthy)
	}
	afterFailed := testutil.CollectAndCount(m.InstancesFailed)
	if afterFailed != 0 {
		t.Errorf("InstancesFailed series count after cleanup = %d, want 0", afterFailed)
	}
	afterUnlock := testutil.CollectAndCount(m.SecondsUntilUnlock)
	if afterUnlock != 0 {
		t.Errorf("SecondsUntilUnlock series count after cleanup = %d, want 0", afterUnlock)
	}
	afterLock := testutil.CollectAndCount(m.SecondsUntilLock)
	if afterLock != 0 {
		t.Errorf("SecondsUntilLock series count after cleanup = %d, want 0", afterLock)
	}
	afterPhase := testutil.CollectAndCount(m.PhaseTransitions)
	if afterPhase != 0 {
		t.Errorf("PhaseTransitions series count after cleanup = %d, want 0", afterPhase)
	}
	afterProvision := testutil.CollectAndCount(m.ProvisionDuration)
	if afterProvision != 0 {
		t.Errorf("ProvisionDuration series count after cleanup = %d, want 0", afterProvision)
	}
	afterPhaseDur := testutil.CollectAndCount(m.PhaseDuration)
	if afterPhaseDur != 0 {
		t.Errorf("PhaseDuration series count after cleanup = %d, want 0", afterPhaseDur)
	}
	afterSwaps := testutil.CollectAndCount(m.SpareSwaps)
	if afterSwaps != 0 {
		t.Errorf("SpareSwaps series count after cleanup = %d, want 0", afterSwaps)
	}
}

func TestCleanupExam_NoopForUnknownExam(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	// Should not panic when cleaning up an exam that was never tracked.
	m.CleanupExam("nonexistent", "exam-system")
}

func TestCountdownGauges_SetAndRead(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	m.SecondsUntilUnlock.WithLabelValues("final", "exam-system").Set(120)
	m.SecondsUntilLock.WithLabelValues("final", "exam-system").Set(3600)

	unlock := testutil.ToFloat64(m.SecondsUntilUnlock.WithLabelValues("final", "exam-system"))
	if unlock != 120 {
		t.Errorf("SecondsUntilUnlock = %v, want 120", unlock)
	}

	lock := testutil.ToFloat64(m.SecondsUntilLock.WithLabelValues("final", "exam-system"))
	if lock != 3600 {
		t.Errorf("SecondsUntilLock = %v, want 3600", lock)
	}
}

func TestEmailCounters(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	m.EmailsSent.WithLabelValues("quiz1", "exam-system").Inc()
	m.EmailsSent.WithLabelValues("quiz1", "exam-system").Inc()
	m.EmailsFailed.WithLabelValues("quiz1", "exam-system").Inc()

	sent := testutil.ToFloat64(m.EmailsSent.WithLabelValues("quiz1", "exam-system"))
	if sent != 2 {
		t.Errorf("EmailsSent = %v, want 2", sent)
	}

	failed := testutil.ToFloat64(m.EmailsFailed.WithLabelValues("quiz1", "exam-system"))
	if failed != 1 {
		t.Errorf("EmailsFailed = %v, want 1", failed)
	}
}

func TestInstanceGauges(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	m.InstancesTotal.WithLabelValues("lab1", "exam-system").Set(25)
	m.InstancesHealthy.WithLabelValues("lab1", "exam-system").Set(23)
	m.InstancesFailed.WithLabelValues("lab1", "exam-system").Set(2)

	total := testutil.ToFloat64(m.InstancesTotal.WithLabelValues("lab1", "exam-system"))
	if total != 25 {
		t.Errorf("InstancesTotal = %v, want 25", total)
	}

	healthy := testutil.ToFloat64(m.InstancesHealthy.WithLabelValues("lab1", "exam-system"))
	if healthy != 23 {
		t.Errorf("InstancesHealthy = %v, want 23", healthy)
	}

	failed := testutil.ToFloat64(m.InstancesFailed.WithLabelValues("lab1", "exam-system"))
	if failed != 2 {
		t.Errorf("InstancesFailed = %v, want 2", failed)
	}
}

func TestProvisionDuration_ObservesHistogram(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	m.ProvisionDuration.WithLabelValues("exam1", "exam-system", "alice").Observe(25.0)
	m.ProvisionDuration.WithLabelValues("exam1", "exam-system", "bob").Observe(45.0)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	var found bool
	for _, mf := range mfs {
		if mf.GetName() == "exam_provision_duration_seconds" {
			found = true
			metrics := mf.GetMetric()
			if len(metrics) != 2 {
				t.Errorf("expected 2 histogram series, got %d", len(metrics))
			}
			for _, metric := range metrics {
				h := metric.GetHistogram()
				if h == nil {
					t.Fatal("expected histogram metric, got nil")
				}
				if h.GetSampleCount() != 1 {
					t.Errorf("histogram sample_count = %d, want 1", h.GetSampleCount())
				}
			}
		}
	}
	if !found {
		t.Error("exam_provision_duration_seconds metric family not found")
	}
}

func TestProvisionDuration_Buckets(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	m.ProvisionDuration.WithLabelValues("exam1", "exam-system", "alice").Observe(7.0)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	wantBuckets := []float64{5, 10, 30, 60, 120, 300}
	for _, mf := range mfs {
		if mf.GetName() == "exam_provision_duration_seconds" {
			for _, metric := range mf.GetMetric() {
				h := metric.GetHistogram()
				buckets := h.GetBucket()
				if len(buckets) != len(wantBuckets) {
					t.Fatalf("bucket count = %d, want %d", len(buckets), len(wantBuckets))
				}
				for i, b := range buckets {
					if b.GetUpperBound() != wantBuckets[i] {
						t.Errorf("bucket[%d] upper bound = %v, want %v", i, b.GetUpperBound(), wantBuckets[i])
					}
				}
			}
		}
	}
}

func TestPhaseDuration_SetAndRead(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	m.PhaseDuration.WithLabelValues("final", "exam-system", "Provisioning").Set(45.5)
	m.PhaseDuration.WithLabelValues("final", "exam-system", "Running").Set(120.0)

	prov := testutil.ToFloat64(m.PhaseDuration.WithLabelValues("final", "exam-system", "Provisioning"))
	if prov != 45.5 {
		t.Errorf("PhaseDuration Provisioning = %v, want 45.5", prov)
	}

	running := testutil.ToFloat64(m.PhaseDuration.WithLabelValues("final", "exam-system", "Running"))
	if running != 120.0 {
		t.Errorf("PhaseDuration Running = %v, want 120.0", running)
	}
}

func TestSpareSwaps_IncrementsCounter(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	m.SpareSwaps.WithLabelValues("exam1", "exam-system").Inc()
	m.SpareSwaps.WithLabelValues("exam1", "exam-system").Inc()
	m.SpareSwaps.WithLabelValues("exam1", "exam-system").Inc()

	val := testutil.ToFloat64(m.SpareSwaps.WithLabelValues("exam1", "exam-system"))
	if val != 3 {
		t.Errorf("SpareSwaps = %v, want 3", val)
	}
}
