# Hubble Student Activity Monitoring

**Date:** 2026-04-03
**Status:** Implementing

## Problem

The exam controller tracks instance health (alive or dead) but not instance
activity (is a student working?). Operators and instructors need to see which
instances are active during an exam without adding instrumentation to the exam
applications themselves.

A secondary goal is academic integrity: verifying that each instance receives
connections from a single student, not multiple collaborators. This requires
client IP visibility.

## Investigation Findings

### Client IP Preservation

Cilium's Envoy ingress uses TPROXY to forward traffic to its proxy. This
preserves the original client IP regardless of `externalTrafficPolicy`. Verified
on the staging cluster:

- `X-Forwarded-For` contains the real external client IP (e.g., `99.248.6.242`)
- `X-Envoy-External-Address` confirms the address is external
- The pod's `client_address` (L4 source) is the Envoy proxy pod IP, not the
  client — this is expected and irrelevant since the HTTP headers carry the truth

No configuration changes are needed for client IP visibility. Applications that
read `X-Forwarded-For` already see the real student IP.

### externalTrafficPolicy: Local

Switching the `cilium-ingress` service to `externalTrafficPolicy: Local` is
**not possible** with L2 announcements. Cilium's L2 announcement controller
ignores this setting and announces the service IP from whichever node wins the
lease, regardless of backend placement. Traffic is silently dropped when the
announcing node has no local backend. This is tracked in
[cilium/cilium#27800](https://github.com/cilium/cilium/issues/27800), still open
as of Cilium 1.19.

This limitation does not matter because TPROXY already preserves client IPs
through the Envoy ingress path.

### Hubble Metric Visibility

Hubble's `httpV2` metric collector provides `hubble_http_requests_total` and
`hubble_http_request_duration_seconds`, both filterable by `destination_namespace`
and `destination_workload`. These map directly to exam namespaces and student pod
deployments.

The `source_ip` label in `httpV2` labelsContext shows the Envoy proxy pod IP, not
the real client IP. Hubble's L7 path gets its source from Envoy's access log,
which reflects the proxied connection rather than the original client. We omit
`source_ip` from the labelsContext to avoid high-cardinality labels that add no
value.

## Design

### Hubble Metrics (s1-infra)

Enable the previously commented-out Hubble metric collectors in the Cilium
HelmRelease:

- `dns` — DNS query/response counts per pod
- `drop` — dropped packet counts with reason
- `tcp` — TCP flag distribution (SYN/FIN/RST)
- `flow` — total flow counts by verdict
- `port-distribution` — traffic by destination port
- `icmp` — ICMP message counts
- `httpV2` — HTTP request counts and latency histograms

The `httpV2` collector uses `labelsContext` to expose `source_namespace`,
`source_workload`, `destination_ip`, `destination_namespace`,
`destination_workload`, and `traffic_direction` as Prometheus labels.

A Hubble metrics ServiceMonitor ensures Prometheus scrapes these metrics.

### Grafana Dashboard (exam-controller)

A new collapsed row, "Student Activity (requires Hubble)," added to the existing
exam overview dashboard. Three panels:

1. **Active Instances** (stat) — count of instances with HTTP requests in the
   last five minutes. Answers "how many students are working right now?"
2. **Requests per Instance** (time series) — per-workload request rate over time.
   Shows which instances are active and how active they are.
3. **Request Latency** (heatmap) — HTTP latency distribution across instances.
   Surfaces sluggish instances that may indicate problems.

All panels filter by `destination_namespace="$namespace"` to scope to the
selected exam.

### What This Does Not Cover

- **CR status enrichment** — adding activity status to the Exam custom resource
  would require the controller to query Prometheus or Hubble, adding a runtime
  dependency. The Grafana dashboard provides the same visibility without coupling
  the controller to the monitoring stack.
- **IP-based access control** — locking instances to specific client IPs (e.g.,
  campus subnets) is feasible through CiliumNetworkPolicy L7 rules or
  application-level middleware using `X-Forwarded-For`. This is a separate design
  effort.
- **Per-student source IP tracking** — exposing which IPs connect to each
  instance for integrity auditing. The data is available in `X-Forwarded-For` at
  the application layer. A future sidecar or application-level logging solution
  could capture this without Hubble involvement.
