# Exam Controller Design v2

## Problem

A software security course runs pen-testing exams where each student gets an isolated instance of a vulnerable web app on Kubernetes. The instructor needs to control when students can access their instances, verify access works before the exam, and deliver unique URLs to students. The system must handle accommodation privacy (extended time) without identifying which students receive it, respect SMTP relay rate limits, provide spare instances for failover/late additions, and expose metrics for operational dashboards.

## Approach

A Kubernetes-native CRD controller. One `Exam` custom resource defines the entire exam event: what app to deploy, when to unlock/lock, and the student roster. The controller reconciles desired state automatically using a time-driven state machine. Network policies use CiliumNetworkPolicy when available (with L7 visibility) and fall back to vanilla Kubernetes NetworkPolicy otherwise.

## Prerequisites

- **Cilium CNI** (recommended) for L7 visibility, Hubble flow logs, and FQDN-aware egress. Falls back to any CNI with NetworkPolicy enforcement (e.g., Calico). Default Flannel does not enforce NetworkPolicy — locking will silently not work.
- **Wildcard DNS**: `*.exam.otu.ca` pointing to the ingress controller's external IP.
- **Wildcard TLS certificate**: A cert for `*.exam.otu.ca` stored as a Secret (e.g., via cert-manager or manual provisioning). Referenced by all student Ingress resources.
- **NTP-synced nodes**: Time-driven transitions depend on accurate clocks.

## CRD: `Exam`

```yaml
apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: sofe4790u-midterm
spec:
  template:
    image: registry.example.com/vuln-app:v2.1
    resources:
      requests: { cpu: "250m", memory: "256Mi" }
      limits: { cpu: "500m", memory: "512Mi" }
    port: 8080

  schedule:
    unlock: "2026-04-10T14:00:00-04:00"
    duration: "2h"                    # base exam duration
    timeMultiplier: 1.5               # lock = unlock + (duration × multiplier)
    provisionBefore: "1h"             # how early to spawn instances (default: 1h)
    retention: "24h"                  # keep instances after lock, then auto-delete (default: 24h)
    dryRun:
      before: "5m"                    # smoke test window before unlock
      duration: "2m"

  email:
    before: "30m"                     # when to start sending before unlock
    rateLimit: 1                      # emails per second (default: 1)
    instructorEmail: "instructor@ontariotechu.net"
    secretRef: exam-smtp-credentials
    from: "noreply@otu.ca"
    subject: "SOFE4790U - Your Exam Instance"

  students:
    - id: john.smith
      email: john.smith@ontariotechu.net
    - id: jane.doe
      email: jane.doe@ontariotechu.net

  spares: 2                           # extra unassigned instances (default: 0)

  ingressTLS:
    secretName: exam-wildcard-tls
  domain: exam.otu.ca
```

### Computed Values

These are derived from spec fields and surfaced in status:

- `lockTime = unlock + (duration × timeMultiplier)`
- `provisionTime = unlock - provisionBefore`
- `emailTime = unlock - emailBefore`
- `retentionDeadline = lockTime + retention`

### Defaults

| Field | Default | Notes |
|-------|---------|-------|
| `timeMultiplier` | 1.5 | Universal accommodation buffer |
| `provisionBefore` | 1h | Enough for image pulls, health checks, dry run |
| `retention` | 24h | Business day to investigate disputes |
| `email.before` | 30m | Balance between relay limits and URL exposure |
| `email.rateLimit` | 1 | 1 email/second, conservative for most relays |
| `spares` | 0 | No spares unless requested |
| `dryRun.before` | 5m | Smoke tests shortly before unlock |
| `dryRun.duration` | 2m | Quick health check pass |

### Spec Immutability

Once the controller transitions past `Pending`, the following fields become immutable:

- `spec.template` (would require recreating all Deployments)
- `spec.students[].id` (would orphan resources)
- `spec.schedule.unlock` (would disrupt the state machine)
- `spec.spares` (would orphan or require new instances mid-exam)

Fields that remain mutable until `Locked`:

- `spec.schedule.duration`, `spec.schedule.timeMultiplier` — adjusting lock time mid-exam is a legitimate "extend the exam" use case. Immutable once the exam reaches `Locked` to prevent reopening a completed exam. The webhook also enforces that the new computed `lockTime >= now` to prevent setting lock time in the past.

Fields that remain always mutable:

- `spec.students[].email` — fix typos before email is sent
- `spec.email.subject`, `spec.email.from`

The controller rejects edits to immutable fields via a validating webhook.

## Controller State Machine

```
Pending → Provisioning → Ready → Unlocked → Locked → TearingDown
```

Six phases, down from nine in v1. Locking eliminated (everyone locks at the same computed time). Email sending and dry run are time-gated substeps of the Ready phase, guarded by status conditions to prevent re-execution on controller restart or leader failover.

### States

| State | Description |
|-------|-------------|
| Pending | Exam resource exists, waiting for provision time |
| Provisioning | Creating namespace, Deployments, Services, Ingress per student + spares |
| Ready | All instances healthy, policies blocking traffic. Time-gated substeps: emails sent (at `emailTime`, guarded by `AllEmailsSent` condition), dry run (at `dryRunTime`, guarded by `DryRunComplete` condition). Waiting for unlock. |
| Unlocked | Exam in progress, students have access |
| Locked | All students locked simultaneously, exam over, retention timer running |
| TearingDown | Retention expired, resources being deleted automatically |

### Timeline (defaults, 2h exam)

| Time | Event |
|------|-------|
| unlock - 1h | Provisioning starts |
| unlock - 30m | Student emails sent (throttled at 1/s) |
| unlock - 5m | Dry run smoke tests |
| unlock | Ingress allow policy created, exam live, instructor notified |
| unlock + 3h | Lock (2h × 1.5), instructor notified, ingress deleted |
| unlock + 27h | Auto-teardown (lock + 24h retention) |

### Degraded States

If provisioning or dry run encounters failures, the controller sets conditions visible via `kubectl describe exam`:

- **ProvisioningDegraded**: Some instances failed to start. Healthy instances continue.
- **DryRunFailed**: Smoke tests failed for some instances. Unlock proceeds on schedule regardless.

The unlock proceeds on schedule regardless of degraded conditions — the instructor can manually intervene if needed, but the controller does not block the exam for other students.

### Time-Driven Transitions and RequeueAfter

Each reconcile checks current time against the schedule. The controller uses explicit `RequeueAfter` durations to wake at the next transition boundary:

| Current State | RequeueAfter |
|---------------|-------------|
| Pending | `provisionTime - now` |
| Provisioning | Short interval (polling instance health) |
| Ready | `min(emailTime, dryRunTime, unlock) - now` (whichever substep or transition is next) |
| Unlocked | `lockTime - now` |
| Locked | `retentionDeadline - now` |

### Drift Correction

The controller enforces drift correction in all phases:

- **Locked/Ready**: If a deny-all or egress policy is accidentally deleted, the controller recreates it. If an ingress-allow policy or Ingress resource is accidentally created, the controller removes it.
- **Unlocked**: If an ingress-allow policy is accidentally deleted, the controller recreates it. If a deny-all policy is accidentally created where it shouldn't be, the controller removes it.

The controller watches Ingress and NetworkPolicy resources (via `SetupWithManager` owns/watches) so drift is detected immediately via the informer cache, not just on the next scheduled RequeueAfter wake-up.

## Namespace Strategy

The controller creates one namespace per Exam (e.g., `exam-sofe4790u-midterm`). All student resources live in this namespace. Benefits:

- NetworkPolicy scope is clean
- ResourceQuota can cap the whole exam
- `kubectl delete namespace` is the nuclear cleanup option
- Namespace deletion cascades to all contained resources

