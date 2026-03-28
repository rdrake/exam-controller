# Test Suite Redesign — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Transform the test suite to 80%+ coverage with a clear unit/integration/e2e pyramid, Ginkgo convention for new tests, and CI enforcement.

**Architecture:** Three tiers — fast unit tests (no K8s), envtest integration tests behind a build tag, slim Kind-based e2e. Thin interfaces at package boundaries with hand-written fakes.

**Tech Stack:** Go 1.25, Ginkgo/Gomega, controller-runtime envtest, Kind, Prometheus testutil

**Known uncoverable branches (do not chase):**
- `slug.Generate` — `crypto/rand.Int` error path (never fails in practice)
- `notifier.SMTPSender.Send` — calls `smtp.SendMail` directly (needs real SMTP server)
- `api/v1alpha1.SetupExamWebhookWithManager` — thin manager wiring (covered by e2e)
- `cmd/main.go` — excluded from coverage threshold entirely

---

### Task 1: Inject sleep function into RetrySender

Prerequisite refactoring. Replace hard-coded `time.Sleep` in `RetrySender` with an injectable function so tests can verify retry timing without real delays.

**Files:**
- Modify: `internal/notifier/email.go`
- Modify: `internal/notifier/email_test.go`

**Step 1: Update RetrySender struct**

In `internal/notifier/email.go`, add a `SleepFunc` field and update `Send` to use it. Keep `inner` and `maxRetries` unexported. Preserve existing backoff timing (100ms base).

```go
type RetrySender struct {
	inner      Sender
	maxRetries int
	SleepFunc  func(time.Duration) // injectable; nil defaults to time.Sleep
}

func NewRetrySender(inner Sender, maxRetries int) *RetrySender {
	return &RetrySender{inner: inner, maxRetries: maxRetries}
}

func (r *RetrySender) Send(from string, to []string, msg []byte) error {
	sleep := r.SleepFunc
	if sleep == nil {
		sleep = time.Sleep
	}
	var err error
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		err = r.inner.Send(from, to, msg)
		if err == nil {
			return nil
		}
		if attempt < r.maxRetries {
			sleep(time.Duration(1<<uint(attempt)) * 100 * time.Millisecond)
		}
	}
	return fmt.Errorf("after %d retries: %w", r.maxRetries, err)
}
```

**Step 2: Verify existing tests still pass**

Run: `cd /Users/rdrake/workspace/exam-controller && go test ./internal/notifier/ -v -count=1`
Expected: all existing tests pass (nil SleepFunc defaults to time.Sleep, same behavior)

**Step 3: Add backoff timing test**

In `internal/notifier/email_test.go`:

```go
func TestRetrySender_BackoffTiming(t *testing.T) {
	var sleeps []time.Duration
	rs := NewRetrySender(&FailNSender{FailCount: 2}, 3)
	rs.SleepFunc = func(d time.Duration) { sleeps = append(sleeps, d) }
	err := rs.Send("from@test.com", []string{"to@test.com"}, []byte("test"))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(sleeps) != 2 {
		t.Fatalf("expected 2 sleeps, got %d", len(sleeps))
	}
	// 100ms * 2^0 = 100ms, 100ms * 2^1 = 200ms
	if sleeps[0] != 100*time.Millisecond {
		t.Errorf("first backoff = %v, want 100ms", sleeps[0])
	}
	if sleeps[1] != 200*time.Millisecond {
		t.Errorf("second backoff = %v, want 200ms", sleeps[1])
	}
}
```

**Step 4: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && go test ./internal/notifier/ -v -count=1`
Expected: all tests pass

**Step 5: Commit**

```bash
git add internal/notifier/email.go internal/notifier/email_test.go
git commit -m "refactor: inject sleep function into RetrySender for testable backoff"
```

---

### Task 2: Extract HealthChecker interface and inject into controller

Prerequisite refactoring. Define a `HealthChecker` interface, preserve the two separate timeouts (health=5s, blocked=3s), and add it as a field on `ExamReconciler`.

**Files:**
- Modify: `internal/smoketest/runner.go`
- Modify: `internal/smoketest/runner_test.go`
- Modify: `internal/controller/exam_controller.go`

**Step 1: Add interface, struct, and fake to runner.go**

In `internal/smoketest/runner.go`:

```go
// HealthChecker verifies instance reachability.
type HealthChecker interface {
	CheckHealth(ctx context.Context, url string) error
	CheckBlocked(ctx context.Context, url string) error
}

// HTTPChecker implements HealthChecker using real HTTP calls.
type HTTPChecker struct {
	HealthTimeout  time.Duration // default 5s
	BlockedTimeout time.Duration // default 3s
}

