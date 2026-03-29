# Operational Runbook

This document covers deployment, monitoring, troubleshooting, and supported operational procedures for the exam-controller.

---

## 1. Deployment

### Prerequisites

- Kubernetes 1.28+
- An Ingress controller (ingress-nginx expected by default NetworkPolicy rules)
- A wildcard TLS certificate for your exam domain (e.g. `*.exam.otu.ca`)
- An SMTP server for email delivery
- (Optional) Prometheus + Grafana for monitoring
- (Optional) Cilium CNI for CiliumNetworkPolicy support (auto-detected at startup)

### Namespace Setup

The controller itself runs in `exam-controller-system` (Kustomize) or whatever namespace you install the Helm release into. Each Exam CR creates a dedicated namespace named `exam-<exam-name>-<hash>` containing all student resources. The hash makes namespaces stable and collision-safe when two Exams share the same name in different Kubernetes namespaces.

### RBAC Overview

The controller requires a ClusterRole with the following permissions:

| API Group          | Resources                             | Verbs                                     |
|--------------------|---------------------------------------|--------------------------------------------|
| `""`               | namespaces, services                  | get, list, watch, create, update, patch, delete |
| `""`               | secrets                               | get, list, watch, create, update, patch    |
| `apps`             | deployments                           | get, list, watch, create, update, patch, delete |
| `networking.k8s.io`| ingresses, networkpolicies            | get, list, watch, create, update, patch, delete |
| `cilium.io`        | ciliumnetworkpolicies                 | get, list, watch, create, update, patch, delete |
| `exam.otu.ca`      | exams, exams/status, exams/finalizers | full CRUD                                  |

The default install ships only the RBAC required by the controller and its metrics endpoint. Bind any additional read or write access to `Exam` resources explicitly for your own platform team workflows.

### Platform Secrets

The controller reads two platform-managed secrets from its configured secret namespace:

1. **Wildcard certificate** -- `--ingress-tls-secret-name` points to a TLS secret covering `*.exam.otu.ca` (or your chosen base domain). The controller copies this secret into each exam namespace before creating ingresses.
2. **SMTP credentials** -- `--smtp-secret-name` points to a secret with `host`, `port`, `username`, and `password` keys. The `port` defaults to 587 if missing.

Example:

```bash
kubectl create secret tls exam-wildcard-tls \
  --cert=wildcard.crt --key=wildcard.key \
  -n exam-controller-system

kubectl create secret generic exam-smtp-credentials \
  --from-literal=host=smtp.example.com \
  --from-literal=port=587 \
  --from-literal=username=noreply@example.com \
  --from-literal=password=changeme \
  -n exam-controller-system
```

Metrics and webhook certificates remain separate. The controller can generate self-signed certs by default; for production, use cert-manager or pass `--metrics-cert-path` / `--webhook-cert-path`.

### Kustomize Deployment

```bash
# Install CRDs
make install

# Deploy controller to exam-controller-system namespace
make deploy IMG=ghcr.io/rdrake/exam-controller:latest

# Verify
kubectl get pods -n exam-controller-system
```

Key startup flags (configured in `config/default/manager_metrics_patch.yaml`):

| Flag                         | Default    | Description                            |
|------------------------------|------------|----------------------------------------|
| `--metrics-bind-address`     | `0`        | Metrics listen address (`:8443` for HTTPS, `:8080` for HTTP) |
| `--health-probe-bind-address`| `:8081`    | Liveness/readiness probe address       |
| `--leader-elect`             | `false`    | Enable leader election for HA          |
| `--base-domain`              | `exam.otu.ca` | Base domain for student URLs and Ingress hosts |
| `--ingress-tls-secret-name`  | `exam-wildcard-tls` | Wildcard TLS secret copied into exam namespaces |
| `--smtp-secret-name`         | `exam-smtp-credentials` | SMTP credentials secret name |
| `--platform-secret-namespace`| `POD_NAMESPACE` or `exam-system` | Namespace holding platform secrets |
| `--metrics-secure`           | `true`     | Serve metrics over HTTPS               |
| `--enable-http2`             | `false`    | Enable HTTP/2 (disabled by default for CVE mitigation) |

### Helm Deployment

```bash
helm install exam-controller charts/exam-controller \
  --namespace exam-controller-system --create-namespace \
  --set image.tag=v0.1.0 \
  --set leaderElection.enabled=true \
  --set metrics.serviceMonitor.enabled=true
```

