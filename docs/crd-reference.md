# Exam CRD Reference

The `Exam` custom resource defines the complete lifecycle of a scheduled exam environment: instance provisioning, network policy enforcement, email notifications, dry-run smoke tests, timed unlock/lock transitions, retention, and teardown. This document covers every field in `ExamSpec` and `ExamStatus`.

**API group:** `exam.otu.ca`
**Version:** `v1alpha1`
**Kind:** `Exam`
**Scope:** Namespaced (`Exam` objects typically live in a control namespace such as `exam-system`; the controller creates a dedicated namespace per exam)

---

## Spec Fields

### Template (`spec.template`)

Defines the pod template used for each student and spare instance.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `spec.template.image` | `string` | Yes | -- | Container image for the vulnerable application. |
| `spec.template.resources` | `corev1.ResourceRequirements` | No | -- | CPU/memory requests and limits for each pod. Uses the standard Kubernetes [ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcerequirements-v1-core) schema. |
| `spec.template.port` | `int32` | Yes | -- | Container port the application listens on. Used by the Service and Ingress created for each instance. |

**Validation rules:**

- `spec.template.image` is immutable after provisioning (any phase past `Pending`).
- `spec.template.port` is immutable after provisioning.
- `spec.template.resources` is immutable after provisioning.

---

### Schedule (`spec.schedule`)