func (h *HTTPChecker) CheckHealth(ctx context.Context, url string) error {
	timeout := h.HealthTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func (h *HTTPChecker) CheckBlocked(ctx context.Context, url string) error {
	timeout := h.BlockedTimeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	return fmt.Errorf("service reachable (HTTP %d) — NetworkPolicy not enforced", resp.StatusCode)
}

// FakeChecker is a test double that returns canned responses.
type FakeChecker struct {
	HealthErr  error
	BlockedErr error
}

func (f *FakeChecker) CheckHealth(_ context.Context, _ string) error  { return f.HealthErr }
func (f *FakeChecker) CheckBlocked(_ context.Context, _ string) error { return f.BlockedErr }
```

Keep the existing package-level `CheckHealth` and `CheckBlocked` functions as-is (they are used directly by the e2e suite and other callers). Update `RunAll` and `RunDryRun` to accept a `HealthChecker`:

```go
func RunAll(ctx context.Context, checker HealthChecker, targets []Target) Result {
	var r Result
	for _, t := range targets {
		if err := checker.CheckHealth(ctx, t.URL); err != nil {
			r.Failed++
			r.Failures = append(r.Failures, Failure{Student: t.StudentID, Error: err.Error()})
		} else {
			r.Passed++
		}
	}
	return r
}

func RunDryRun(ctx context.Context, checker HealthChecker, targets []Target, negativeURL string) DryRunResult {
	result := RunAll(ctx, checker, targets)
	policyEnforced := true
	if err := checker.CheckBlocked(ctx, negativeURL); err != nil {
		policyEnforced = false
	}
	return DryRunResult{Result: result, PolicyEnforced: policyEnforced}
}
```

**Step 2: Update smoketest tests**

Update `internal/smoketest/runner_test.go` to pass `&HTTPChecker{}` as first arg to `RunAll`/`RunDryRun` where currently called without it.

**Step 3: Add Checker field to ExamReconciler**

In `internal/controller/exam_controller.go`, add field to struct:

```go
type ExamReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	PolicyProvider network.PolicyProvider
	Sender         notifier.Sender
	Now            func() time.Time
	Metrics        *metrics.ExamMetrics
	Checker        smoketest.HealthChecker // nil defaults to HTTPChecker
}
```

Update `runDryRun` method (line 621) to use `r.Checker`:

```go
func (r *ExamReconciler) runDryRun(ctx context.Context, exam *examv1alpha1.Exam) {
	checker := r.Checker
	if checker == nil {
		checker = &smoketest.HTTPChecker{}
	}
	// ...existing target construction...
	dr := smoketest.RunDryRun(ctx, checker, targets, negativeURL)
	// ...rest unchanged...
}
```

**Step 4: Run all tests**

Run: `cd /Users/rdrake/workspace/exam-controller && go test $(go list ./... | grep -v /e2e) -count=1`
Expected: all pass

**Step 5: Commit**

```bash
git add internal/smoketest/ internal/controller/exam_controller.go
git commit -m "refactor: extract HealthChecker interface and inject into controller"
```

---

### Task 3: Extract selectPolicyProvider into internal/provider

Prerequisite refactoring. Move CRD discovery logic out of `cmd/main.go`.

**Files:**
- Create: `internal/provider/provider.go`
- Create: `internal/provider/provider_test.go`
- Modify: `cmd/main.go`

**Step 1: Write the failing test**

Create `internal/provider/provider_test.go`:

```go
package provider

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"

	"github.com/rdrake/exam-controller/internal/network"
)

func TestSelectPolicyProvider_CiliumAvailable(t *testing.T) {
	client := &fakeclientset.Clientset{}
	fakeDisc := client.Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "cilium.io/v2",
			APIResources: []metav1.APIResource{
				{Kind: "CiliumNetworkPolicy"},
			},
		},
	}
	p := SelectPolicyProvider(fakeDisc)
	if _, ok := p.(*network.CiliumPolicyProvider); !ok {
		t.Errorf("expected CiliumPolicyProvider, got %T", p)
	}
}

func TestSelectPolicyProvider_CiliumNotAvailable(t *testing.T) {
	client := &fakeclientset.Clientset{}
	fakeDisc := client.Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = []*metav1.APIResourceList{}
	p := SelectPolicyProvider(fakeDisc)
	if _, ok := p.(*network.VanillaPolicyProvider); !ok {
		t.Errorf("expected VanillaPolicyProvider, got %T", p)
	}
}

