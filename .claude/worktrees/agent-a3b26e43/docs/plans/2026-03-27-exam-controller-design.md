# Exam Controller Design

## Problem

A software security course runs pen-testing exams where each student gets an isolated instance of a vulnerable web app on Kubernetes. The instructor needs to control when students can access their instances, verify access works before the exam, and deliver unique URLs to students.

## Approach

A Kubernetes-native CRD controller. One `Exam` custom resource defines the entire exam event: what app to deploy, when to unlock/lock, and the student roster. The controller reconciles desired state automatically.

## Prerequisites

- **CNI with NetworkPolicy enforcement** (e.g., Calico, Cilium). Default Flannel does not enforce NetworkPolicy — locking will silently not work.
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
  # What to deploy per student
  template:
    image: registry.example.com/vuln-app:v2.1
    resources:
      requests: { cpu: "250m", memory: "256Mi" }
      limits: { cpu: "500m", memory: "512Mi" }
    port: 8080

  # When
  schedule:
    unlock: "2026-04-10T14:00:00-04:00"
    lock: "2026-04-10T16:00:00-04:00"
    dryRun:
      before: "15m"    # 5-min test window starting 15min before unlock
      duration: "5m"

  # Who — lockOverride supports accommodated students without identifying them
  students:
    - id: john.smith
      email: john.smith@ontariotechu.net
    - id: jane.doe
      email: jane.doe@ontariotechu.net
      lockOverride: "2026-04-10T17:00:00-04:00"

  # Infrastructure
  ingressTLS:
    secretName: exam-wildcard-tls
  domain: exam.otu.ca

  smtp:
    secretRef: exam-smtp-credentials
    from: "noreply@otu.ca"
    subject: "SOFE4790U - Your Exam Instance"
