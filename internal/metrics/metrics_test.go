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

	counter := m.PhaseTransitions.WithLabelValues("exam1", "Pending", "Provisioning")
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
	m.InstancesTotal.WithLabelValues("midterm").Set(10)
	m.InstancesHealthy.WithLabelValues("midterm").Set(8)
	m.InstancesFailed.WithLabelValues("midterm").Set(2)
	m.SecondsUntilUnlock.WithLabelValues("midterm").Set(300)
	m.SecondsUntilLock.WithLabelValues("midterm").Set(600)
	m.PhaseTransitions.WithLabelValues("midterm", "Pending", "Provisioning").Inc()

	// Verify the gauge series exist before cleanup.
	beforeTotal := testutil.CollectAndCount(m.InstancesTotal)
	if beforeTotal == 0 {
		t.Fatal("InstancesTotal should have at least one series before cleanup")
	}

	m.CleanupExam("midterm")

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
}

func TestCleanupExam_NoopForUnknownExam(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	// Should not panic when cleaning up an exam that was never tracked.
	m.CleanupExam("nonexistent")
}

func TestCountdownGauges_SetAndRead(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	m.SecondsUntilUnlock.WithLabelValues("final").Set(120)
	m.SecondsUntilLock.WithLabelValues("final").Set(3600)

	unlock := testutil.ToFloat64(m.SecondsUntilUnlock.WithLabelValues("final"))
	if unlock != 120 {
		t.Errorf("SecondsUntilUnlock = %v, want 120", unlock)
	}

	lock := testutil.ToFloat64(m.SecondsUntilLock.WithLabelValues("final"))
	if lock != 3600 {
		t.Errorf("SecondsUntilLock = %v, want 3600", lock)
	}
}

func TestEmailCounters(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	m.EmailsSent.WithLabelValues("quiz1").Inc()
	m.EmailsSent.WithLabelValues("quiz1").Inc()
	m.EmailsFailed.WithLabelValues("quiz1").Inc()

	sent := testutil.ToFloat64(m.EmailsSent.WithLabelValues("quiz1"))
	if sent != 2 {
		t.Errorf("EmailsSent = %v, want 2", sent)
	}

	failed := testutil.ToFloat64(m.EmailsFailed.WithLabelValues("quiz1"))
	if failed != 1 {
		t.Errorf("EmailsFailed = %v, want 1", failed)
	}
}

func TestInstanceGauges(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	m.InstancesTotal.WithLabelValues("lab1").Set(25)
	m.InstancesHealthy.WithLabelValues("lab1").Set(23)
	m.InstancesFailed.WithLabelValues("lab1").Set(2)

	total := testutil.ToFloat64(m.InstancesTotal.WithLabelValues("lab1"))
	if total != 25 {
		t.Errorf("InstancesTotal = %v, want 25", total)
	}

	healthy := testutil.ToFloat64(m.InstancesHealthy.WithLabelValues("lab1"))
	if healthy != 23 {
		t.Errorf("InstancesHealthy = %v, want 23", healthy)
	}

	failed := testutil.ToFloat64(m.InstancesFailed.WithLabelValues("lab1"))
	if failed != 2 {
		t.Errorf("InstancesFailed = %v, want 2", failed)
	}
}