func TestSelectPolicyProvider_DiscoveryError(t *testing.T) {
	// Empty fake with no resources returns an error for unknown group versions
	client := &fakeclientset.Clientset{}
	fakeDisc := client.Discovery().(*fakediscovery.FakeDiscovery)
	p := SelectPolicyProvider(fakeDisc)
	if _, ok := p.(*network.VanillaPolicyProvider); !ok {
		t.Errorf("expected VanillaPolicyProvider on discovery error, got %T", p)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/rdrake/workspace/exam-controller && go test ./internal/provider/ -v -count=1`
Expected: FAIL — package doesn't exist

**Step 3: Write implementation**

Create `internal/provider/provider.go`:

```go
package provider

import (
	"k8s.io/client-go/discovery"

	"github.com/rdrake/exam-controller/internal/network"
)

// SelectPolicyProvider inspects the cluster API to choose Cilium or vanilla NetworkPolicy.
func SelectPolicyProvider(disc discovery.DiscoveryInterface) network.PolicyProvider {
	resources, err := disc.ServerResourcesForGroupVersion("cilium.io/v2")
	if err == nil && resources != nil {
		for _, r := range resources.APIResources {
			if r.Kind == "CiliumNetworkPolicy" {
				return &network.CiliumPolicyProvider{}
			}
		}
	}
	return &network.VanillaPolicyProvider{}
}
```

**Step 4: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && go test ./internal/provider/ -v -count=1`
Expected: PASS

**Step 5: Update cmd/main.go**

Replace the local `selectPolicyProvider` function. The current function signature is `func selectPolicyProvider(mgr ctrl.Manager) network.PolicyProvider` and builds a discovery client internally. Replace the call site and function:

```go
// At the call site (around line 183 of main.go), replace:
//   policyProvider := selectPolicyProvider(mgr)
// with:
disc, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
if err != nil {
	setupLog.Info("Cannot create discovery client, using vanilla NetworkPolicy")
}
policyProvider := provider.SelectPolicyProvider(disc)
```

Add import: `"github.com/rdrake/exam-controller/internal/provider"` and `"k8s.io/client-go/discovery"`.
Delete the local `selectPolicyProvider` function (lines 219-236).

Note: `SelectPolicyProvider` must handle a `nil` disc parameter gracefully (return vanilla). Add a nil guard at the top of the function:

```go
func SelectPolicyProvider(disc discovery.DiscoveryInterface) network.PolicyProvider {
	if disc == nil {
		return &network.VanillaPolicyProvider{}
	}
	// ...rest unchanged
}
```

**Step 6: Run full test suite**

Run: `cd /Users/rdrake/workspace/exam-controller && go test $(go list ./... | grep -v /e2e) -count=1`
Expected: all pass

**Step 7: Commit**

```bash
git add internal/provider/ cmd/main.go
git commit -m "refactor: extract selectPolicyProvider into internal/provider for testability"
```

---

### Task 4: Add build tags to integration tests and update Makefile

**Files:**
- Modify: `internal/controller/suite_test.go`
- Modify: `internal/controller/exam_controller_test.go`
- Modify: `Makefile`

**Step 1: Add build tag to both integration test files**

Add `//go:build integration` as the very first line (before `package`) of:
- `internal/controller/suite_test.go`
- `internal/controller/exam_controller_test.go`

Note: `phase_test.go` does NOT get a build tag — it's a unit test that should always run.

**Step 2: Update Makefile**

Update the `test` target to pass `-tags=integration`:

```makefile
.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run all tests (unit + integration).
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test -tags=integration $$(go list ./... | grep -v /e2e) -coverprofile cover.out
```

Add new targets after the `test` target:

```makefile
.PHONY: test-unit
test-unit: ## Run unit tests only (no envtest, no e2e).
	go test $$(go list ./... | grep -v /e2e) -count=1

.PHONY: test-integration
test-integration: manifests generate fmt vet setup-envtest ## Run integration tests only (envtest).
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test -tags=integration ./internal/controller/ -v -count=1

.PHONY: coverage
coverage: manifests generate fmt vet setup-envtest ## Generate HTML coverage report.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test -tags=integration $$(go list ./... | grep -v /e2e) -coverprofile cover.out
	go tool cover -html=cover.out -o coverage.html
	@echo "Coverage report: coverage.html"
```

Note: `make test-unit` has NO envtest/manifest prerequisites for fast feedback. It still runs `phase_test.go` in `internal/controller/` (no build tag), but skips the Ginkgo integration suite.

**Step 3: Verify targets work**

Run: `cd /Users/rdrake/workspace/exam-controller && make test-unit`
Expected: unit tests pass, controller integration tests skipped

Run: `cd /Users/rdrake/workspace/exam-controller && make test`
Expected: all tests pass, `cover.out` generated

**Step 4: Commit**

```bash
git add internal/controller/suite_test.go internal/controller/exam_controller_test.go Makefile
git commit -m "build: add integration build tag and separate Makefile test targets"
```

---

### Task 5: Expand unit tests — internal/notifier

**Files:**
- Modify: `internal/notifier/email_test.go`

**Step 1: Add comprehensive message builder tests**

Cover all four builders with exact output checks:

- `TestBuildStudentMessage` — verify From/To/Subject headers present, body contains URL
- `TestBuildStudentMessage_SpecialCharsInEmail` — email with `+` or `.` subaddressing
- `TestBuildSparesMessage` — verify instructor receives spare URLs joined by newlines
- `TestBuildSparesMessage_NoSpares` — empty URL slice produces valid message
- `TestBuildUnlockNotification` — verify student count, spare count in body
- `TestBuildUnlockNotification_WithFailedEmails` — verify failed emails listed in body
- `TestBuildLockNotification` — verify student/healthy/failed counts in body

**Step 2: Add RetrySender edge cases**

- `TestRetrySender_ZeroRetries` — MaxRetries=0 means ONE attempt (loop runs once), no retries. If inner.Send succeeds, returns nil. If inner.Send fails, returns error after 1 attempt.
- `TestRetrySender_AllRetriesFail` — verify error message includes retry count and wrapped error
- `TestRetrySender_NoSleepAfterLastAttempt` — with injectable SleepFunc, verify sleep is NOT called after the final failed attempt (existing code: `if attempt < r.maxRetries`)

**Step 3: Add FailNSender test**

- `TestFailNSender_TracksAttempts` — verify `Attempts` field increments correctly

**Step 4: Run tests and check coverage**

Run: `cd /Users/rdrake/workspace/exam-controller && go test ./internal/notifier/ -v -count=1 -coverprofile=notifier.cov && go tool cover -func=notifier.cov`
Expected: all pass, coverage of `email.go` > 90% (SMTPSender.Send is uncoverable)

**Step 5: Commit**

```bash
git add internal/notifier/email_test.go
git commit -m "test: expand notifier unit tests for message builders and retry edge cases"
```

---

### Task 6: Expand unit tests — internal/network

**Files:**
- Modify: `internal/network/policy_test.go`
- Modify: `internal/network/cilium_test.go`

**Step 1: Add field-by-field vanilla policy verification**

Expand existing tests:

`TestVanillaDenyAll`:
- `.Spec.PodSelector.MatchLabels` = `{"exam.otu.ca/slug": slug}`
- `.Spec.PolicyTypes` = `[Ingress, Egress]`
- `.Spec.Ingress` is empty, `.Spec.Egress` is empty

`TestVanillaEgressAllowlist`:
- Two ports: 53/UDP and 53/TCP
- Namespace selector: `kubernetes.io/metadata.name: kube-system`
- Pod selector: `k8s-app: kube-dns`
- PolicyTypes = `[Egress]`

`TestVanillaIngressAllow`:
- Port matches the `exam.otu.ca/port` label value
- Protocol is TCP
- Namespace selector: `kubernetes.io/metadata.name: ingress-nginx`
- Pod selector: `app.kubernetes.io/name: ingress-nginx`
- PolicyTypes = `[Ingress]`

**Step 2: Add default port test**

- `TestVanillaIngressAllow_DefaultPort` — labels without `exam.otu.ca/port` key → port defaults to `"8080"`
- `TestVanillaIngressAllow_CustomPort` — labels with `exam.otu.ca/port: "9090"` → port is `"9090"`

**Step 3: Expand Cilium tests with field-by-field spec verification**

`TestCiliumDenyAll`:
- Parse `u.Object["spec"]`, verify `endpointSelector.matchLabels` = `{"exam.otu.ca/slug": slug}`
- Verify `ingressDeny` and `egressDeny` arrays are present

`TestCiliumEgressAllowlist`:
- Verify `toFQDNs` contains `matchPattern: *.cluster.local`
- Verify `toPorts` contains port 53 UDP/TCP

`TestCiliumIngressAllow`:
- Verify `fromEndpoints` matchLabels include `ingress-nginx`
- Verify `toPorts` rules contain HTTP method matching
- Verify port matches label value

`TestCiliumIngressAllow_DefaultPort`:
- Labels without port key → defaults to `"8080"`

**Step 4: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && go test ./internal/network/ -v -count=1 -coverprofile=network.cov && go tool cover -func=network.cov`
Expected: coverage of `policy.go` and `cilium.go` > 95% (default port branch now covered)

**Step 5: Commit**

```bash
git add internal/network/policy_test.go internal/network/cilium_test.go
git commit -m "test: add field-by-field verification and default port coverage for network policies"
```

---

### Task 7: Expand unit tests — internal/provisioner

**Files:**
- Modify: `internal/provisioner/resources_test.go`

**Step 1: Expand Deployment tests**

Assert fields that actually exist in the code:

- `TestDeployment_SecurityContext` — RunAsNonRoot=true, AllowPrivilegeEscalation=false, Capabilities.Drop=["ALL"], SeccompProfile.Type=RuntimeDefault, AutomountServiceAccountToken=false
- `TestDeployment_Resources` — container resources match `exam.Spec.Template.Resources`
- `TestDeployment_ImageAndPort` — container image = `exam.Spec.Template.Image`, containerPort = `exam.Spec.Template.Port`
- `TestDeployment_Replicas` — `*spec.Replicas == 1`
- `TestDeployment_Selector` — selector matchLabels contains slug label

**Step 2: Expand Service tests**

- `TestService_Ports` — port number matches `exam.Spec.Template.Port`
- `TestService_Selector` — selector = `{"exam.otu.ca/slug": slug}`

**Step 3: Expand Ingress tests**

- `TestIngress_TLS` — TLS hosts = `["{slug}.{domain}"]`, secretName = `exam.Spec.IngressTLS.SecretName`
- `TestIngress_Rules` — host = `{slug}.{domain}`, path = `/`, pathType = Prefix, backend service name = slug, backend port = exam port

**Step 4: Expand Labels tests**

- `TestLabels_WithStudentID` — contains keys: `exam.otu.ca/exam`, `exam.otu.ca/slug`, `exam.otu.ca/student`
- `TestLabels_WithoutStudentID` — `exam.otu.ca/student` key is absent (not just empty)

Note: `Labels()` does NOT include a port label. The port label is added by the controller at call sites (`enforcePolicies`, `provisionInstance`), not by `Labels()` itself.

**Step 5: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && go test ./internal/provisioner/ -v -count=1 -coverprofile=prov.cov && go tool cover -func=prov.cov`
Expected: coverage of `resources.go` > 95%

**Step 6: Commit**

```bash
git add internal/provisioner/resources_test.go
git commit -m "test: expand provisioner tests with field-level resource verification"
```

---

### Task 8: Expand unit tests — internal/metrics

**Files:**
- Modify: `internal/metrics/metrics_test.go`

**Step 1: Expand metric tests**

Use `prometheus.NewRegistry()` (not `prometheus.DefaultRegisterer`) for isolated tests:

- `TestNewExamMetrics_AllFieldsInitialized` — create with fresh registry, verify every field is non-nil
- `TestRecordPhaseTransition_IncrementsCounter` — use `testutil.ToFloat64(m.PhaseTransitions.WithLabelValues(...))` to verify value
- `TestReconcileDuration_ObservesHistogram` — call `m.ReconcileDuration.Observe(0.5)`, then gather metrics from registry and verify histogram `sample_count == 1`
- `TestCleanupExam_RemovesLabelSeries` — set gauge values for an exam, call `CleanupExam`, verify `testutil.ToFloat64()` panics or returns 0 for those label values. Use `testutil.CollectAndCount()` to verify metric family cardinality drops.
- `TestCleanupExam_NoopForUnknownExam` — call `CleanupExam("nonexistent")` with a fresh registry, verify no panic
- `TestCountdownGauges_SetAndRead` — set SecondsUntilUnlock and SecondsUntilLock, read back via `testutil.ToFloat64()`
- `TestEmailCounters` — increment EmailsSent and EmailsFailed, verify values
- `TestInstanceGauges` — set InstancesTotal/Healthy/Failed, verify values

Note: Do NOT test duplicate registration — `MustRegister` will intentionally panic. Each test should use its own fresh `prometheus.NewRegistry()`.

**Step 2: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && go test ./internal/metrics/ -v -count=1 -coverprofile=metrics.cov && go tool cover -func=metrics.cov`
Expected: coverage of `metrics.go` > 90% (CleanupExam now covered)

**Step 3: Commit**

```bash
git add internal/metrics/metrics_test.go
git commit -m "test: expand metrics tests covering all counters, gauges, and cleanup"
```

---

### Task 9: Expand unit tests — internal/smoketest

**Files:**
- Modify: `internal/smoketest/runner_test.go`

**Step 1: Expand health check tests**

- `TestCheckHealth_Timeout` — use `HTTPChecker{HealthTimeout: 50*time.Millisecond}` against an httptest.Server that sleeps 200ms, verify error contains timeout
- `TestCheckHealth_StatusCodes` — table-driven: 200 OK, 201 OK, 299 OK, 300 fail, 400 fail, 500 fail
- `TestCheckBlocked_ConnectionRefused` — URL to closed listener (use `httptest.NewServer` then close it), verify returns nil (blocked = good)
- `TestCheckBlocked_Timeout` — use `HTTPChecker{BlockedTimeout: 50*time.Millisecond}` against a server that sleeps, verify returns nil (timeout = blocked = good)

**Step 2: Expand Runner tests (using HealthChecker parameter)**

- `TestRunAll_AllHealthy` — all targets pass, `Result.Passed == len(targets)`, `Failed == 0`
- `TestRunAll_AllFailed` — all targets fail, `Passed == 0`, `Failed == len(targets)`
- `TestRunAll_EmptyTargets` — empty target slice, `Result{}`
- `TestRunDryRun_AllHealthyPolicyEnforced` — FakeChecker with nil errors
- `TestRunDryRun_PolicyNotEnforced` — FakeChecker with BlockedErr set

**Step 3: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && go test ./internal/smoketest/ -v -count=1 -coverprofile=smoke.cov && go tool cover -func=smoke.cov`
Expected: coverage of `runner.go` > 90%

**Step 4: Commit**

```bash
git add internal/smoketest/runner_test.go
git commit -m "test: expand smoketest coverage with timeouts, edge cases, and FakeChecker"
```

---

### Task 10: Expand unit tests — api/v1alpha1 webhook

**Files:**
- Modify: `api/v1alpha1/exam_webhook_test.go`

**Step 1: Add missing ValidateCreate edge cases**

- `TestValidateCreate_InvalidDomain` — domain with spaces or underscores → rejected
- `TestValidateCreate_InvalidStudentID` — student ID that fails Kubernetes label validation → rejected
- `TestValidateCreate_ZeroSpares` — spares=0 is valid
- `TestValidateCreate_NegativeSpares` — spares=-1 is rejected
- `TestValidateCreate_MultiplierExactlyOne` — multiplier=1.0 is valid
- `TestValidateCreate_EmailTimingEdge` — emailBefore exactly equals required time (boundary)

**Step 2: Add missing ValidateUpdate edge cases**

- `TestValidateUpdate_ImageChangeAfterPending` — image change during Provisioning → rejected
- `TestValidateUpdate_DurationChangeAfterLocked` — duration change after Locked → rejected
- `TestValidateUpdate_StudentAddAfterPending` — adding students after Pending → rejected
- `TestValidateUpdate_AllowedFieldChange` — changing a mutable field succeeds

**Step 3: Test ValidateDelete**

- `TestValidateDelete_AlwaysAllowed` — verify it returns nil, nil

**Step 4: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && go test ./api/v1alpha1/ -v -count=1 -coverprofile=webhook.cov && go tool cover -func=webhook.cov`
Expected: coverage of `exam_webhook.go` > 85%

**Step 5: Commit**

```bash
git add api/v1alpha1/exam_webhook_test.go
git commit -m "test: expand webhook validation tests with edge cases and boundary conditions"
```

---

### Task 11: Restructure integration tests — helpers, suite, and lifecycle

This task combines helper extraction, suite update, monolith removal, and the lifecycle happy-path test into a single atomic step so we never lose coverage.

**Files:**
- Create: `internal/controller/helpers_test.go`
- Create: `internal/controller/reconcile_lifecycle_test.go`
- Delete: `internal/controller/exam_controller_test.go`
- Modify: `internal/controller/suite_test.go`

**Step 1: Create helpers_test.go**

Create `internal/controller/helpers_test.go` with `//go:build integration` tag. Extract from the current monolith:

- `createExamCR()` — creates a standard Exam CR. Creates in `exam-system` namespace (matching `SetupWithManager`'s `mapToExam` which hard-codes `exam-system`).
- `patchDeploymentsReady()` — patches deployment replicas to simulate readiness
- `createSMTPSecret()` — creates SMTP secret in test namespace
- `gaugeValue()` — reads Prometheus gauge value
- `eventuallyHasPhase()` — Gomega Eventually assertion for phase transitions
- `uniqueExamName()` — atomic counter for unique names

**Step 2: Ensure suite_test.go creates exam-system namespace**

The `BeforeSuite` in `suite_test.go` should create the `exam-system` namespace in envtest so Exam CRs can be created there. Add if not present.

**Step 3: Create reconcile_lifecycle_test.go**

Create `internal/controller/reconcile_lifecycle_test.go` with `//go:build integration` tag:

```go
var _ = Describe("Exam Lifecycle", func() {
    It("transitions through all 6 phases", func() {
        // 1. Create Exam with near-past provisionTime → starts Provisioning
        // 2. Patch deployments ready → transitions to Ready
        //    Assert: student namespace exists, deployments/services/policies created
        //    Assert: student statuses populated, spare statuses populated
        // 3. If spares > 0, verify spare URL email sent to instructor (FakeSender)
        // 4. Advance clock past unlock → Unlocked
        //    Assert: ingresses created, student phases updated to Unlocked
        // 5. Advance clock past lock → Locked
        //    Assert: ingresses deleted, student phases updated to Locked
        // 6. Advance clock past retention → TearingDown
        //    Assert: namespace being deleted
        //    Note: finalizer removal happens when Exam object is deleted,
        //    NOT when TearingDown is reached via retention deadline
    })
})
```

**Step 4: Delete exam_controller_test.go**

Remove the monolith ONLY after helpers_test.go and reconcile_lifecycle_test.go exist.

**Step 5: Verify tests pass**

Run: `cd /Users/rdrake/workspace/exam-controller && make test-integration`
Expected: lifecycle test passes, suite bootstraps correctly

**Step 6: Commit**

```bash
git add internal/controller/helpers_test.go internal/controller/reconcile_lifecycle_test.go internal/controller/suite_test.go
git rm internal/controller/exam_controller_test.go
git commit -m "test: restructure integration tests — extract helpers, add lifecycle test, remove monolith"
```

---

### Task 12: Integration tests — reconcile_provisioning_test.go

Resource creation, drift correction, spare email, both policy providers.

**Files:**
- Create: `internal/controller/reconcile_provisioning_test.go`

**Step 1: Write provisioning tests**

`//go:build integration` tag. Test cases:

- **"creates namespace, deployments, services, and network policies"** — create Exam, reconcile, verify all expected resources exist in exam namespace
- **"creates correct number of student + spare instances"** — 3 students + 2 spares → 5 deployments, 5 services
- **"sends spare URLs to instructor when spares > 0"** — verify FakeSender.Sent contains spares message with instructor as recipient (this scenario existed in the old monolith)
- **"drift correction: recreates deleted deployment"** — delete one deployment, re-reconcile, verify it's recreated
- **"drift correction: recreates deleted network policy"** — delete a deny-all policy, run `enforcePolicies` via reconcile, verify recreated
- **"uses VanillaPolicyProvider"** — configure reconciler with VanillaPolicyProvider, verify NetworkPolicy resources created
- **"uses CiliumPolicyProvider"** — configure reconciler with CiliumPolicyProvider. NOTE: must install Cilium CRD in envtest. Add `cilium.io_ciliumnetworkpolicies.yaml` to `suite_test.go` CRD paths, or use a test-local CRD YAML. If CRD not available, skip with `Skip("Cilium CRD not installed in envtest")`.

**Step 2: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && make test-integration`
Expected: all pass (Cilium test may skip if CRD not available)

**Step 3: Commit**

```bash
git add internal/controller/reconcile_provisioning_test.go
git commit -m "test: add integration tests for provisioning, drift correction, and spare email"
```

---

### Task 13: Integration tests — reconcile_finalizer_test.go

**Files:**
- Create: `internal/controller/reconcile_finalizer_test.go`

**Step 1: Write finalizer tests**

`//go:build integration` tag. Test cases:

- **"adds finalizer on first reconcile"** — create Exam, reconcile, verify finalizer present
- **"deletion triggers namespace cleanup and finalizer removal"** — create Exam, reconcile through Provisioning, delete the Exam object (set deletion timestamp), reconcile again, verify: exam namespace deleted, finalizer removed, Exam object gone. Note: this is object DELETION (DeletionTimestamp set), not retention-deadline-triggered TearingDown.
- **"handles already-deleted namespace gracefully"** — reconcile through Provisioning, manually delete exam namespace, then delete Exam object — finalizer completes without error
- **"reconcile for non-existent resource returns no error"** — reconcile with a key that doesn't exist, verify no error

**Step 2: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && make test-integration`
Expected: all pass

**Step 3: Commit**

```bash
git add internal/controller/reconcile_finalizer_test.go
git commit -m "test: add integration tests for finalizer and deletion edge cases"
```

---

### Task 14: Integration tests — reconcile_email_test.go

**Files:**
- Create: `internal/controller/reconcile_email_test.go`

**Step 1: Write email tests**

`//go:build integration` tag. Test cases:

- **"sends student emails during Ready phase"** — FakeSender, advance to Ready + past emailTime, reconcile, verify FakeSender.Sent contains student email
- **"respects rate limit"** — rateLimit=1, each reconcile sends at most 1 email
- **"does not re-send already-sent emails"** — reconcile until all sent, reconcile again, verify no duplicates
- **"skips students with Failed email status"** — set one student's EmailStatus to Failed manually, reconcile, verify only Pending students get emails, Failed is NOT retried
- **"sets AllEmailsSent condition after all students attempted"** — verify condition set even when some emails Failed (this is the actual behavior: AllEmailsSent=True means "all attempted", not "all succeeded")
- **"sends unlock notification to instructor"** — advance to Unlocked, verify FakeSender contains unlock notification with student/spare counts
- **"sends lock notification to instructor"** — advance to Locked, verify notification with healthy/failed counts
- **"handles missing SMTP secret"** — Exam with secretRef pointing to nonexistent secret, r.Sender=nil (forcing Secret lookup), verify sendEmail returns error and student EmailStatus set to Failed

**Step 2: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && make test-integration`
Expected: all pass

**Step 3: Commit**

```bash
git add internal/controller/reconcile_email_test.go
git commit -m "test: add integration tests for email sending, rate limiting, and failure handling"
```

---

### Task 15: Integration tests — reconcile_metrics_test.go

**Files:**
- Create: `internal/controller/reconcile_metrics_test.go`

**Step 1: Write metrics integration tests**

`//go:build integration` tag. Use a fresh `prometheus.NewRegistry()` per test to avoid counter accumulation.

- **"updates phase transition counter on phase change"** — reconcile through phase changes, verify PhaseTransitions counter increments
- **"sets countdown gauges"** — during Ready phase, verify SecondsUntilUnlock > 0
- **"updates instance counts"** — verify InstancesTotal/Healthy/Failed reflect student statuses
- **"cleans up metrics on teardown"** — delete Exam (trigger finalizer), verify CleanupExam called (gauge labels gone)
- **"records reconcile duration"** — verify ReconcileDuration histogram has sample_count > 0 after reconcile

**Step 2: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && make test-integration`
Expected: all pass

**Step 3: Commit**

```bash
git add internal/controller/reconcile_metrics_test.go
git commit -m "test: add integration tests for metrics updates and teardown cleanup"
```

---

### Task 16: Integration tests — reconcile_error_test.go (NEW development)

The largest effort item — entirely new test content.

**Files:**
- Create: `internal/controller/reconcile_error_test.go`

**Step 1: Write error path tests**

`//go:build integration` tag. Test cases:

- **"sets ProvisioningDegraded condition when instance fails"** — make one student's provisioning fail (e.g., slug collision or resource conflict), verify ProvisioningDegraded condition set with reason "SomeInstancesFailed"
- **"continues provisioning remaining students after one fails"** — if student 2 of 5 fails, students 3-5 still get provisioned
- **"returns error to trigger requeue on API failure"** — verify reconcile returns a non-nil error (controller-runtime handles requeue automatically)
- **"handles SMTP failure gracefully"** — FakeSender that returns errors, verify: student EmailStatus set to Failed, EmailsFailed metric incremented, exam still progresses (not stuck)
- **"sets DryRunFailed condition when health checks fail"** — use FakeChecker with HealthErr set, verify DryRunFailed condition with "SomeFailed" reason and failure count message
- **"sets NetworkPolicyEnforced=false when blocked check fails"** — use FakeChecker with BlockedErr set, verify NetworkPolicyEnforced condition with status=False, reason="NotEnforced"
- **"runDryRun populates Status.DryRun"** — use FakeChecker, trigger dry run, verify Status.DryRun.CompletedAt set, Passed/Failed counts correct, Failures array populated

**Step 2: Run tests**

Run: `cd /Users/rdrake/workspace/exam-controller && make test-integration`
Expected: all pass

**Step 3: Commit**

```bash
git add internal/controller/reconcile_error_test.go
git commit -m "test: add integration tests for error paths, dry run, and degraded conditions"
```

---

### Task 17: Slim down E2E tests

**Files:**
- Rewrite: `test/e2e/e2e_test.go`

**Step 1: Rewrite with 4 focused scenarios**

Replace the current 828-line file with 4 `It` blocks:

1. **"controller boots and becomes healthy"**
   - Controller pod is Running
   - healthz/readyz respond OK
   - CRD registered (`kubectl get crd exams.exam.otu.ca`)
   - Webhook configuration exists
   - Metrics endpoint reachable (curl via pod with bearer token — verifies RBAC + service wiring)

2. **"exam completes full lifecycle"**
   - Create one valid Exam CR with near-future unlock
   - Watch: Pending → Provisioning → Ready
   - Verify student namespace exists during Ready
   - Delete Exam, verify namespace cleaned up
   - Use generous timeouts (Eventually with 2-minute timeout, 5s polling)

3. **"webhook rejects invalid exam"**
   - Empty students → rejected
   - Zero duration → rejected
   - Invalid multiplier → rejected

4. **"unlock and lock transitions create and remove ingresses"**
   - Create Exam with very short unlock (30s) and short duration
   - Watch for Unlocked, verify Ingress exists in exam namespace
   - Watch for Locked, verify Ingress deleted
   - Use generous timeouts for CI stability

Keep `e2e_suite_test.go` unchanged.

**Step 2: Run e2e tests**

Run: `cd /Users/rdrake/workspace/exam-controller && make test-e2e`
Expected: all 4 pass

**Step 3: Commit**

```bash
git add test/e2e/e2e_test.go
git commit -m "test: slim e2e suite to 4 focused scenarios"
```

---

### Task 18: Add CI coverage threshold

This is the LAST task — only added after all coverage work is complete.

**Files:**
- Modify: `Makefile`
- Modify: `.github/workflows/test.yml`
- Modify: `.gitignore`

**Step 1: Add coverage check to Makefile**

The coverage gate must recompute the percentage from filtered per-function lines (not grep the precomputed total, which includes excluded packages):

```makefile
COVERAGE_THRESHOLD ?= 80

.PHONY: check-coverage
check-coverage: ## Verify test coverage meets threshold.
	@echo "Checking coverage threshold ($(COVERAGE_THRESHOLD)%)..."
	@go tool cover -func=cover.out \
		| grep -v zz_generated \
		| grep -v 'cmd/' \
		| grep -v '^total:' \
		| awk 'BEGIN {hit=0; total=0} { split($$3, a, "%"); if (a[1]+0 > 0) hit++; total++ } END { pct=(total>0 ? hit*100/total : 0); printf "Coverage: %.1f%% (%d/%d functions)\n", pct, hit, total; if (pct < $(COVERAGE_THRESHOLD)) { printf "FAIL: %.1f%% < %d%%\n", pct, $(COVERAGE_THRESHOLD); exit 1 } }'
```

Note: This counts the percentage of functions with >0% coverage after filtering out generated code and cmd/. This is a rough proxy but avoids the `grep '^total:'` bug where the total line includes unfiltered packages.

A more accurate approach: exclude packages at the `go test` level:

```makefile
.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run all tests (unit + integration) with coverage.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test -tags=integration $$(go list ./... | grep -v /e2e | grep -v /cmd) -coverprofile cover.out
	$(MAKE) check-coverage

.PHONY: check-coverage
check-coverage: ## Verify test coverage meets threshold.
	@echo "Checking coverage threshold ($(COVERAGE_THRESHOLD)%)..."
	@go tool cover -func=cover.out \
		| grep -v zz_generated \
		| grep '^total:' \
		| awk '{ gsub(/%/, "", $$3); if ($$3+0 < $(COVERAGE_THRESHOLD)) { printf "FAIL: coverage %.1f%% < %d%%\n", $$3, $(COVERAGE_THRESHOLD); exit 1 } else { printf "OK: coverage %.1f%% >= %d%%\n", $$3, $(COVERAGE_THRESHOLD) } }'
```

By excluding `cmd/` from `go test` package list, the `cover.out` total line naturally excludes `cmd/main.go`. Then `grep -v zz_generated` on the total line is sufficient since generated files are the only remaining noise.

**Step 2: Update .github/workflows/test.yml**

```yaml
    - name: Run tests
      run: make test
    - name: Upload coverage
      uses: actions/upload-artifact@v4
      if: always()
      with:
        name: coverage
        path: cover.out
```

**Step 3: Add to .gitignore**

Append `coverage.html` (verify `cover.out` already present).

**Step 4: Verify locally**

Run: `cd /Users/rdrake/workspace/exam-controller && make test`
Expected: tests pass and coverage ≥ 80%

**Step 5: Commit**

```bash
git add Makefile .github/workflows/test.yml .gitignore
git commit -m "ci: enforce 80% coverage threshold and exclude cmd/ from coverage calculation"
```

---

### Task 19: Final verification

**Step 1: Run full suite**

Run: `cd /Users/rdrake/workspace/exam-controller && make test`
Expected: all pass, coverage ≥ 80%

**Step 2: Run each tier independently**

Run: `make test-unit` — unit tests pass, fast
Run: `make test-integration` — integration tests pass
Run: `make lint` — no new lint issues

**Step 3: Generate coverage report**

Run: `make coverage`
Expected: `coverage.html` generated, verify per-package coverage visually

**Step 4: Commit any fixups**

```bash
git add -A
git commit -m "test: final test suite fixups"
```