Key Helm values:

| Value                            | Default                              | Description                        |
|----------------------------------|--------------------------------------|------------------------------------|
| `image.repository`               | `ghcr.io/rdrake/exam-controller`     | Controller image                   |
| `image.tag`                      | Chart appVersion                     | Image tag                          |
| `leaderElection.enabled`         | `true`                               | Leader election for HA             |
| `metrics.enabled`                | `true`                               | Enable metrics endpoint            |
| `metrics.port`                   | `8443`                               | Metrics port                       |
| `metrics.secure`                 | `true`                               | HTTPS metrics                      |
| `metrics.serviceMonitor.enabled` | `false`                              | Create Prometheus ServiceMonitor   |
| `metrics.serviceMonitor.interval`| `30s`                                | Scrape interval                    |
| `webhook.enabled`                | `false`                              | Enable admission webhooks          |
| `platform.baseDomain`            | `exam.otu.ca`                        | Base domain for student URLs       |
| `platform.ingressTLSSecretName`  | `exam-wildcard-tls`                  | Wildcard TLS secret name           |
| `platform.smtpSecretName`        | `exam-smtp-credentials`              | SMTP credentials secret name       |
| `platform.secretNamespace`       | `""`                                 | Namespace holding platform secrets |
| `healthProbe.port`               | `8081`                               | Health probe port                  |
| `resources.requests.cpu`         | `10m`                                | CPU request                        |
| `resources.requests.memory`      | `64Mi`                               | Memory request                     |
| `resources.limits.cpu`           | `500m`                               | CPU limit                          |
| `resources.limits.memory`        | `128Mi`                              | Memory limit                       |
| `networkPolicy.enabled`          | `false`                              | Controller pod NetworkPolicy       |

---

## 2. Day-2 Operations -- Prometheus Metrics

The controller exposes 12 metrics on its metrics endpoint. All per-exam metrics are labeled with both `exam="<exam-name>"` and `namespace="<exam-cr-namespace>"`.

### Metric Reference

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `exam_reconcile_duration_seconds` | Histogram | (none) | Time spent per reconcile loop |
| `exam_reconcile_errors_total` | Counter | (none) | Total reconcile failures |
| `exam_phase_transitions_total` | Counter | `exam`, `namespace`, `from`, `to` | Phase changes by exam |
| `exam_instances_total` | Gauge | `exam`, `namespace` | Total instances (students + spares) |
| `exam_instances_healthy` | Gauge | `exam`, `namespace` | Instances passing health checks |
| `exam_instances_failed` | Gauge | `exam`, `namespace` | Instances in failed state |
| `exam_emails_sent_total` | Counter | `exam`, `namespace` | Emails successfully delivered |
| `exam_emails_failed_total` | Counter | `exam`, `namespace` | Email delivery failures |
| `exam_dryrun_passed` | Gauge | `exam`, `namespace` | Dry run checks that passed |
| `exam_dryrun_failed` | Gauge | `exam`, `namespace` | Dry run checks that failed |
| `exam_seconds_until_unlock` | Gauge | `exam`, `namespace` | Seconds remaining until unlock (0 after) |
| `exam_seconds_until_lock` | Gauge | `exam`, `namespace` | Seconds remaining until lock (0 after) |

When an exam is torn down, all its label series are cleaned up via `CleanupExam()` to prevent unbounded cardinality growth.

### PromQL Alerting Queries

**Reconcile error rate (>5% over 5 minutes):**

```promql
rate(exam_reconcile_errors_total[5m])
  / rate(exam_reconcile_duration_seconds_count[5m]) > 0.05
```

**Reconcile loop taking too long (p99 > 10s):**

```promql
histogram_quantile(0.99, rate(exam_reconcile_duration_seconds_bucket[5m])) > 10
```

**Exam stuck in Provisioning for over 15 minutes:**

```promql
exam_phase_transitions_total{to="Provisioning"}
  unless exam_phase_transitions_total{from="Provisioning"}
```

Combined with a time-based approach, check for exams that have been provisioning longer than expected:

```promql
exam_instances_healthy < exam_instances_total
  and exam_seconds_until_unlock < 3600
```

**Failed instances detected:**

```promql
exam_instances_failed > 0
```

**Email delivery failures:**

```promql
exam_emails_failed_total > 0
```