```

Provisioning is always PreProvisioned — instances are created when the Exam resource is applied (days before the exam) and locked via NetworkPolicy until exam time. OnDemand provisioning was considered and rejected: image pulls, scheduling, ingress programming, DNS propagation, and email delivery all landing on the critical path at exam start creates too much risk.

### Spec Immutability

Once the controller transitions past `Pending`, the following fields become immutable:
- `spec.template` (would require recreating all Deployments)
- `spec.students[].id` (would orphan resources)
- `spec.schedule.unlock` (would disrupt the state machine)

The controller rejects edits to these fields via a validating webhook. Fields that remain mutable: `spec.schedule.lock`, `spec.students[].lockOverride` (to adjust accommodations), and `spec.students[].email` (to fix typos before email is sent).

## Controller State Machine

```
Pending -> Provisioning -> Ready -> DryRun -> Verified -> Unlocked -> Locking -> Locked -> TearingDown
```

### States

| State | Description |
|-------|-------------|
| Pending | Exam resource exists, nothing provisioned |
| Provisioning | Creating namespace, Deployments, Services, Ingress per student |
| Ready | All instances healthy, NetworkPolicies blocking traffic, capacity validated |
| DryRun | Smoke tests running (internal only — policies stay in place, see below) |
| Verified | Dry run passed, waiting for unlock time. Distinct from Ready to prevent re-running dry run on controller restart |
| Unlocked | Exam in progress, students have access |
| Locking | Mixed state: some students locked (past their lock time), others still writing (accommodation overrides) |
| Locked | All students locked, exam over |
| TearingDown | Cleanup triggered manually, resources being deleted |

### Degraded States

If provisioning or dry run encounters failures, the controller enters a sub-state visible in `status.conditions`:

- **ProvisioningDegraded**: Some student instances failed to start. The controller continues provisioning healthy students but sets a condition with the failure list.
- **DryRunFailed**: Smoke tests failed for some instances. The controller transitions to `Verified` regardless (the schedule must be honored), but sets a condition so the instructor can investigate via `kubectl describe exam`.

The unlock proceeds on schedule regardless of degraded conditions — the instructor can manually intervene if needed, but the controller does not block the exam for other students.

### Time-Driven Transitions and RequeueAfter

Each reconcile checks current time against the schedule. The controller uses explicit `RequeueAfter` durations to wake at the next transition boundary:

| Current State | RequeueAfter |
|---------------|-------------|
| Ready | `dryRunStart - now` |
| DryRun | `dryRunDuration` remaining |
| Verified | `unlock - now` |
| Unlocked | `earliestStudentLock - now` |
| Locking | `nextStudentLock - now` |

This ensures transitions happen promptly without relying on external events to trigger reconciliation.

### Drift Correction

If a NetworkPolicy is accidentally deleted during the locked phase, the controller recreates it on next reconcile. If a NetworkPolicy is accidentally created during the unlocked phase, the controller removes it.

## Namespace Strategy

The controller creates one namespace per Exam (e.g., `exam-sofe4790u-midterm`). All student resources live in this namespace. Benefits:

- NetworkPolicy scope is clean
- ResourceQuota can cap the whole exam
- `kubectl delete namespace` is the nuclear cleanup option
- Owner references work (all resources in the same namespace as the Exam CR)

The controller itself runs in a separate `exam-system` namespace.

## Network Policies

### Three-Policy Model

Rather than toggling between "deny-all" and "no policy," the controller maintains explicit policies:

**Always present (exam lifetime):**

1. **Default deny-all ingress + egress**: Baseline isolation. No traffic in or out unless explicitly allowed.
2. **Egress allowlist**: Permits DNS resolution (kube-dns) and any explicit egress targets the vuln app needs. Blocks access to the Kubernetes API server, other student pods, and the internet (unless the exam requires it).

**Toggled by lock/unlock:**

3. **Ingress allow from ingress controller**: Added at unlock, removed at lock. Uses a `namespaceSelector` + `podSelector` matching the ingress controller pods. This means "unlocked" = reachable via ingress only, not by arbitrary cluster traffic.

### Lock Enforcement

At lock time, the controller:
1. Removes the ingress-allow policy (new connections blocked)
2. Deletes the student's Ingress resource (terminates the route — kills established connections that would otherwise survive the NetworkPolicy change)
3. Recreates the Ingress resource only if the student is unlocked again (not expected, but supports manual override)

This handles the "long-lived TCP connection past deadline" problem that NetworkPolicy alone does not solve.

### Per-Student Locking

Students with `lockOverride` get locked at their override time instead of the global `schedule.lock`. The controller checks each student's effective lock time individually, producing the `Locking` intermediate state where some students are locked and others are still writing.

## Dry Run / Smoke Tests

Starting `spec.schedule.dryRun.before` before unlock time, lasting `spec.schedule.dryRun.duration`.

**The dry run does NOT remove NetworkPolicies.** Instead, the controller runs smoke tests from inside the cluster (bypassing the deny-all policy by testing pod-to-pod connectivity directly):

1. HTTP health check against each student's Service ClusterIP (validates pod is responding)
2. DNS resolution check for each student's hostname (validates wildcard DNS)
3. TLS validation against the Ingress (validates certificate is in place)
4. Results written to `status.dryRun`

This avoids the early-access hole where a student polling their URL could catch a dry-run window. Students never have access before the scheduled unlock.

```yaml
status:
  phase: Verified
  dryRun:
    completedAt: "2026-04-10T13:50:00-04:00"
    passed: 47
    failed: 1
    failures:
      - student: jane.doe
        error: "HTTP 503 - container CrashLoopBackOff"
```

## Capacity Preflight

During the `Provisioning` → `Ready` transition, the controller validates:

- All student pods are Running and passing readiness probes
- Aggregate resource usage is within namespace ResourceQuota
- The container image is present on all nodes (or pullable — the controller creates a DaemonSet-based image pre-pull job during provisioning)

Failures are reported in `status.conditions` so the instructor has time to fix issues before exam day.

## URLs & Email

### URL Scheme

Each student gets a non-guessable URL:

```
https://<random-slug>.exam.otu.ca
```

**Slug generation**: 8-character lowercase alphanumeric string generated using `crypto/rand`. DNS-safe (no hyphens needed at this length, collision probability negligible for class sizes). Stored in `status.students[].slug`.

No student IDs appear in URLs. Student ID labels on Kubernetes resources are only visible to the instructor via kubectl.

### Email Timing

Emails are sent **30 minutes before unlock**, regardless of when provisioning happened. This limits the window where students have the URL before the exam starts, reducing the chance of probing or social engineering.

If email delivery fails, the controller retries with exponential backoff (max 3 retries). Failures are recorded in `status.students[].emailStatus`. Fallback:

```bash
kubectl get exam sofe4790u-midterm -o jsonpath='{.status.students[?(@.id=="jane.doe")].url}'
```

## Per-Student Resources

For each student, the controller creates within the exam namespace:

- **Deployment**: Single-replica pod running the vulnerable app
- **Service**: ClusterIP pointing to the pod
- **Ingress**: Maps `<slug>.exam.otu.ca` to the Service, references wildcard TLS Secret
- **NetworkPolicy**: Three-policy model described above

All resources have `ownerReferences` pointing to the Exam CR for garbage collection.

### Pod Security

```yaml
securityContext:
  runAsNonRoot: true
  readOnlyRootFilesystem: true  # if the vuln app supports it
  allowPrivilegeEscalation: false
  capabilities:
    drop: ["ALL"]
  seccompProfile:
    type: RuntimeDefault
