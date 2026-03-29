# exam-controller

A Kubernetes operator that automates the full lifecycle of pen-testing exam instances for university courses. It provisions isolated, network-locked containers for each student, sends credential emails on a schedule, enforces network policies per phase, and tears everything down after a configurable retention window. Built with Kubebuilder for instructors who need hands-off exam orchestration on any Kubernetes cluster.

## How It Works

The controller manages each `Exam` custom resource through a six-phase state machine. Phase transitions are time-driven, computed from the `spec.schedule` fields.

```
Pending --> Provisioning --> Ready --> Unlocked --> Locked --> TearingDown
```

**Pending** -- The exam resource exists but the provisioning window has not started yet. The controller sleeps until `unlock - provisionBefore`.

**Provisioning** -- The controller creates a dedicated per-exam namespace (`exam-<name>-<hash>`), then provisions a Deployment, Service, and deny-all/egress-allowlist network policies for each student and spare instance. It polls every 10 seconds until all pods report ready.

**Ready** -- All instances are healthy. The controller sends credential emails to students (rate-limited) starting at `unlock - email.before`, optionally runs a dry-run smoke test, and enforces deny-all network policies. Spare instance URLs are emailed to the instructor.

**Unlocked** -- The exam is in progress. Ingress resources and ingress-allow network policies are created so students can reach their instances via `https://<slug>.<domain>`. The instructor receives an unlock notification. The controller sleeps until the computed lock time (`unlock + duration * timeMultiplier`).

**Locked** -- The exam has ended. Ingress resources and ingress-allow policies are removed, cutting off student access. Instances are retained for investigation. The instructor receives a lock notification with pass/fail counts. The controller sleeps until the retention deadline.

**TearingDown** -- The retention window has expired. The controller deletes the exam namespace (and all resources within it) and cleans up Prometheus metric series.

## Quick Start

### Prerequisites

- Go 1.24.6+
- Docker 17.03+
- kubectl 1.11.3+
- Access to a Kubernetes cluster

### Install CRDs

```sh
make install
```

### Deploy the controller

```sh
make deploy IMG=ghcr.io/rdrake/exam-controller:v0.1.0
```

### Create your first exam

```sh
kubectl apply -f config/samples/exam_v1alpha1_exam.yaml
```

See the [sample manifest](config/samples/exam_v1alpha1_exam.yaml) for all available fields.

## Installation

### Kustomize

```sh
make deploy IMG=ghcr.io/rdrake/exam-controller:v0.1.0
```

Or generate a standalone installer YAML:

```sh
make build-installer IMG=ghcr.io/rdrake/exam-controller:v0.1.0
kubectl apply -f dist/install.yaml
```

### Helm

```sh
helm install exam-controller \
  oci://ghcr.io/rdrake/charts/exam-controller \
  --version 0.1.0
```

See [`charts/exam-controller/values.yaml`](charts/exam-controller/values.yaml) for all configurable Helm values.

## Configuration

### Exam CR Spec Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `spec.template.image` | `string` | (required) | Container image for student instances. Immutable after provisioning. |
| `spec.template.port` | `int32` | (required) | Port the container listens on. Immutable after provisioning. |
| `spec.template.resources` | `ResourceRequirements` | none | CPU/memory requests and limits for each instance. Immutable after provisioning. |
| `spec.schedule.unlock` | `Time` | (required) | When the exam unlocks (ISO 8601). Immutable after provisioning. |
| `spec.schedule.duration` | `Duration` | (required) | Base exam duration (e.g. `"2h"`). Immutable after locking. |
| `spec.schedule.timeMultiplier` | `float64` | `1.5` | Multiplied with duration to compute lock time. Must be >= 1.0. Immutable after locking. |
| `spec.schedule.provisionBefore` | `Duration` | `"1h"` | How long before unlock to start provisioning. Must be greater than `email.before`. |
| `spec.schedule.retention` | `Duration` | `"24h"` | How long to keep instances after locking before teardown. |
| `spec.schedule.dryRun.before` | `Duration` | -- | How long before unlock to run the smoke test. |
| `spec.schedule.dryRun.duration` | `Duration` | -- | Timeout for the smoke test HTTP checks. |
| `spec.students` | `[]Student` | (required) | List of `{id, email}` entries. At least one required. IDs must be valid label values. Immutable after provisioning. |
| `spec.email.before` | `Duration` | `"30m"` | How long before unlock to begin sending emails. Must allow enough time for all students at the configured rate limit. |
| `spec.email.rateLimit` | `int` | `1` | Maximum emails sent per second. |
| `spec.email.instructorEmail` | `string` | (required) | Instructor email for notifications (spares, unlock, lock). |
| `spec.email.secretRef` | `string` | (required) | Name of the Kubernetes Secret containing SMTP credentials. |
| `spec.email.from` | `string` | (required) | Sender address for all emails. |
| `spec.email.subject` | `string` | (required) | Email subject line. |
| `spec.spares` | `int` | `0` | Number of spare instances to provision. Immutable after provisioning. |
| `spec.domain` | `string` | (required) | Base domain for student Ingress URLs (e.g. `exam.otu.ca`). Must be a valid DNS subdomain. Immutable after provisioning. |
| `spec.ingressTLS.secretName` | `string` | (required) | Name of the TLS Secret for Ingress resources (e.g. a wildcard certificate). |