The Exam CR lives in `exam-system` (the controller's namespace), not in the per-exam namespace. Since Kubernetes owner references cannot cross namespace boundaries, the controller uses a **finalizer** (`exam.otu.ca/cleanup`) on the Exam CR instead. The finalizer ensures the per-exam namespace is deleted before the Exam CR is removed. Namespace deletion cascades to all student resources (Deployments, Services, Ingresses, NetworkPolicies) automatically.

## Network Policy — Graceful Degradation

The controller detects at startup whether CiliumNetworkPolicy CRDs exist in the cluster via API discovery. It selects the appropriate policy provider:

### Interface

```go
type PolicyProvider interface {
    DenyAll(namespace string, labels map[string]string) client.Object
    EgressAllowlist(namespace string, labels map[string]string) client.Object
    IngressAllow(namespace string, labels map[string]string) client.Object
}
```

Two implementations: `CiliumPolicyProvider` and `VanillaPolicyProvider`. The controller reconciler is agnostic to which one is active.

### Vanilla NetworkPolicy (fallback)

**Always present (exam lifetime):**

1. **Default deny-all ingress + egress**: Baseline isolation.
2. **Egress allowlist**: DNS resolution (port 53 UDP+TCP) restricted to CoreDNS pods specifically (`namespaceSelector` matching kube-system + `podSelector` matching `k8s-app: kube-dns`). This is tighter than allowing all pods in kube-system on port 53.

**Toggled by lock/unlock:**

3. **Ingress allow from ingress controller**: `namespaceSelector` + `podSelector` matching ingress controller pods. Added at unlock, removed at lock.

### CiliumNetworkPolicy (when available)

Same three-policy model, plus:

- **L7 HTTP visibility**: Ingress allow policy includes `l7Rules` with HTTP match-all, enabling Envoy to proxy traffic for per-request metrics (method, path, status code, latency).
- **FQDN-aware egress**: DNS allowlist uses `toFQDNs` instead of label selectors.
- **Hubble flow logs**: Source IP, request paths, timestamps per student — available via Hubble relay without sidecar injection.

```yaml
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: <slug>-ingress-allow
spec:
  endpointSelector:
    matchLabels:
      exam.otu.ca/slug: <slug>
  ingress:
    - fromEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: ingress-nginx
            app.kubernetes.io/name: ingress-nginx    # match specific ingress controller pods, not all pods in namespace
      toPorts:
        - ports:
            - port: "8080"
              protocol: TCP
          rules:
            http:
              - method: ""    # match all — visibility only, not filtering
```

Both Cilium and vanilla providers use equivalent pod-level selectors for the ingress controller to maintain identical trust boundaries.

### Lock Enforcement

At lock time, the controller:

1. Removes the ingress-allow policy (new connections blocked)
2. Deletes the student's Ingress resource (terminates the route — kills established connections that would otherwise survive the policy change)

**Known limitation**: Lock enforcement is eventual, not instantaneous. Ingress controller reload latency and in-flight keep-alive connections mean a student may retain access for a few seconds after lock time. For a multi-hour exam with a 1.5x time multiplier, this is acceptable. The Ingress deletion (step 2) bounds the window — once the ingress controller processes the deletion, no new or existing connections can reach the pod.

## Dry Run / Smoke Tests

Run as a time-gated substep of the Ready phase, starting `spec.schedule.dryRun.before` before unlock time, lasting `spec.schedule.dryRun.duration`. Guarded by the `DryRunComplete` status condition to prevent re-execution on controller restart. If `spec.schedule.dryRun` is omitted, the dry run is skipped entirely.

**The dry run does NOT remove NetworkPolicies.** The controller runs smoke tests from inside the cluster (bypassing the deny-all policy by testing pod-to-pod connectivity directly):

1. HTTP health check against each student's Service ClusterIP (validates pod is responding)
2. DNS resolution check for each student's hostname (validates wildcard DNS)
3. TLS validation against the Ingress (best-effort — may fail in clusters without hairpin NAT; failure is logged as a warning, not a blocking error)
4. **Negative connectivity test**: Attempt to reach a student's Service from outside the allowed ingress path (e.g., from the controller pod, which is not in the ingress-controller namespace). If the connection succeeds, NetworkPolicy enforcement is broken. The controller sets a `NetworkPolicyEnforced: False` condition and logs a critical warning. This catches misconfigured CNIs (Flannel, broken Cilium) that silently pass traffic.
5. Results written to `status.dryRun`

This avoids the early-access hole where a student polling their URL could catch a dry-run window.

## Spare Instances

The controller provisions `spec.spares` additional instances with no student assigned. These are:

- Provisioned, health-checked, and smoke-tested alongside student instances
- Tracked separately in `status.spares` (each with slug and URL)
- All spare URLs sent in a **single email** to `spec.email.instructorEmail` after provisioning
- Available for the instructor to manually reassign if a student's instance fails or a late addition arrives

Spares follow the same lock/unlock lifecycle as student instances.

## URLs & Email

### URL Scheme

Each student (and spare) gets a non-guessable URL:

```
https://<random-slug>.exam.otu.ca
```

**Slug generation**: 8-character lowercase alphanumeric string generated using `crypto/rand`. DNS-safe, collision probability negligible for class sizes. Stored in `status.students[].slug`. Errors from `crypto/rand` (entropy source failure) must be propagated — a failed slug generation should fail the provisioning step for that student, not produce an empty slug.

No student IDs appear in URLs.

**Accepted risk**: URLs are bearer tokens — anyone with the URL can access the instance. There is no per-student authentication or source IP binding. Random 8-character slugs (36^8 ≈ 2.8 trillion combinations) make guessing infeasible, but sharing or forwarding a URL would grant access. This is an accepted trade-off: adding auth would require SSO integration or per-student credentials, which adds significant complexity for a pen-testing exam where the target app is intentionally vulnerable. The limited exam window (hours, not days) and network-level locking further bound the exposure.

### Email Timing & Throttling

Student emails begin sending at `unlock - emailBefore`, throttled at `emailRateLimit` per second. The spare URLs instructor email is sent separately after provisioning completes (not part of the pre-unlock student batch).

The admission webhook validates that all student emails can be sent before unlock, using ceiling division and a 1.5x buffer to account for retries:

```
emailBefore >= ceil(len(students) / emailRateLimit) × 1.5
```

If email delivery fails, the controller retries with exponential backoff (max 3 retries). Failures recorded in `status.students[].emailStatus`. The instructor notification at unlock time includes a list of students whose emails failed, so the instructor can manually share URLs. Fallback:

```bash
kubectl get exam sofe4790u-midterm -o jsonpath='{.status.students[?(@.id=="jane.doe")].url}'
```

### Instructor Notifications

All sent to `spec.email.instructorEmail` via SMTP:

| Event | Timing | Content |
|-------|--------|---------|
| Spares ready | After provisioning completes | Single email listing all spare URLs |
| Exam unlocked | At unlock time | "Exam is live, N students, M spares" |
| Exam locked | At lock time | "Exam ended" + summary stats (healthy/failed counts) |

## Per-Student Resources

For each student (and spare), the controller creates within the exam namespace:

- **Deployment**: Single-replica pod running the vulnerable app
- **Service**: ClusterIP pointing to the pod
- **Ingress**: Maps `<slug>.exam.otu.ca` to the Service, references wildcard TLS Secret
- **NetworkPolicy**: Three-policy model (vanilla or Cilium, selected at startup)

All resources are named by slug (e.g., `a3f9b2c1-deny-all`, `a3f9b2c1-deploy`), not by student ID. Student IDs appear only as labels (`exam.otu.ca/student`) visible to the instructor via kubectl. This ensures student identity is not leaked through resource names.

### Pod Security

```yaml
securityContext:
  runAsNonRoot: true
  allowPrivilegeEscalation: false
  capabilities:
    drop: ["ALL"]
  seccompProfile:
    type: RuntimeDefault
automountServiceAccountToken: false
```

## Status Schema

```yaml
status:
  phase: Unlocked
  message: "Exam in progress — 48 students, 2 spares"

  # Computed schedule (read-only, set by controller)
  computedLockTime: "2026-04-10T17:00:00-04:00"
  provisionTime: "2026-04-10T13:00:00-04:00"
  emailTime: "2026-04-10T13:30:00-04:00"
  retentionDeadline: "2026-04-11T17:00:00-04:00"

  conditions:
    - type: Provisioned
      status: "True"
      lastTransitionTime: "2026-04-10T13:05:00-04:00"
    - type: AllEmailsSent
      status: "True"
      lastTransitionTime: "2026-04-10T13:31:00-04:00"
    - type: DryRunComplete
      status: "True"
      lastTransitionTime: "2026-04-10T13:57:00-04:00"
    - type: NetworkPolicyEnforced
      status: "True"                                     # False if negative connectivity test fails
      lastTransitionTime: "2026-04-10T13:57:00-04:00"

  dryRun:
    completedAt: "2026-04-10T13:57:00-04:00"
    passed: 50
    failed: 0
    failures: []

  students:
    - id: john.smith
      slug: a3f9b2c1
      url: https://a3f9b2c1.exam.otu.ca
      phase: Unlocked
      emailStatus: Sent
      emailSentAt: "2026-04-10T13:30:12-04:00"

  spares:
    - slug: x9k2m4p7
      url: https://x9k2m4p7.exam.otu.ca
      phase: Unlocked
    - slug: r3t8w1n5
      url: https://r3t8w1n5.exam.otu.ca
      phase: Unlocked

  metrics:
    totalStudents: 48
    totalSpares: 2
    emailsSent: 48
    emailsFailed: 0
    instancesHealthy: 50
    instancesFailed: 0
```

## Prometheus Metrics

### Operational (controller health)

| Metric | Type | Description |
|--------|------|-------------|
| `exam_reconcile_duration_seconds` | Histogram | Time spent per reconcile loop |
| `exam_reconcile_errors_total` | Counter | Reconcile failures |
| `exam_phase_transitions_total` | Counter | Phase changes, labeled by `from` and `to` |

### Lifecycle (per exam, labeled by exam name)

| Metric | Type | Description |
|--------|------|-------------|
| `exam_instances_total` | Gauge | Total instances (students + spares) |
| `exam_instances_healthy` | Gauge | Instances passing health checks |
| `exam_instances_failed` | Gauge | Instances in failed state |
| `exam_emails_sent_total` | Counter | Emails successfully sent |
| `exam_emails_failed_total` | Counter | Email delivery failures |
| `exam_dryrun_passed` | Gauge | Dry run pass count |
| `exam_dryrun_failed` | Gauge | Dry run fail count |
| `exam_seconds_until_unlock` | Gauge | Countdown to unlock (0 after unlock) |
| `exam_seconds_until_lock` | Gauge | Countdown to lock (0 after lock) |

### Access Stats (Cilium only, not emitted by controller)

When Cilium is present, the following are available via Hubble/Envoy:

- Per-host request rate, latency, response codes (Envoy metrics filtered by `*.exam.otu.ca`)
- Source IP per connection (Hubble flow logs)
- L7 HTTP path visibility (CiliumNetworkPolicy L7 rules)

These require Grafana dashboards querying Hubble/Envoy metrics — documented separately, not emitted by the controller.

## Admission Validation

### At creation time

- `emailBefore >= ceil(len(students) / emailRateLimit) × 1.5` — student emails must finish before unlock (1.5x buffer for retries)
- `provisionBefore > emailBefore` — must provision before emailing
- `dryRun.before` must fit within provisioning window
- `duration > 0`
- `timeMultiplier >= 1.0`
- `spares >= 0`
- At least one student
- `instructorEmail` is required

### After provisioning (immutable fields)

- `spec.template` (image, port, resources)
- `spec.students[].id`
- `spec.schedule.unlock`
- `spec.spares`
- `spec.domain` (must be DNS-label safe, validated at creation)

### After locked (additionally immutable)

- `spec.schedule.duration`, `spec.schedule.timeMultiplier` (prevents reopening a completed exam)

### Mutable until locked

- `spec.schedule.duration`, `spec.schedule.timeMultiplier` (extend/shorten live exams). Webhook enforces `newLockTime >= now`.

### Always mutable

- `spec.students[].email` (fix typos before send)
- `spec.email.subject`, `spec.email.from`

### Format validation (at creation)

- `spec.domain` must be a valid DNS domain
- `spec.students[].id` must be a valid Kubernetes label value (alphanumeric, `-`, `_`, `.`, max 63 chars — e.g., `john.smith` is valid)
- `spec.email.instructorEmail` must be a valid email address

## Project Structure

```
exam-controller/
├── api/v1alpha1/            # CRD type definitions
├── internal/
│   ├── controller/          # Reconciliation loop
│   ├── network/             # PolicyProvider interface + Vanilla/Cilium implementations
│   ├── provisioner/         # Per-student Deployment/Service/Ingress
│   ├── smoketest/           # Cluster-internal health/DNS/TLS checks
│   ├── notifier/            # SMTP email delivery with throttling
│   ├── webhook/             # Validating webhook for spec immutability + timing validation
│   └── metrics/             # Prometheus metric registration
├── config/
│   ├── crd/                 # Generated CRD manifests
│   ├── rbac/                # Controller RBAC
│   ├── webhook/             # Webhook configuration
│   └── samples/             # Example Exam manifests
├── cmd/main.go              # Entrypoint
├── Dockerfile
├── Makefile
└── go.mod
```

## Decisions

- **Network-level locking over auth proxy**: Simpler, fewer moving parts, leverages native K8s primitives.
- **Global time multiplier over per-student lock overrides**: Everyone gets `duration × timeMultiplier`. Accommodated students get their time without being identifiable. Eliminates the `Locking` intermediate state.
- **PreProvisioned only**: OnDemand rejected — too many failure modes on exam day's critical path.
- **Random slugs over student IDs in URLs**: Privacy for accommodated students. Generated with `crypto/rand`.
- **Dry run without removing NetworkPolicies**: Tests run cluster-internally to prevent early-access hole.
- **Configurable email timing with rate limiting**: Respects SMTP relay limits. Admission webhook validates emails can finish before unlock.
- **Delete Ingress at lock time**: Kills established connections that survive NetworkPolicy changes.
- **Automatic teardown with configurable retention**: 24h default gives time to investigate, then auto-deletes. No manual annotation required.
- **One namespace per exam**: Clean isolation boundary, finalizer-based cleanup, easy nuclear option via `kubectl delete namespace`.
- **Finalizer over owner references**: Exam CR lives in `exam-system`, student resources in per-exam namespace. Cross-namespace owner references don't work in Kubernetes, so a finalizer handles cleanup via namespace deletion cascade.
- **Spec immutability after provisioning**: Validating webhook prevents accidental disruption of live exams. Duration/multiplier remain mutable until Locked for mid-exam adjustments, then frozen to prevent reopening.
- **Negative NetworkPolicy smoke test**: Dry run includes a connectivity test that verifies policies are actually enforced, catching misconfigured CNIs before the exam starts.
- **CiliumNetworkPolicy with vanilla fallback**: L7 visibility and Hubble integration when Cilium is present, graceful degradation to standard NetworkPolicy otherwise.
- **Spare instances**: General-purpose buffer for failover and late additions. URLs sent in a single email to the instructor.
- **Single instructor email for spares**: One consolidated email, not per-spare.

## Future Enhancements

- **Slack/webhook notifications**: Notify instructor via Slack or arbitrary webhook in addition to email.
- **Grafana dashboard templates**: Pre-built dashboards for Cilium/Envoy access metrics filtered by exam slug hostnames.
- **Student access reports**: Post-exam summary of per-student access patterns (source IPs, request counts, activity timeline) derived from Hubble flow logs.
- **Multiple templates per exam**: Different vuln apps for different exam sections.
- **Exam cloning**: `kubectl` command to duplicate an exam spec for a new semester.
- **Resource quota integration**: Auto-create namespace ResourceQuota based on `template.resources × (len(students) + spares)`.
- **Image pre-pull DaemonSet**: Ensure container image is cached on all nodes before provisioning starts.
- **Vanilla NetworkPolicy port**: Full port for environments without Cilium, covering all features except L7 visibility.
- **Auto-spare reassignment**: If a student's instance fails before unlock, automatically swap in a spare and re-send the email with the new URL. Currently manual to keep the instructor in control.