**Dry run failures:**

```promql
exam_dryrun_failed > 0
```

**Exam approaching unlock with unhealthy instances:**

```promql
exam_seconds_until_unlock < 900
  and exam_instances_healthy < exam_instances_total
```

**Exam about to lock (less than 5 minutes):**

```promql
exam_seconds_until_lock > 0 and exam_seconds_until_lock < 300
```

### Dashboard Panels

Useful Grafana panel queries:

```promql
# Active exams by phase
count by (to) (exam_phase_transitions_total)

# Email success rate per exam
exam_emails_sent_total / (exam_emails_sent_total + exam_emails_failed_total)

# Instance health ratio
exam_instances_healthy / exam_instances_total

# Reconcile latency heatmap
rate(exam_reconcile_duration_seconds_bucket[5m])
```

---

## 3. Troubleshooting

### Exam stuck in Provisioning

**Symptoms:** Exam `status.phase` is `Provisioning` and has not transitioned to `Ready` within the expected time.

**Diagnosis:**

```bash
EXAM_NS=$(kubectl get ns -l exam.otu.ca/exam=<name>,exam.otu.ca/exam-namespace=exam-system -o jsonpath='{.items[0].metadata.name}')

# Check exam status
kubectl get exam <name> -n exam-system -o yaml

# Check deployments in exam namespace
kubectl get deployments -n "$EXAM_NS"

# Check pods
kubectl get pods -n "$EXAM_NS"

# Look for pods not reaching Ready
kubectl describe pod -n "$EXAM_NS" -l exam.otu.ca/exam=<name>,exam.otu.ca/exam-namespace=exam-system

# Check controller logs for provisioning errors
kubectl logs -n exam-controller-system deployment/exam-controller-controller-manager \
  | grep -i "failed to provision"
```

**Common causes and resolution:**

- **Image pull errors:** Verify the `spec.template.image` is accessible from the cluster. Check imagePullSecrets if using a private registry.
- **Resource quota exceeded:** Check if the exam namespace has ResourceQuotas blocking pod creation.
- **SecurityContext violations:** The controller sets `runAsNonRoot: true` and drops all capabilities. The container image must support running as non-root.
- **Pod scheduling failures:** Check node resources with `kubectl describe nodes` and look for `Insufficient cpu/memory` events.

**Log patterns to search for:**

```
"Failed to provision student"
"Failed to provision spare"
"SomeInstancesFailed"
```

### Emails failing

**Symptoms:** `status.students[].emailStatus` shows `Failed`. The `exam_emails_failed_total` metric is incrementing.

**Diagnosis:**

```bash
# Check student email statuses
kubectl get exam <name> -n exam-system -o jsonpath='{range .status.students[*]}{.id} {.emailStatus}{"\n"}{end}'

# Verify SMTP secret exists in the controller's platform secret namespace
kubectl get secret exam-smtp-credentials -n exam-controller-system -o yaml

# Check controller logs for SMTP errors
kubectl logs -n exam-controller-system deployment/exam-controller-controller-manager \
  | grep -i "email\|smtp\|send"
```

**Common causes and resolution:**

- **Secret not found:** Verify the controller's `--smtp-secret-name` and `--platform-secret-namespace` settings point to an existing Secret.
- **Wrong credentials:** Check the `username` and `password` keys in the Secret. The controller uses PLAIN auth.
- **Port mismatch:** The Secret `port` defaults to 587 if omitted. Ensure your SMTP server listens on that port.
- **Network connectivity:** The controller pod must be able to reach the SMTP host. Check NetworkPolicies on the controller namespace.
- **Retry exhaustion:** The RetrySender retries up to 3 times with exponential backoff (100ms, 200ms, 400ms). Persistent failures indicate an infrastructure problem, not a transient issue.

**Log patterns to search for:**

```
"reading SMTP secret"
"after 3 retries"
"Failed to send"
```

### Dry run showing failures

**Symptoms:** `status.dryRun.failed > 0` or `status.conditions` contains `DryRunFailed=True`. The condition `NetworkPolicyEnforced` may be `False`.

**Diagnosis:**

