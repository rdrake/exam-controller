package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	if m.ReconcileDuration == nil {
		t.Error("ReconcileDuration not initialized")
	}
	if m.ReconcileErrors == nil {
		t.Error("ReconcileErrors not initialized")
	}
	if m.PhaseTransitions == nil {
		t.Error("PhaseTransitions not initialized")
	}
	if m.InstancesTotal == nil {
		t.Error("InstancesTotal not initialized")
	}
	if m.InstancesHealthy == nil {
		t.Error("InstancesHealthy not initialized")
	}
	if m.InstancesFailed == nil {
		t.Error("InstancesFailed not initialized")
	}
	if m.EmailsSent == nil {
		t.Error("EmailsSent not initialized")
	}
	if m.EmailsFailed == nil {
		t.Error("EmailsFailed not initialized")
	}
	if m.SecondsUntilUnlock == nil {
		t.Error("SecondsUntilUnlock not initialized")
	}
	if m.SecondsUntilLock == nil {
		t.Error("SecondsUntilLock not initialized")
	}
}

func TestRecordPhaseTransition(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)
	m.PhaseTransitions.WithLabelValues("test-exam", "Pending", "Provisioning").Inc()
	// No panic = success
}
