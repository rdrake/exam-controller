# Helm Chart, CI Pipeline, E2E Tests, and Documentation

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Package the exam-controller with a Helm chart, add CI build/push/release workflows, expand E2E tests to cover the full exam lifecycle, and write comprehensive documentation.

**Architecture:** Four sequential workstreams — Helm chart mirrors the existing Kustomize config, CI adds image build/push and release automation on top of existing lint/test/e2e workflows, E2E tests exercise the 6-phase state machine with injectable time, and docs cover CRD reference plus operational runbook.

**Tech Stack:** Helm 3, GitHub Actions, Ginkgo v2 + envtest, MkDocs or plain Markdown

---

## Workstream 1: Helm Chart

### Task 1: Scaffold Helm chart structure

**Files:**
- Create: `charts/exam-controller/Chart.yaml`
- Create: `charts/exam-controller/values.yaml`
- Create: `charts/exam-controller/.helmignore`

**Step 1: Create Chart.yaml**

```yaml
apiVersion: v2
name: exam-controller
description: A Kubernetes operator for managing penetration-testing exam instances
type: application
version: 0.1.0
appVersion: "0.1.0"
keywords:
  - kubernetes
  - operator
  - exam
  - education
home: https://github.com/rdrake/exam-controller
sources:
  - https://github.com/rdrake/exam-controller
maintainers:
  - name: rdrake
```

**Step 2: Create values.yaml**

```yaml
replicaCount: 1

image:
  repository: ghcr.io/rdrake/exam-controller
  tag: ""  # defaults to .Chart.AppVersion
  pullPolicy: IfNotPresent

imagePullSecrets: []
nameOverride: ""
fullnameOverride: ""

serviceAccount:
  create: true
  annotations: {}
  name: ""

leaderElection:
  enabled: true

metrics:
  enabled: true
  port: 8443
  secure: true
  serviceMonitor:
    enabled: false
    interval: 30s

webhook:
  enabled: false
  certDir: ""
  certName: tls.crt
  keyName: tls.key

healthProbe:
  port: 8081

resources:
  requests:
    cpu: 10m
    memory: 64Mi
  limits:
    cpu: 500m
    memory: 128Mi

nodeSelector: {}
tolerations: []
affinity: {}

networkPolicy:
  enabled: false
  metricsNamespaceSelector:
    metrics: enabled
```

**Step 3: Create .helmignore**

```
.git/
.gitignore
*.swp
*.bak
*.tmp
```

**Step 4: Verify structure**

Run: `ls -R charts/exam-controller/`
Expected: Chart.yaml, values.yaml, .helmignore listed

---

### Task 2: Create Helm template helpers

**Files:**
- Create: `charts/exam-controller/templates/_helpers.tpl`

**Step 1: Write _helpers.tpl**

```gotemplate
{{/*
Expand the name of the chart.
*/}}
{{- define "exam-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "exam-controller.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "exam-controller.labels" -}}
helm.sh/chart: {{ include "exam-controller.chart" . }}
{{ include "exam-controller.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "exam-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "exam-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{/*
Chart label.
*/}}
{{- define "exam-controller.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Service account name.
*/}}
{{- define "exam-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "exam-controller.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Manager container image.
*/}}
{{- define "exam-controller.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) }}
{{- end }}
```

---

### Task 3: Create core RBAC templates

**Files:**
- Create: `charts/exam-controller/templates/serviceaccount.yaml`
- Create: `charts/exam-controller/templates/clusterrole.yaml`
- Create: `charts/exam-controller/templates/clusterrolebinding.yaml`
- Create: `charts/exam-controller/templates/leader-election-role.yaml`
- Create: `charts/exam-controller/templates/leader-election-rolebinding.yaml`

**Step 1: Write serviceaccount.yaml**

```yaml
{{- if .Values.serviceAccount.create -}}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "exam-controller.serviceAccountName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "exam-controller.labels" . | nindent 4 }}
  {{- with .Values.serviceAccount.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
```

**Step 2: Write clusterrole.yaml**