```bash
EXAM_NS=$(kubectl get ns -l exam.otu.ca/exam=<name>,exam.otu.ca/exam-namespace=exam-system -o jsonpath='{.items[0].metadata.name}')

# Check dry run results
kubectl get exam <name> -n exam-system -o jsonpath='{.status.dryRun}'

# Check which students failed
kubectl get exam <name> -n exam-system -o jsonpath='{range .status.dryRun.failures[*]}{.student}: {.error}{"\n"}{end}'

# Check conditions
kubectl get exam <name> -n exam-system -o jsonpath='{range .status.conditions[*]}{.type}: {.status} ({.reason}) {.message}{"\n"}{end}'

# Verify pods are running and healthy
kubectl get pods -n "$EXAM_NS" -o wide

# Test connectivity from within the cluster
kubectl run -n "$EXAM_NS" --rm -it debug --image=curlimages/curl -- \
  curl -s -o /dev/null -w '%{http_code}' http://<slug>."$EXAM_NS":<port>
```

**Common causes and resolution:**

- **Pods not ready:** The dry run performs HTTP health checks. If pods are crashlooping or not listening on the expected port, checks will fail.
- **NetworkPolicy enforcement broken:** If `NetworkPolicyEnforced=False`, the negative connectivity test succeeded when it should have been blocked. Verify your CNI supports NetworkPolicy enforcement.
- **Wrong port:** The dry run uses the `spec.template.port` to construct health check URLs. Ensure the container actually listens on that port.

**Log patterns to search for:**

```
"DryRunFailed"
"NetworkPolicy not enforced"
"policyEnforced=false"
```

### Students cannot access instance

**Symptoms:** Students receive their URL but get connection refused, timeout, or TLS errors.

**Diagnosis:**

```bash
EXAM_NS=$(kubectl get ns -l exam.otu.ca/exam=<name>,exam.otu.ca/exam-namespace=exam-system -o jsonpath='{.items[0].metadata.name}')

# Check if Ingress exists (only created during Unlocked phase)
kubectl get ingress -n "$EXAM_NS"

# Verify the exam is in Unlocked phase
kubectl get exam <name> -n exam-system -o jsonpath='{.status.phase}'

# Check TLS secret
kubectl get secret exam-wildcard-tls -n "$EXAM_NS"

# Test DNS resolution
nslookup <slug>.exam.otu.ca

# Check ingress controller logs
kubectl logs -n ingress-nginx deployment/ingress-nginx-controller | grep <slug>

# Verify the ingress-allow NetworkPolicy exists
kubectl get networkpolicy -n "$EXAM_NS" -l exam.otu.ca/slug=<slug>
```

**Common causes and resolution:**

- **Exam not yet unlocked:** Ingress resources are only created when the exam transitions to the `Unlocked` phase. Before unlock, pods are isolated by deny-all NetworkPolicies.
- **TLS secret missing:** Verify the controller's platform wildcard TLS secret exists in the configured secret namespace and that the controller successfully copied it into `$EXAM_NS`.
- **DNS not configured:** A wildcard DNS record (e.g. `*.exam.otu.ca`) must point to the ingress controller's external IP.
- **NetworkPolicy blocking traffic:** The ingress-allow policy permits traffic only from pods in the `ingress-nginx` namespace with label `app.kubernetes.io/name=ingress-nginx`. If your ingress controller uses different labels or a different namespace, traffic will be blocked.
- **Ingress controller not found:** The IngressAllow NetworkPolicy expects the controller in the `ingress-nginx` namespace. Adjust if using a different ingress controller.

### Exam not transitioning phases

**Symptoms:** The exam stays in a phase longer than expected (e.g. stays `Ready` past unlock time).

**Diagnosis:**

```bash
# Check current phase and computed times
kubectl get exam <name> -n exam-system -o jsonpath='Phase: {.status.phase}
Unlock: {.spec.schedule.unlock}
ComputedLock: {.status.computedLockTime}
ProvisionTime: {.status.provisionTime}
RetentionDeadline: {.status.retentionDeadline}'

# Verify controller is running
kubectl get pods -n exam-controller-system

# Check leader election (if enabled)
kubectl get lease -n exam-controller-system

# Check controller logs for reconcile errors
kubectl logs -n exam-controller-system deployment/exam-controller-controller-manager \
  | grep -i "error\|failed\|requeue"

# Check if the controller is watching the correct namespace
kubectl logs -n exam-controller-system deployment/exam-controller-controller-manager \
  | grep "Phase transition"
```

**Common causes and resolution:**

