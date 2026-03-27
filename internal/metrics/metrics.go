package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

type ExamMetrics struct {
	ReconcileDuration  prometheus.Histogram
	ReconcileErrors    prometheus.Counter
	PhaseTransitions   *prometheus.CounterVec
	InstancesTotal     *prometheus.GaugeVec
	InstancesHealthy   *prometheus.GaugeVec
	InstancesFailed    *prometheus.GaugeVec
	EmailsSent         *prometheus.CounterVec
	EmailsFailed       *prometheus.CounterVec
	DryRunPassed       *prometheus.GaugeVec
	DryRunFailed       *prometheus.GaugeVec
	SecondsUntilUnlock *prometheus.GaugeVec
	SecondsUntilLock   *prometheus.GaugeVec
}

func NewExamMetrics(reg prometheus.Registerer) *ExamMetrics {
	m := &ExamMetrics{
		ReconcileDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "exam_reconcile_duration_seconds",
			Help:    "Time spent per reconcile loop.",
			Buckets: prometheus.DefBuckets,
		}),
		ReconcileErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "exam_reconcile_errors_total",
			Help: "Total reconcile failures.",
		}),
		PhaseTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "exam_phase_transitions_total",
			Help: "Phase changes labeled by exam, from, and to.",
		}, []string{"exam", "from", "to"}),
		InstancesTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_instances_total",
			Help: "Total instances (students + spares).",
		}, []string{"exam"}),
		InstancesHealthy: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_instances_healthy",
			Help: "Instances passing health checks.",
		}, []string{"exam"}),
		InstancesFailed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_instances_failed",
			Help: "Instances in failed state.",
		}, []string{"exam"}),
		EmailsSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "exam_emails_sent_total",
			Help: "Emails successfully sent.",
		}, []string{"exam"}),
		EmailsFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "exam_emails_failed_total",
			Help: "Email delivery failures.",
		}, []string{"exam"}),
		DryRunPassed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_dryrun_passed",
			Help: "Dry run pass count.",
		}, []string{"exam"}),
		DryRunFailed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_dryrun_failed",
			Help: "Dry run fail count.",
		}, []string{"exam"}),
		SecondsUntilUnlock: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_seconds_until_unlock",
			Help: "Countdown to unlock (0 after unlock).",
		}, []string{"exam"}),
		SecondsUntilLock: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_seconds_until_lock",
			Help: "Countdown to lock (0 after lock).",
		}, []string{"exam"}),
	}
	reg.MustRegister(
		m.ReconcileDuration, m.ReconcileErrors, m.PhaseTransitions,
		m.InstancesTotal, m.InstancesHealthy, m.InstancesFailed,
		m.EmailsSent, m.EmailsFailed,
		m.DryRunPassed, m.DryRunFailed,
		m.SecondsUntilUnlock, m.SecondsUntilLock,
	)
	return m
}