Translate `config/rbac/role.yaml` — the ClusterRole named `manager-role` with rules for:
- `""` (core): namespaces, services (full CRUD), secrets (get/list/watch)
- `apps`: deployments (full CRUD)
- `exam.otu.ca`: exams, exams/finalizers, exams/status
- `cilium.io`: ciliumnetworkpolicies (full CRUD)
- `networking.k8s.io`: ingresses, networkpolicies (full CRUD)

Name: `{{ include "exam-controller.fullname" . }}-manager`

**Step 3: Write clusterrolebinding.yaml**

Bind the manager ClusterRole to the service account.

**Step 4: Write leader-election-role.yaml**

Namespace-scoped Role for configmaps, leases (coordination.k8s.io), and events.

**Step 5: Write leader-election-rolebinding.yaml**

Bind the leader election Role to the service account in `.Release.Namespace`.

---

### Task 4: Create Deployment template

**Files:**
- Create: `charts/exam-controller/templates/deployment.yaml`

**Step 1: Write deployment.yaml**

Translate `config/manager/manager.yaml` with all patches applied:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "exam-controller.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "exam-controller.labels" . | nindent 4 }}
spec:
  replicas: {{ .Values.replicaCount }}
  selector:
    matchLabels:
      {{- include "exam-controller.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "exam-controller.selectorLabels" . | nindent 8 }}
      annotations:
        kubectl.kubernetes.io/default-container: manager
    spec:
      serviceAccountName: {{ include "exam-controller.serviceAccountName" . }}
      terminationGracePeriodSeconds: 10
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: manager
          image: {{ include "exam-controller.image" . }}
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          command:
            - /manager
          args:
            {{- if .Values.leaderElection.enabled }}
            - --leader-elect
            {{- end }}
            - --health-probe-bind-address=:{{ .Values.healthProbe.port }}
            {{- if .Values.metrics.enabled }}
            - --metrics-bind-address=:{{ .Values.metrics.port }}
            - --metrics-secure={{ .Values.metrics.secure }}
            {{- end }}
            {{- if .Values.webhook.enabled }}
            {{- if .Values.webhook.certDir }}
            - --webhook-cert-path={{ .Values.webhook.certDir }}
            {{- end }}
            {{- end }}
          ports:
            {{- if .Values.metrics.enabled }}
            - containerPort: {{ .Values.metrics.port }}
              name: https
              protocol: TCP
            {{- end }}
          livenessProbe:
            httpGet:
              path: /healthz
              port: {{ .Values.healthProbe.port }}
            initialDelaySeconds: 15
            periodSeconds: 20
          readinessProbe:
            httpGet:
              path: /readyz
              port: {{ .Values.healthProbe.port }}
            initialDelaySeconds: 5
            periodSeconds: 10
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
          securityContext:
            readOnlyRootFilesystem: true
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
```

---

### Task 5: Create metrics and optional templates

**Files:**
- Create: `charts/exam-controller/templates/metrics-service.yaml`
- Create: `charts/exam-controller/templates/metrics-rbac.yaml`
- Create: `charts/exam-controller/templates/servicemonitor.yaml`
- Create: `charts/exam-controller/templates/networkpolicy.yaml`
- Create: `charts/exam-controller/templates/webhook.yaml`

**Step 1: Write metrics-service.yaml**

Gated with `{{- if .Values.metrics.enabled }}`. Service on port 8443 targeting `https` port on pods with selector labels.

**Step 2: Write metrics-rbac.yaml**

ClusterRole `metrics-auth-role` for tokenreviews/subjectaccessreviews + its ClusterRoleBinding.
ClusterRole `metrics-reader` for `/metrics` non-resource URL.

**Step 3: Write servicemonitor.yaml**

Gated with `{{- if and .Values.metrics.enabled .Values.metrics.serviceMonitor.enabled }}`. Prometheus ServiceMonitor targeting the metrics service.

**Step 4: Write networkpolicy.yaml**

Gated with `{{- if .Values.networkPolicy.enabled }}`. NetworkPolicy allowing ingress on metrics port from namespaces with configured selector.

**Step 5: Write webhook.yaml**

Gated with `{{- if .Values.webhook.enabled }}`. ValidatingWebhookConfiguration for path `/validate-exam-otu-ca-v1alpha1-exam` on create/update of exams.

---

### Task 6: Create RBAC helper roles and NOTES.txt

**Files:**
- Create: `charts/exam-controller/templates/user-roles.yaml`
- Create: `charts/exam-controller/templates/NOTES.txt`

**Step 1: Write user-roles.yaml**

Three ClusterRoles: exam-admin, exam-editor, exam-viewer (from `config/rbac/exam_*_role.yaml`).

**Step 2: Write NOTES.txt**

```
Exam Controller has been deployed to namespace {{ .Release.Namespace }}.

To verify the deployment:
  kubectl get pods -n {{ .Release.Namespace }} -l {{ include "exam-controller.selectorLabels" . | replace ": " "=" | replace "\n" "," }}

To create an exam:
  kubectl apply -f https://raw.githubusercontent.com/rdrake/exam-controller/main/config/samples/exam_v1alpha1_exam.yaml

{{- if .Values.metrics.enabled }}

Metrics are available on port {{ .Values.metrics.port }}.
{{- if .Values.metrics.serviceMonitor.enabled }}
A ServiceMonitor has been created for Prometheus auto-discovery.
{{- end }}
{{- end }}
```

---

### Task 7: Validate and test Helm chart

**Step 1: Lint the chart**

Run: `helm lint charts/exam-controller/`
Expected: "1 chart(s) linted, 0 chart(s) failed"

**Step 2: Template with defaults**

Run: `helm template exam-test charts/exam-controller/ --namespace exam-system`
Expected: Valid YAML with all resources rendered

**Step 3: Template with all options enabled**

Run: `helm template exam-test charts/exam-controller/ --namespace exam-system --set webhook.enabled=true --set metrics.serviceMonitor.enabled=true --set networkPolicy.enabled=true`
Expected: Valid YAML including webhook, servicemonitor, and networkpolicy resources

**Step 4: Dry-run install**

Run: `helm install exam-test charts/exam-controller/ --namespace exam-system --create-namespace --dry-run`
Expected: Successful dry run output

**Step 5: Commit**

```bash
git add charts/
git commit -m "feat: add Helm chart for exam-controller"
```

---

## Workstream 2: CI Pipeline Enhancements

### Task 8: Add Docker build and push workflow

**Files:**
- Create: `.github/workflows/build.yml`

**Step 1: Write build.yml**

```yaml
name: Build and Push

on:
  push:
    branches: [main]
    tags: ["v*"]
  pull_request:
    branches: [main]

env:
  REGISTRY: ghcr.io
  IMAGE_NAME: ${{ github.repository }}

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@v4

      - uses: docker/setup-buildx-action@v3

      - uses: docker/login-action@v3
        if: github.event_name != 'pull_request'
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - uses: docker/metadata-action@v5
        id: meta
        with:
          images: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}
          tags: |
            type=ref,event=branch
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=sha

      - uses: docker/build-push-action@v6
        with:
          context: .
          push: ${{ github.event_name != 'pull_request' }}
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          platforms: linux/amd64,linux/arm64
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

**Step 2: Commit**

```bash
git add .github/workflows/build.yml
git commit -m "ci: add Docker build and push workflow"
```

---

### Task 9: Add release workflow with Helm chart

**Files:**
- Create: `.github/workflows/release.yml`

**Step 1: Write release.yml**

```yaml
name: Release

on:
  push:
    tags: ["v*"]

permissions:
  contents: write
  packages: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Build installer manifest
        run: make build-installer IMG=ghcr.io/${{ github.repository }}:${{ github.ref_name }}

      - name: Create GitHub Release
        uses: softprops/action-gh-release@v2
        with:
          generate_release_notes: true
          files: |
            dist/install.yaml

  helm:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@v4

      - name: Install Helm
        uses: azure/setup-helm@v4

      - name: Login to GHCR
        run: echo "${{ secrets.GITHUB_TOKEN }}" | helm registry login ghcr.io -u ${{ github.actor }} --password-stdin

      - name: Package and push Helm chart
        run: |
          VERSION="${GITHUB_REF_NAME#v}"
          sed -i "s/^version:.*/version: ${VERSION}/" charts/exam-controller/Chart.yaml
          sed -i "s/^appVersion:.*/appVersion: \"${VERSION}\"/" charts/exam-controller/Chart.yaml
          helm package charts/exam-controller/
          helm push exam-controller-${VERSION}.tgz oci://ghcr.io/${{ github.repository_owner }}/charts
```

**Step 2: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: add release workflow with Helm OCI push"
```

---

### Task 10: Add Helm lint to CI

**Files:**
- Modify: `.github/workflows/lint.yml`

**Step 1: Add Helm lint job**

Add a second job to `lint.yml`:

```yaml
  helm:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: azure/setup-helm@v4
      - run: helm lint charts/exam-controller/
```

**Step 2: Commit**

```bash
git add .github/workflows/lint.yml
git commit -m "ci: add Helm lint to lint workflow"
```

---

## Workstream 3: E2E Test Expansion

### Task 11: Add exam lifecycle E2E test — provisioning through ready

**Files:**
- Modify: `test/e2e/e2e_test.go`

**Step 1: Add lifecycle test**

Add a new `It` block inside the existing `Describe("Manager")` context, after the existing metrics test. This test creates an Exam CR with `unlock` set 5 minutes in the future and `provisionBefore: 10m` (so provisioning starts immediately), then verifies:

1. Create an SMTP Secret `exam-smtp-credentials` in the `exam-system` namespace with dummy values
2. Create an Exam CR in `exam-system` namespace with:
   - 1 student, 0 spares
   - `unlock` = now + 5 minutes
   - `provisionBefore` = 10 minutes (triggers immediate provisioning)
   - `duration` = 1h, `timeMultiplier` = 1.5
3. Eventually: Exam status phase becomes `Provisioning` (poll via `kubectl get exam -o jsonpath={.status.phase}`)
4. Eventually: Exam namespace `exam-<name>` is created
5. Eventually: Deployment exists in exam namespace
6. Eventually: Exam status phase becomes `Ready`
7. Verify status conditions include `Provisioned=True`
8. Clean up: delete the Exam CR and verify namespace is cleaned up

Use `Eventually` with 3-minute timeout and 2-second polling for phase transitions.

**Step 2: Run E2E tests**

Run: `make test-e2e`
Expected: All tests pass including new lifecycle test

**Step 3: Commit**

```bash
git add test/e2e/e2e_test.go
git commit -m "test: add E2E test for exam provisioning lifecycle"
```

---

### Task 12: Add E2E test for email and dry run phases

**Files:**
- Modify: `test/e2e/e2e_test.go`

**Step 1: Add email/dry-run test**

New `It` block that creates an Exam CR with `unlock` set close enough that email sending triggers:

1. Create Exam CR with:
   - `unlock` = now + 2 minutes
   - `provisionBefore` = 5 minutes
   - `email.before` = 3 minutes (email starts immediately since 3m > 2m-until-unlock)
   - `email.rateLimit` = 10
   - `dryRun.before` = 1 minute
2. Eventually: status phase becomes `Ready`
3. Eventually: status condition `AllEmailsSent=True` appears
4. Verify: `status.students[0].emailStatus` = `Failed` (SMTP is dummy, so send fails — but controller marks it and moves on)
5. Eventually: status condition `DryRunComplete=True` appears
6. Verify: `status.dryRun` is populated with pass/fail counts
7. Clean up

**Step 2: Commit**

```bash
git add test/e2e/e2e_test.go
git commit -m "test: add E2E test for email sending and dry run phases"
```

---

### Task 13: Add E2E test for unlock and lock transitions

**Files:**
- Modify: `test/e2e/e2e_test.go`

**Step 1: Add unlock/lock test**

New `It` block with short timing to exercise the full lifecycle:

1. Create Exam CR with:
   - `unlock` = now + 30 seconds
   - `provisionBefore` = 2 minutes
   - `duration` = 30 seconds
   - `timeMultiplier` = 1.0 (lock = unlock + 30s)
   - `email.before` = 2 minutes
2. Eventually: phase becomes `Ready`
3. Eventually: phase becomes `Unlocked` (wait up to 2 minutes)
4. Verify: Ingress resources exist in exam namespace
5. Verify: student status phase = `Unlocked`
6. Eventually: phase becomes `Locked` (wait up to 2 minutes)
7. Verify: Ingress resources are deleted
8. Verify: student status phase = `Locked`
9. Clean up

**Step 2: Commit**

```bash
git add test/e2e/e2e_test.go
git commit -m "test: add E2E test for unlock and lock phase transitions"
```

---

### Task 14: Add E2E test for webhook validation

**Files:**
- Modify: `test/e2e/e2e_test.go`

**Step 1: Add webhook validation tests**

Only run if webhook is enabled (check with a helper). Test that invalid Exam CRs are rejected:

1. Attempt to create Exam with 0 students → expect rejection
2. Attempt to create Exam with `duration: 0s` → expect rejection
3. Attempt to create Exam with `timeMultiplier: 0.5` → expect rejection
4. Create valid Exam, then attempt to update `spec.template.image` after provisioning → expect rejection (immutability)

Each rejection: verify `kubectl apply` returns error containing the expected validation message.

**Step 2: Commit**

```bash
git add test/e2e/e2e_test.go
git commit -m "test: add E2E test for webhook validation rules"
```

---

## Workstream 4: Documentation

### Task 15: Rewrite README with comprehensive content

**Files:**
- Modify: `README.md`

**Step 1: Rewrite README.md**

Structure:
1. **Overview** — What the exam controller does (1 paragraph)
2. **How It Works** — 6-phase state machine diagram (text-based), brief description of each phase
3. **Quick Start** — Prerequisites, install CRD, deploy controller, create first exam
4. **Installation** — Kustomize method and Helm method side-by-side
5. **Configuration** — Table of Exam CR spec fields with types, defaults, descriptions
6. **Email Setup** — How to create the SMTP Secret, field reference
7. **Network Policies** — Cilium auto-detection, vanilla fallback, what gets blocked
8. **Monitoring** — Prometheus metrics table (all 12 metrics with names, types, labels)
9. **Development** — Building, testing, linting commands
10. **License** — Apache 2.0

**Step 2: Commit**

```bash
git add README.md
git commit -m "docs: rewrite README with comprehensive usage guide"
```

---

### Task 16: Write CRD field reference

**Files:**
- Create: `docs/crd-reference.md`

**Step 1: Write CRD reference**

Document every field in ExamSpec and ExamStatus with:
- Field path (e.g., `spec.schedule.unlock`)
- Type
- Required/Optional
- Default value
- Description
- Validation rules (from webhook)

Include a complete annotated example Exam CR.

**Step 2: Commit**

```bash
git add docs/crd-reference.md
git commit -m "docs: add CRD field reference"
```

---

### Task 17: Write operational runbook

**Files:**
- Create: `docs/operations.md`

**Step 1: Write operations guide**

Sections:
1. **Deployment** — Namespace setup, RBAC, TLS, image configuration
2. **Day-2 Operations** — Monitoring dashboard queries for the 12 Prometheus metrics
3. **Troubleshooting** — Common failure scenarios:
   - Exam stuck in Provisioning (deployment not ready)
   - Emails failing (SMTP Secret misconfigured)
   - Dry run showing failures (network policy issues)
   - Students can't access instance (ingress/TLS issues)
   - Exam not transitioning phases (time/schedule issues)
4. **Emergency Procedures** — Force-unlock (edit status), manual teardown, skip retention
5. **Scaling** — Resource limits, multiple exams, rate limiting

**Step 2: Commit**

```bash
git add docs/operations.md
git commit -m "docs: add operational runbook"
```

---

### Task 18: Final validation

**Step 1: Run full test suite**

Run: `make test`
Expected: All unit tests pass

**Step 2: Lint**

Run: `make lint`
Expected: No lint errors

**Step 3: Helm lint**

Run: `helm lint charts/exam-controller/`
Expected: No errors

**Step 4: Template validation**

Run: `helm template test charts/exam-controller/ | kubectl apply --dry-run=client -f -`
Expected: All resources valid

**Step 5: Final commit (if any fixups needed)**

```bash
git add -A
git commit -m "chore: final validation fixes"
```