## Email Setup

The controller reads SMTP credentials from a Kubernetes Secret referenced by `spec.email.secretRef`. Create the secret in the same namespace as the Exam resource:

```sh
kubectl create secret generic exam-smtp-credentials \
  --namespace exam-system \
  --from-literal=host=smtp.example.com \
  --from-literal=port=587 \
  --from-literal=username=apikey \
  --from-literal=password=SG.xxxxx
```

| Secret Key | Description |
|---|---|
| `host` | SMTP server hostname |
| `port` | SMTP server port (defaults to 587 if omitted) |
| `username` | SMTP authentication username |
| `password` | SMTP authentication password |

The controller resolves credentials from the Secret at send time (not at startup), so you can rotate credentials without restarting the controller. Emails are sent with automatic retry (up to 3 attempts).

Three types of emails are sent:

1. **Student emails** -- Each student receives their unique instance URL before the exam unlocks.
2. **Instructor spare notification** -- The instructor receives all spare instance URLs when provisioning completes.
3. **Instructor unlock/lock notifications** -- The instructor is notified when the exam unlocks (including any failed email deliveries) and when it locks (with healthy/failed instance counts).

## Network Policies

### Backend Auto-Detection

At startup, the controller checks the Kubernetes API for the `CiliumNetworkPolicy` CRD. If found, it uses the Cilium policy provider with L7 visibility. Otherwise, it falls back to vanilla Kubernetes `NetworkPolicy` resources.

### Three-Policy Model

For each student/spare instance, the controller creates three network policies:

| Policy | Present | Effect |
|---|---|---|
| **deny-all** | All phases after provisioning | Blocks all ingress and egress traffic to/from the pod. |
| **egress-allowlist** | All phases after provisioning | Permits DNS lookups (UDP/TCP port 53) to `kube-dns` in `kube-system`. |
| **ingress-allow** | Unlocked phase only | Permits inbound traffic from the ingress controller on the container port. |

### Per-Phase Behavior

| Phase | Ingress | Egress | Ingress Resources |
|---|---|---|---|
| Provisioning | Blocked | DNS only | None |
| Ready | Blocked | DNS only | None |
| Unlocked | Allowed (from ingress controller) | DNS only | Created |
| Locked | Blocked | DNS only | Deleted |

The dry-run smoke test (if configured) includes a negative connectivity check that verifies network policies are actually enforced. The result is recorded in the `NetworkPolicyEnforced` status condition.

## Monitoring

The controller exposes 12 Prometheus metrics. Enable a `ServiceMonitor` via the Helm chart by setting `metrics.serviceMonitor.enabled: true`.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `exam_reconcile_duration_seconds` | Histogram | -- | Time spent per reconcile loop. |
| `exam_reconcile_errors_total` | Counter | -- | Total reconcile failures. |
| `exam_phase_transitions_total` | Counter | `exam`, `namespace`, `from`, `to` | Phase changes by exam and transition direction. |
| `exam_instances_total` | Gauge | `exam`, `namespace` | Total instances (students + spares). |
| `exam_instances_healthy` | Gauge | `exam`, `namespace` | Instances passing health checks. |
| `exam_instances_failed` | Gauge | `exam`, `namespace` | Instances in failed state. |
| `exam_emails_sent_total` | Counter | `exam`, `namespace` | Emails successfully sent. |
| `exam_emails_failed_total` | Counter | `exam`, `namespace` | Email delivery failures. |
| `exam_dryrun_passed` | Gauge | `exam`, `namespace` | Dry run pass count. |
| `exam_dryrun_failed` | Gauge | `exam`, `namespace` | Dry run fail count. |
| `exam_seconds_until_unlock` | Gauge | `exam`, `namespace` | Countdown to unlock (0 after unlock). |
| `exam_seconds_until_lock` | Gauge | `exam`, `namespace` | Countdown to lock (0 after lock). |

Metric series for a given exam are automatically cleaned up during the TearingDown phase to prevent unbounded cardinality growth.

## Development

```sh
# Run the fast preflight checks before pushing
make verify-fast

# Build the manager binary
make build

# Run the envtest-backed integration suite and coverage gate
make test

# Run the linter
make lint

# Run end-to-end tests (requires Kind)
make test-e2e

# Run the controller locally against your current kubeconfig
make run
```

Additional targets:

```sh
# Generate CRD manifests and RBAC
make manifests

# Generate DeepCopy methods
make generate

# Verify generated files are committed
make check-generated

# Verify Helm linting and key chart renders
make helm-verify

# Build the container image
make docker-build IMG=ghcr.io/rdrake/exam-controller:dev

# Push the container image
make docker-push IMG=ghcr.io/rdrake/exam-controller:dev
```

## License

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