automountServiceAccountToken: false
```

The namespace is labeled for Pod Security Standards enforcement (`pod-security.kubernetes.io/enforce: restricted` or `baseline` depending on what the vuln app requires).

## Status Schema

```yaml
status:
  phase: Unlocked                    # Current state machine state
  message: "Exam in progress, 2 students with extended time"

  conditions:
    - type: Provisioned
      status: "True"
      lastTransitionTime: "2026-04-08T10:00:00-04:00"
    - type: DryRunPassed
      status: "True"
      lastTransitionTime: "2026-04-10T13:50:00-04:00"
    - type: AllEmailsSent
      status: "True"
      lastTransitionTime: "2026-04-10T13:30:00-04:00"

  dryRun:
    completedAt: "2026-04-10T13:50:00-04:00"
    passed: 48
    failed: 0
    failures: []

  students:
    - id: john.smith
      slug: a3f9b2c1
      url: https://a3f9b2c1.exam.otu.ca
      phase: Unlocked         # Provisioned | Healthy | Unlocked | Locked | Failed
      emailStatus: Sent       # Pending | Sent | Failed
      emailSentAt: "2026-04-10T13:30:00-04:00"
      lockedAt: null
      effectiveLockTime: "2026-04-10T16:00:00-04:00"
    - id: jane.doe
      slug: b7e2d4f8
      url: https://b7e2d4f8.exam.otu.ca
      phase: Unlocked
      emailStatus: Sent
      emailSentAt: "2026-04-10T13:30:00-04:00"
      lockedAt: null
      effectiveLockTime: "2026-04-10T17:00:00-04:00"   # accommodation
```

## Post-Exam Retention

After locking, instances remain for grading but are fully network-isolated (deny-all policies in place). The controller sets a `status.retentionDeadline` to 7 days after lock time. After the deadline, `kubectl describe exam` shows a warning. The instructor triggers teardown manually:

```bash
kubectl annotate exam sofe4790u-midterm exam.otu.ca/teardown=confirmed
```

This avoids leaving intentionally vulnerable workloads running indefinitely.

## Project Structure

```
exam-controller/
├── api/v1alpha1/            # CRD type definitions
├── internal/
│   ├── controller/          # Reconciliation loop
│   ├── network/             # NetworkPolicy create/toggle/delete
│   ├── provisioner/         # Per-student Deployment/Service/Ingress
│   ├── smoketest/           # Cluster-internal health/DNS/TLS checks
│   ├── notifier/            # SMTP email delivery
│   └── webhook/             # Validating webhook for spec immutability
├── config/
│   ├── crd/                 # Generated CRD manifests
│   ├── rbac/                # Controller RBAC (scoped to exam namespace + specific Secrets)
│   ├── webhook/             # Webhook configuration
│   └── samples/             # Example Exam manifests
├── cmd/main.go              # Entrypoint
├── Dockerfile
├── Makefile
└── go.mod
```

### Tech Stack

- Go with controller-runtime (Kubebuilder)
- `net/smtp` for email
- No database, no web framework

## Decisions

- **Network-level locking over auth proxy**: Simpler, fewer moving parts, leverages native K8s primitives.
- **Single unlock window, per-student lock times**: Accommodated students get a later lock time via `lockOverride` without being identifiable.
- **PreProvisioned only**: OnDemand rejected — too many failure modes on exam day's critical path.
- **Random slugs over student IDs in URLs**: Privacy for accommodated students. Generated with `crypto/rand`.
- **Dry run without removing NetworkPolicies**: Tests run cluster-internally to prevent early-access hole.
- **Email 30 min before unlock**: Limits URL exposure window vs. sending at provisioning time.
- **Delete Ingress at lock time**: Kills established connections that survive NetworkPolicy changes.
- **Manual teardown with retention warning**: Prevents indefinite vulnerable workload exposure.
- **One namespace per exam**: Clean isolation boundary, working owner references, easy nuclear cleanup.
- **Spec immutability after provisioning**: Validating webhook prevents accidental disruption of live exams.