Controls the exam timeline: when provisioning starts, when the exam unlocks/locks, and how long resources are retained.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `spec.schedule.unlock` | `string` (RFC 3339 timestamp) | Yes | -- | The moment the exam unlocks and students gain network access. Example: `"2026-04-10T14:00:00-04:00"`. |
| `spec.schedule.duration` | `string` (duration, e.g. `"2h"`) | Yes | -- | Base exam duration. Must be greater than 0. |
| `spec.schedule.timeMultiplier` | `float64` | No | `1.5` | Multiplier applied to `duration` to compute the lock time. `lockTime = unlock + (duration * timeMultiplier)`. Must be >= 1.0. |
| `spec.schedule.provisionBefore` | `string` (duration) | No | `"1h"` | How far before `unlock` to begin provisioning instances. Must be greater than `spec.email.before`. |
| `spec.schedule.retention` | `string` (duration) | No | `"24h"` | How long after lock time to retain pods before teardown. Platform teams can increase this later to preserve an exam environment for investigation. |
| `spec.schedule.dryRun` | `object` | No | -- | If set, configures a pre-exam smoke test. See [Dry Run](#dry-run-specscheduledryrun) below. |

**Validation rules:**

- `spec.schedule.duration` must be > 0.
- `spec.schedule.timeMultiplier` must be >= 1.0 (defaults to 1.5 if unset or zero).
- `spec.schedule.provisionBefore` must be strictly greater than `spec.email.before`.
- `spec.schedule.unlock` is immutable after provisioning.
- `spec.schedule.duration` is immutable after locking (phases `Locked` or `TearingDown`).
- `spec.schedule.timeMultiplier` is immutable after locking.
- While the exam is `Unlocked`, changes to `duration` or `timeMultiplier` are allowed only if the resulting computed lock time is still in the future.

---

### Dry Run (`spec.schedule.dryRun`)

Optional pre-exam smoke test that verifies all student instances are reachable and network policies are enforced.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `spec.schedule.dryRun.before` | `string` (duration) | Yes | -- | How far before `unlock` to start the dry run. |

---

### Email (`spec.email`)

Configures email delivery to students and the designated course contact.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `spec.email.before` | `string` (duration) | No | `"30m"` | How far before `unlock` to begin sending student emails. Must allow enough time to send all emails at the configured rate. |
| `spec.email.sendInterval` | `string` (duration) | No | `"1s"` | Delay between sending each email. |
| `spec.email.instructorEmail` | `string` | Yes | -- | Email address for course-staff notifications (spare URLs, unlock summary, lock summary). |
| `spec.email.from` | `string` | Yes | -- | Sender address for all outgoing emails. |
| `spec.email.subject` | `string` | Yes | -- | Subject line for student credential emails. |

**Validation rules:**

- `spec.email.instructorEmail` is required (must be non-empty).
- `spec.email.before` must be long enough to deliver all emails at the given `sendInterval`. The webhook enforces: `before >= len(students) * sendInterval * 1.5`.

---

### Students (`spec.students`)

List of students participating in the exam. Each student gets a dedicated pod, service, and ingress.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `spec.students` | `[]object` | Yes | -- | Must contain at least one entry. |
| `spec.students[].id` | `string` | Yes | -- | Unique student identifier. Must be a valid Kubernetes label value (alphanumeric, `-`, `_`, `.`, max 63 characters). Used to generate the instance slug. |
| `spec.students[].email` | `string` | Yes | -- | Student email address where instance credentials are sent. |

**Validation rules:**

- `spec.students` must have at least one entry.
- Each `spec.students[].id` must be a valid Kubernetes label value.
- The student list length is immutable after provisioning.
- Individual `spec.students[].id` values are immutable after provisioning.

---

### Other Spec Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `spec.spares` | `int` | No | `0` | Number of spare instances to provision. Spare URLs are emailed to the course contact once all instances are healthy. |

**Validation rules:**

- `spec.spares` must be >= 0.
- `spec.spares` is immutable after provisioning.

Routing and SMTP settings are controller-level platform configuration, not `ExamSpec` fields. Set them with deployment flags or Helm values such as `platform.baseDomain`, `platform.ingressTLSSecretName`, `platform.smtpSecretName`, and `platform.secretNamespace`.

---

## Status Fields

All status fields are set by the controller and should be treated as read-only.

### Top-Level Status

| Field | Type | Description |
|-------|------|-------------|
| `status.phase` | `string` | Current lifecycle phase. One of: `Pending`, `Provisioning`, `Ready`, `Unlocked`, `Locked`, `TearingDown`. |
| `status.message` | `string` | Human-readable message describing the current state (e.g., `"Exam in progress -- 30 students, 2 spares"`). |
| `status.conditions` | `[]metav1.Condition` | Standard Kubernetes conditions. See [Condition Types](#condition-types) below. |
| `status.computedLockTime` | `string` (RFC 3339) | Computed lock time: `unlock + (duration * timeMultiplier)`. |
| `status.provisionTime` | `string` (RFC 3339) | Computed time when provisioning begins: `unlock - provisionBefore`. |
| `status.emailTime` | `string` (RFC 3339) | Computed time when email delivery begins: `unlock - email.before`. |
| `status.retentionDeadline` | `string` (RFC 3339) | Computed time when teardown begins: `computedLockTime + retention`. |

---

### Dry Run Status (`status.dryRun`)

Present only when `spec.schedule.dryRun` is configured and the dry run has executed.

| Field | Type | Description |
|-------|------|-------------|
| `status.dryRun.completedAt` | `string` (RFC 3339) | Timestamp when the dry run finished. |
| `status.dryRun.passed` | `int` | Number of instances that passed the smoke test. |
| `status.dryRun.failed` | `int` | Number of instances that failed. |
| `status.dryRun.failures` | `[]object` | Details of each failure. |
| `status.dryRun.failures[].student` | `string` | Student ID (or spare slug) of the failed instance. |
| `status.dryRun.failures[].error` | `string` | Error message describing the failure. |

---

### Student Status (`status.students`)

One entry per student, tracking instance and email state.

| Field | Type | Description |
|-------|------|-------------|
| `status.students[].id` | `string` | Student ID matching `spec.students[].id`. |
| `status.students[].slug` | `string` | Generated slug used for the pod, service, and ingress hostname. |
| `status.students[].url` | `string` | Full URL for the student's exam instance (e.g., `https://<slug>.<domain>`). |
| `status.students[].phase` | `string` | Instance phase: `Provisioned`, `Unlocked`, `Locked`, or `Failed`. |
| `status.students[].emailStatus` | `string` | Email delivery state: `Pending`, `Sent`, or `Failed`. |
| `status.students[].emailSentAt` | `string` (RFC 3339) | Timestamp when the credential email was sent. |

---

### Spare Status (`status.spares`)

One entry per spare instance.

| Field | Type | Description |
|-------|------|-------------|
| `status.spares[].slug` | `string` | Generated slug for the spare instance. |
| `status.spares[].url` | `string` | Full URL for the spare instance. |
| `status.spares[].phase` | `string` | Instance phase: `Provisioned`, `Unlocked`, `Locked`, or `Failed`. |

---

### Metrics Summary (`status.metrics`)

Aggregated counts for dashboard queries and Prometheus metrics.

| Field | Type | Description |
|-------|------|-------------|
| `status.metrics.totalStudents` | `int` | Total number of student instances. |
| `status.metrics.totalSpares` | `int` | Total number of spare instances. |
| `status.metrics.emailsSent` | `int` | Number of student emails successfully sent. |
| `status.metrics.emailsFailed` | `int` | Number of student emails that failed to send. |
| `status.metrics.instancesHealthy` | `int` | Number of instances currently healthy. |
| `status.metrics.instancesFailed` | `int` | Number of instances in a failed state. |

---

## Condition Types

The controller sets the following conditions on `status.conditions`. Each condition follows the standard Kubernetes `metav1.Condition` schema with `type`, `status` (`True`/`False`), `reason`, `message`, and `lastTransitionTime`.

| Condition Type | Set When | Reason Values | Description |
|----------------|----------|---------------|-------------|
| `Provisioned` | All student and spare pods are healthy. | `AllHealthy` | Indicates provisioning is complete. Transitions the exam to `Ready` phase. |
| `ProvisioningDegraded` | One or more instances failed to provision. | `SomeInstancesFailed` | Warning condition; the controller retries on subsequent reconciliations. |
| `AllEmailsSent` | All student credential emails have been sent (or failed). | `Complete` | Set during `Ready` phase once the email queue is drained. |
| `DryRunComplete` | The dry-run smoke test has finished executing. | `Complete` | Set regardless of whether all checks passed or some failed. |
| `DryRunFailed` | One or more dry-run checks failed. | `SomeFailed` | Message includes the count of failures (e.g., `"3 of 30 checks failed"`). |
| `NetworkPolicyEnforced` | The dry run has tested network policy enforcement. | `Verified` / `NotEnforced` | `True` if the negative connectivity test confirmed policies block traffic; `False` otherwise. |
| `InstructorNotifiedUnlock` | The unlock notification email was sent to the course contact. | `Sent` | Includes a summary of student count, spare count, and any failed emails. |
| `InstructorNotifiedLock` | The lock notification email was sent to the course contact. | `Sent` | Includes a summary of healthy vs. failed instances at lock time. |

---

## Exam Phases

| Phase | Description |
|-------|-------------|
| `Pending` | The Exam resource has been created but provisioning has not started (waiting for `provisionTime`). |
| `Provisioning` | Pods, services, and network policies are being created in the exam namespace. |
| `Ready` | All instances are healthy. Emails and dry runs execute during this phase. Waiting for `unlock` time. |
| `Unlocked` | The exam is in progress. Ingress resources are created and network policies allow student traffic. |
| `Locked` | The exam has ended. Ingress resources are deleted and network policies block traffic. Instances are retained until `retentionDeadline`. |
| `TearingDown` | The exam namespace is being deleted. Terminal state. |

---

## Complete Example

```yaml
apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: sofe4790u-midterm
  labels:
    app.kubernetes.io/name: exam-controller
    app.kubernetes.io/managed-by: kustomize
spec:
  # --- Pod template for each student instance ---
  template:
    image: registry.example.com/vuln-app:v2.1  # Vulnerable application image
    resources:                                   # Standard K8s resource constraints
      requests:
        cpu: "250m"
        memory: "256Mi"
      limits:
        cpu: "500m"
        memory: "512Mi"
    port: 8080                                   # Container port exposed via Service/Ingress

  # --- Exam schedule ---
  schedule:
    unlock: "2026-04-10T14:00:00-04:00"          # When the exam becomes accessible (RFC 3339)
    duration: "2h"                                # Base exam length
    timeMultiplier: 1.5                           # Lock at unlock + (2h * 1.5) = unlock + 3h
    provisionBefore: "1h"                         # Start provisioning 1h before unlock
    retention: "24h"                              # Keep pods 24h after lock for investigation
    dryRun:                                       # Optional pre-exam smoke test
      before: "5m"                                #   Run 5 minutes before unlock

  # --- Email settings ---
  email:
    before: "30m"                                 # Start sending emails 30m before unlock
    sendInterval: "1s"                             # 1 second between emails
    instructorEmail: "instructor@ontariotechu.net" # Receives spare URLs + unlock/lock summaries
    from: "noreply@otu.ca"                        # Sender address
    subject: "SOFE4790U - Your Exam Instance"     # Email subject line

  # --- Student list ---
  students:
    - id: john.smith                              # Must be a valid K8s label value
      email: john.smith@ontariotechu.net
    - id: jane.doe
      email: jane.doe@ontariotechu.net

  # --- Spare instances ---
  spares: 2                                       # Number of hot-spare instances
```

`Exam` resources do not carry platform-wide routing or SMTP configuration. The controller provides the base domain, wildcard TLS secret, SMTP secret name, and secret namespace via deployment flags or Helm values.

### Resulting Timeline

Given the example above:

| Event | Time |
|-------|------|
| Provisioning starts | `2026-04-10T13:00:00-04:00` (unlock - 1h) |
| Emails begin | `2026-04-10T13:30:00-04:00` (unlock - 30m) |
| Dry run executes | `2026-04-10T13:55:00-04:00` (unlock - 5m) |
| Exam unlocks | `2026-04-10T14:00:00-04:00` |
| Exam locks | `2026-04-10T17:00:00-04:00` (unlock + 2h * 1.5) |
| Teardown begins | `2026-04-11T17:00:00-04:00` (lock + 24h) |