- **Controller not running:** If the controller pod is down, no reconciliation occurs. Check pod status and events.
- **Leader election stuck:** With `--leader-elect=true`, only the leader reconciles. If the leader pod died without releasing the lease, another pod must wait for the lease to expire (default 15s).
- **Clock skew:** Phase transitions depend on comparing `time.Now()` against scheduled times. Ensure cluster nodes have accurate time (NTP).
- **Time zone mismatch:** The `spec.schedule.unlock` time is parsed as-is. Use explicit timezone offsets (e.g. `2026-04-10T14:00:00-04:00`) to avoid ambiguity.
- **Reconcile error loop:** If a phase handler returns an error, the controller retries with backoff. Check logs for the specific error.

### Metrics not showing up

**Symptoms:** Prometheus is not scraping exam metrics, or metrics are missing from Grafana.

**Diagnosis:**

```bash
# Verify the metrics service exists
kubectl get svc -n exam-controller-system -l control-plane=controller-manager

# Check if ServiceMonitor exists (Helm)
kubectl get servicemonitor -n exam-controller-system

# Verify the metrics endpoint is reachable
kubectl port-forward -n exam-controller-system svc/exam-controller-controller-manager-metrics-service 8443:8443
# Then: curl -k https://localhost:8443/metrics

# Check Prometheus targets
kubectl port-forward -n monitoring svc/prometheus-kube-prometheus-prometheus 9090:9090
# Then browse to http://localhost:9090/targets

# Verify RBAC for metrics scraping
kubectl get clusterrolebinding | grep metrics
```

**Common causes and resolution:**

- **Metrics disabled:** Ensure `--metrics-bind-address` is set (not `0`). The Kustomize default patches this to `:8443`.
- **ServiceMonitor not created:** With Helm, set `metrics.serviceMonitor.enabled=true`. With Kustomize, uncomment the PROMETHEUS section in `config/default/kustomization.yaml`.
- **RBAC missing:** Prometheus needs a ClusterRole allowing access to the `/metrics` endpoint. The `metrics-reader` ClusterRole is included but must be bound to the Prometheus service account.
- **TLS mismatch:** When `--metrics-secure=true`, Prometheus must be configured to scrape HTTPS with proper TLS settings. The ServiceMonitor should include TLS configuration.
- **No exams exist:** Per-exam metrics only appear after an Exam CR is created and reconciled. The `exam_reconcile_duration_seconds` and `exam_reconcile_errors_total` metrics appear after the first reconcile.

---

## 4. Operational Procedures

### Supported manual changes

The operator intentionally exposes a narrow control surface. Supported interventions are:

- Updating student email addresses before provisioning starts
- Extending an active exam by adjusting `spec.schedule.duration` or `spec.schedule.timeMultiplier`
- Extending retention by increasing `spec.schedule.retention`
- Cancelling an exam by deleting the `Exam` resource

Avoid patching `.status.phase` or other status fields directly. The controller treats status as its own reconciliation output, not as an operator-facing control API.

### Extend an active exam

While an exam is in `Unlocked`, you can extend the lock time by increasing `duration` or `timeMultiplier`:

```bash
kubectl patch exam <name> -n exam-system --type=merge \
  -p '{"spec":{"schedule":{"duration":"3h","timeMultiplier":1.5}}}'
```

The validating webhook allows this only while the resulting computed lock time remains in the future.

### Extend retention for an investigation

If course staff report an academic issue and the environment must be preserved longer:

```bash
kubectl patch exam <name> -n exam-system --type=merge \
  -p '{"spec":{"schedule":{"retention":"72h"}}}'
```

On the next reconcile, the controller recomputes `status.retentionDeadline` from the new retention value.

### Manual teardown

If the controller is not running or teardown is stuck, manually delete the exam namespace:

```bash
EXAM_NS=$(kubectl get ns -l exam.otu.ca/exam=<name>,exam.otu.ca/exam-namespace=exam-system -o jsonpath='{.items[0].metadata.name}')

# Delete the exam namespace (destroys all student resources)
kubectl delete namespace "$EXAM_NS"

# Remove the finalizer so the Exam CR can be deleted
kubectl patch exam <name> -n exam-system --type=json \
  -p '[{"op":"remove","path":"/metadata/finalizers"}]'

# Delete the Exam CR
kubectl delete exam <name> -n exam-system
```

### Cancel an exam before teardown

To stop managing an exam and remove its resources:

```bash
kubectl delete exam <name> -n exam-system
```

The finalizer deletes the per-exam namespace before the `Exam` resource disappears.

### Recover from controller crash

If the controller pod crashes or is deleted:

```bash
# Check pod status
kubectl get pods -n exam-controller-system

# Check events
kubectl get events -n exam-controller-system --sort-by=.metadata.creationTimestamp

# If the pod is in CrashLoopBackOff, check logs
kubectl logs -n exam-controller-system deployment/exam-controller-controller-manager --previous

# If leader election is stuck, delete the lease to force re-election
kubectl delete lease 70624ff0.otu.ca -n exam-controller-system
```

The controller is designed to be crash-safe. On restart it will:

1. Re-read all Exam CRs and recompute their desired phase.
2. Resume from the current phase -- it will not re-send emails already marked as `Sent`.
3. Re-apply any missing NetworkPolicies (drift correction runs every reconcile in Ready, Unlocked, and Locked phases).
4. Continue countdown timers based on the original schedule times, not elapsed time.

### Reassign a failed instance

If a student's instance fails and you need to give them a spare:

```bash
# Identify available spare URLs
kubectl get exam <name> -n exam-system -o jsonpath='{range .status.spares[*]}{.slug} {.url} {.phase}{"\n"}{end}'

# Manually share the spare URL with the student
# The configured course contact is also emailed spare URLs when provisioning completes
```

---

## 5. Scaling

### Controller resource limits

The default Helm values allocate minimal resources suitable for small deployments:

```yaml
resources:
  requests:
    cpu: 10m
    memory: 64Mi
  limits:
    cpu: 500m
    memory: 128Mi
```

**Scaling guidance:**

| Concurrent exams | Students total | Recommended CPU (limit) | Recommended memory (limit) |
|-------------------|----------------|--------------------------|----------------------------|
| 1--3              | < 100          | 500m                     | 128Mi                      |
| 3--10             | 100--500       | 1000m                    | 256Mi                      |
| 10+               | 500+           | 2000m                    | 512Mi                      |

The controller is single-threaded per reconcile (one Exam at a time) but processes multiple Exams concurrently through the controller-runtime work queue.

### Handling many concurrent exams

Each exam creates:

- 1 namespace
- N+S Deployments (students + spares)
- N+S Services
- N+S Ingresses (only during Unlocked phase)
- 2*(N+S) to 3*(N+S) NetworkPolicies (deny-all + egress-allow, plus ingress-allow when unlocked)

For a 50-student exam with 5 spares, that is 55 Deployments, 55 Services, and 110--165 NetworkPolicies.

**Recommendations:**

- Enable leader election (`--leader-elect=true`) and run 2 replicas for HA. Only the leader reconciles, but a standby takes over immediately on failure.
- Monitor `exam_reconcile_duration_seconds` -- if p99 exceeds a few seconds, the controller is under load.
- Ensure the Kubernetes API server can handle the object count. Each reconcile loop may list Deployments across exam namespaces.
- Consider staggering exam start times to avoid provisioning storms.

### Email send interval

The controller sends emails one at a time, controlled by `spec.email.sendInterval`:

```yaml
email:
  sendInterval: "1s"  # 1 second between emails
```

With `sendInterval: "1s"` and 100 students, email delivery takes approximately 100 seconds. The controller requeues after each send with the configured delay.

**Considerations:**

- Most SMTP providers have rate limits (e.g. 10--30 messages/second for institutional relays). Set `sendInterval` above the inverse of your provider's limit (e.g. `"100ms"` for 10/s).
- Failed emails are retried 3 times with exponential backoff (100ms, 200ms, 400ms) before being marked as `Failed`. They are not retried again automatically.
- Students with `Failed` email status must be notified manually. The configured course contact receives a list of failed deliveries in the unlock notification email.
- For large classes (200+ students), consider setting `email.before` to `1h` or more to allow time for all emails to be sent before unlock.

### Node capacity planning

Each student instance runs as a single-replica Deployment. Plan node capacity based on the container resource requests specified in `spec.template.resources`:

```bash
# Example: 50 students, each requesting 250m CPU and 256Mi memory
# Total: 12.5 CPU, 12.5 GiB memory (plus spares)
```

Use Kubernetes pod topology spread constraints or node affinity at the cluster level to distribute exam pods across nodes for resilience.
