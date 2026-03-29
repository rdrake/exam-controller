# Metrics & Grafana Dashboard Design

**Date:** 2026-03-29
**Goal:** Give instructors a single Grafana dashboard that answers "is everything working?" across pre-exam, during-exam, and post-exam phases.

## Existing metrics (no changes)

| Metric | Type | Labels |
|---|---|---|
| `exam_reconcile_duration_seconds` | Histogram | — |
| `exam_reconcile_errors_total` | Counter | — |
| `exam_phase_transitions_total` | Counter | exam, namespace, from, to |
| `exam_instances_total` | Gauge | exam, namespace |
| `exam_instances_healthy` | Gauge | exam, namespace |
| `exam_instances_failed` | Gauge | exam, namespace |
| `exam_emails_sent_total` | Counter | exam, namespace |
| `exam_emails_failed_total` | Counter | exam, namespace |
| `exam_dryrun_passed` | Gauge | exam, namespace |
| `exam_dryrun_failed` | Gauge | exam, namespace |
| `exam_seconds_until_unlock` | Gauge | exam, namespace |
| `exam_seconds_until_lock` | Gauge | exam, namespace |

## New metrics (3)

### `exam_provision_duration_seconds` — Histogram
- **Labels:** exam, namespace, student
- **Buckets:** 5s, 10s, 30s, 60s, 120s, 300s
- **Recorded:** once per student when their instance transitions to Provisioned
- **Purpose:** surface slow image pulls or scheduling issues

### `exam_phase_duration_seconds` — Gauge
- **Labels:** exam, namespace, phase
- **Updated:** each reconcile, set to `time.Since(phaseEntryTime).Seconds()` for the current phase
- **Purpose:** detect stuck phases (e.g., Provisioning > 10 min)

### `exam_spare_swaps_total` — Counter
- **Labels:** exam, namespace
- **Incremented:** when a spare instance replaces a failed student instance
- **Purpose:** confirm auto-healing happened; >0 during exam is notable

## Dashboard layout

Single `$exam` + `$namespace` variable selector. Assumes one exam at a time.

### Row 1 — Status at a glance

| Panel | Viz | Query |
|---|---|---|
| Phase | Stat (value mapping: green=Ready/Unlocked, yellow=Provisioning, red=TearingDown) | Derived from `exam_phase_duration_seconds` (whichever phase label has value > 0) |
| Healthy / Total | Stat | `exam_instances_healthy` / `exam_instances_total` |
| Emails Sent | Stat | `exam_emails_sent_total` |
| Dry Run | Stat | `exam_dryrun_passed` / (`exam_dryrun_passed` + `exam_dryrun_failed`) |
| Time Until Unlock | Stat (countdown format) | `exam_seconds_until_unlock` |
| Time Until Lock | Stat (countdown format) | `exam_seconds_until_lock` |

### Row 2 — Provisioning & health

| Panel | Viz | Query |
|---|---|---|
| Instance health over time | Time series (stacked) | `exam_instances_healthy`, `exam_instances_failed`, `exam_instances_total` |
| Provisioning latency | Heatmap | `exam_provision_duration_seconds` |
| Phase timeline | State timeline | `exam_phase_duration_seconds` by phase label |

### Row 3 — Operational

| Panel | Viz | Query |
|---|---|---|
| Reconcile latency | Time series (p50/p99) | `histogram_quantile(0.5, ...)` / `histogram_quantile(0.99, ...)` over `exam_reconcile_duration_seconds` |
| Reconcile errors | Time series | `rate(exam_reconcile_errors_total[5m])` |
| Email failures | Stat | `exam_emails_failed_total` |
| Spare swaps | Stat | `exam_spare_swaps_total` |

## Implementation plan

1. **Metrics code** — add 3 new metrics to `internal/metrics/metrics.go`, update `CleanupExam`
2. **Reconciler wiring** — record provision duration on student phase transition, update phase duration each reconcile, increment spare swaps counter
3. **Tests** — unit tests for new metrics in `internal/metrics/metrics_test.go`, reconciler tests for recording
4. **Grafana dashboard JSON** — `charts/exam-controller/dashboards/exam-overview.json`
5. **Helm integration** — ConfigMap template + `grafana.dashboard.enabled` value
6. **Helm verify** — `make helm-verify` passes
