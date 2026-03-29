# Platform Guide: Managing Exams

This guide is for the infra team that creates and manages `Exam` resources in consultation with course staff. Instructors provide the roster, timing, and application requirements; the platform team owns the Kubernetes objects.

## What the operator handles

Once an `Exam` is applied, the controller:

1. Provisions one isolated instance per student plus any configured spares.
2. Sends unique URLs to students and summary emails to the course contact.
3. Keeps instances inaccessible until the scheduled unlock time.
4. Locks access when the exam window ends.
5. Retains instances for the configured investigation window.
6. Deletes the exam namespace automatically after retention expires.

## Inputs to collect

Before creating an `Exam`, gather:

| Input | Example | Source |
|---|---|---|
| Container image | `registry.example.com/vuln-app:v2.1` | Course team |
| Application port | `8080` | Image documentation |
| Unlock time | `2026-04-10T14:00:00-04:00` | Course team |
| Base duration | `2h` | Course team |
| Student roster | `id` + `email` pairs | Course team / registrar |
| Spare count | `2` | Infra policy |
| Course contact email | `instructor@ontariotechu.net` | Course team |

Platform prerequisites are configured once on the controller deployment:

| Setting | Example | Source |
|---|---|---|
| Base domain | `science.ontariotechu.ca` | Platform |
| SMTP secret name | `exam-smtp-credentials` | Platform |
| Wildcard TLS secret name (optional) | `exam-wildcard-tls` | Platform (only if not using cert-manager/Cilium for TLS) |
| Platform secret namespace | `exam-controller-system` | Platform |

## Example manifest

```yaml
apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: sofe4790u-midterm
  namespace: exam-system
spec:
  template:
    image: registry.example.com/vuln-app:v2.1
    port: 8080
    resources:
      requests:
        cpu: "250m"
        memory: "256Mi"
      limits:
        cpu: "500m"
        memory: "512Mi"
  schedule:
    unlock: "2026-04-10T14:00:00-04:00"
    duration: "2h"
    timeMultiplier: 1.5
    provisionBefore: "1h"
    retention: "24h"
    dryRun:
      before: "5m"
  email:
    before: "30m"
    sendInterval: "1s"
    instructorEmail: instructor@ontariotechu.net
    from: "noreply@otu.ca"
    subject: "SOFE4790U Midterm - Your Exam Instance"
  students:
    - id: john.smith
      email: john.smith@ontariotechu.net
    - id: jane.doe
      email: jane.doe@ontariotechu.net
  spares: 2
```

The controller supplies the base domain and SMTP credentials from its own deployment configuration. A wildcard TLS secret is optional; by default, TLS is handled by cert-manager or Cilium.

Apply it with:

```sh
kubectl apply -f my-exam.yaml
```

## Monitoring

Check the overall phase:

```sh
kubectl get exam sofe4790u-midterm -n exam-system
```

Inspect detailed status:

```sh
kubectl get exam sofe4790u-midterm -n exam-system -o yaml
```

Useful fields:

| Field | Meaning |
|---|---|
| `status.phase` | Current lifecycle phase |
| `status.students[].url` | Student URLs |
| `status.students[].emailStatus` | `Pending`, `Sent`, or `Failed` |
| `status.spares[].url` | Spare instance URLs |
| `status.metrics.instancesHealthy` | Healthy instance count |
| `status.retentionDeadline` | When teardown will begin |
| `status.conditions` | Provisioning, email, dry-run, and notification state |

## Supported operator actions

These are the supported manual changes to an `Exam` resource:

| Goal | Supported action |
|---|---|
| Fix a student email before provisioning starts | Update `spec.students[].email` |
| Extend an exam already in progress | Increase `spec.schedule.duration` or `spec.schedule.timeMultiplier` while phase is `Unlocked` |
| Keep instances longer for an investigation | Increase `spec.schedule.retention` |
| Cancel the exam and remove all resources | `kubectl delete exam <name> -n exam-system` |

The operator does not provide a supported first-class "force unlock" or "force lock" API. Avoid patching `.status.phase` directly; that bypasses the intended lifecycle and is not part of the supported contract.

## Common situations

If a student did not receive email:

```sh
kubectl get exam sofe4790u-midterm -n exam-system \
  -o jsonpath='{range .status.students[*]}{.id}: {.url} ({.emailStatus}){"\n"}{end}'
```

If a student needs a spare:

```sh
kubectl get exam sofe4790u-midterm -n exam-system \
  -o jsonpath='{range .status.spares[*]}{.url}{"\n"}{end}'
```

If you need to preserve the environment longer:

```sh
kubectl patch exam sofe4790u-midterm -n exam-system --type=merge \
  -p '{"spec":{"schedule":{"retention":"72h"}}}'
```

If the exam must be extended while it is live:

```sh
kubectl patch exam sofe4790u-midterm -n exam-system --type=merge \
  -p '{"spec":{"schedule":{"duration":"3h","timeMultiplier":1.5}}}'
```

## Access model

This repository does not assume direct instructor write access to `Exam` resources. Any RBAC bindings for course staff should be created intentionally by the infra team rather than shipped as part of the default install.
