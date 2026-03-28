# Test Suite Redesign

## Goals

- Reach and enforce 80% code coverage (excluding generated code and `cmd/`)
- Structure the suite so regressions in the reconciliation loop, phase machine, webhooks, and network isolation are caught before merge
- New tests written in Ginkgo/Gomega (Kubebuilder convention); existing passing `testing.T` unit tests left as-is

## Prerequisites -- Source Refactoring

These changes must land before the test expansion can proceed:

1. **`internal/notifier/email.go`**: `RetrySender.Send()` uses hard-coded `time.Sleep` for backoff. Inject a clock function (e.g., `sleepFunc func(time.Duration)`) so tests can verify retry timing without real delays.
2. **`internal/smoketest/runner.go`**: Extract a `HealthChecker` interface from the bare `CheckHealth`/`CheckBlocked` functions so the controller can accept a fake in tests.
3. **`cmd/main.go`**: Extract `selectPolicyProvider()` into `internal/provider/` (or similar) so it can be unit-tested with a fake `discovery.DiscoveryInterface`. The remaining `main()` wiring stays untested.

## Architecture

Three tiers with clear boundaries:

| Tier | Framework | K8s API | External I/O | Speed | Location |
|------|-----------|---------|-------------|-------|----------|
| Unit | Ginkgo/Gomega (new) or `testing.T` (existing) | None | Fakes | <1s per pkg | `internal/*/` alongside source |
| Integration | Ginkgo/Gomega + envtest | Real (in-memory) | Fakes | ~30s total | `internal/controller/` |
| E2E | Ginkgo/Gomega + Kind | Real cluster | Real | ~3min | `test/e2e/` |

Rules:

- Unit tests call exported functions directly with no K8s client. Pure input/output.
- Integration tests use envtest to exercise the full reconciliation loop, including status updates, finalizers, and drift correction. SMTP and HTTP remain faked.
- E2E tests prove the binary boots, webhooks work, and one exam completes its lifecycle. No duplication of integration-tier scenarios.
- Integration tests are gated behind a `//go:build integration` build tag so `make test-unit` can skip them.

## Mocking Strategy

Thin interfaces at package boundaries with hand-written fakes:

- `notifier.Sender` (already exists as FakeSender pattern, formalized)
- `smoketest.HealthChecker` (new -- prerequisite refactoring)

The controller accepts these as injected dependencies. envtest handles K8s API interactions in integration tests.

## Unit Test Packages

### `internal/slug/`

Keep existing `testing.T` tests. Add any missing edge cases.

### `internal/notifier/`

Expand significantly. Tests: message building for student/instructor emails, RetrySender exponential backoff (using injected clock), all error paths (partial recipient failure, auth failure), template rendering with edge-case input (special characters in student names, empty fields).

### `internal/network/`

Expand. Tests: vanilla policy builder (deny-all, egress with port lists, ingress), Cilium policy builder (L7 visibility annotations, field-by-field verification), edge cases (empty port lists, nil CIDR blocks).

### `internal/provisioner/`

Expand. Tests: Deployment builder (security context, resource limits, image pull policy, environment variables), Service builder (port mappings, selectors), Ingress builder (TLS, host rules, path types). Cover every ExamSpec field that influences the output.

### `internal/metrics/`

Expand. Tests: registration doesn't panic, recording phase transitions updates gauges, recording durations populates histograms, `CleanupExam()` removes labels, duplicate registration is safe.

### `internal/smoketest/`

Expand. Tests: health check success/failure (httptest.Server), blocked connectivity detection, timeout behavior, dry-run mode.

### `api/v1alpha1/` (webhook)

Keep existing `testing.T` validation tests. Add a webhook integration test that goes through the admission handler framework (not just direct `ValidateCreate` calls) to catch type registration issues.

### `internal/controller/phase_test.go`

Keep existing `testing.T` tests as-is. Already provides excellent pure-unit coverage of `computeLockTime`, `examNamespace`, `effectiveMultiplier`, `determineDesiredPhase`, and `computeSchedule`.

## Integration Test Restructuring

Split the current monolithic `exam_controller_test.go` by concern. All integration test files carry `//go:build integration` tag.

- `suite_test.go` -- envtest setup, shared k8sClient
- `reconcile_lifecycle_test.go` -- Happy path through all 6 phases
- `reconcile_provisioning_test.go` -- Resource creation, drift correction. Test with both `CiliumPolicyProvider` and `VanillaPolicyProvider` in separate test cases (not runtime detection, which lives in `cmd/main.go`).
- `reconcile_error_test.go` -- **New test development** (not restructuring). Partial failures, retry behavior, orphan prevention. This is the largest effort item.
- `reconcile_finalizer_test.go` -- Deletion, cleanup ordering, already-gone scenarios
- `reconcile_email_test.go` -- Notification timing, idempotency, FakeSender assertions
- `reconcile_metrics_test.go` -- Gauge updates, teardown label cleanup
- `helpers_test.go` -- Shared helpers: createExamCR, patchDeploymentsReady, createSMTPSecret, expectPhase, eventuallyHasPhase

## E2E -- Slim and Focused

Four scenarios:

1. **Controller boots and becomes healthy** -- Pod running, healthz/readyz responding, CRD registered, webhook serving, metrics endpoint reachable (verifies RBAC and service wiring).
2. **Exam completes full lifecycle** -- One Exam CR through all phases, student namespace exists during Ready, gone after teardown.
3. **Webhook rejects invalid exam** -- Malformed CR is rejected, proving admission webhooks are wired.
4. **Unlock/Lock phase transitions with Ingress verification** -- Exercises real networking behavior (Ingress objects appear and disappear), which integration tests cannot fully verify.

Everything else (concurrent exams, error recovery, email content) tested in integration tier.

## CI Enforcement

### Coverage threshold

After `make test`, parse `cover.out` and fail if coverage < 80%. Exclude `zz_generated.deepcopy.go`, `cmd/`, and `test/` from the calculation:

```sh
go tool cover -func=cover.out \
  | grep -v zz_generated \
  | grep -v 'cmd/main.go' \
  | grep '^total:' \
  | awk '{ gsub(/%/, "", $3); if ($3+0 < 80.0) { print "FAIL: coverage " $3 "% < 80%"; exit 1 } }'
```

### Makefile targets

- `make test` -- Existing, add coverage threshold check after test run
- `make test-unit` -- New, runs only unit tests. Uses `-tags='!integration'` to skip envtest suite. **No envtest/manifest prerequisites** for fast feedback.
- `make test-integration` -- New, runs only the envtest integration suite (`-tags=integration` on `internal/controller/`)
- `make test-e2e` -- Existing, unchanged
- `make coverage` -- New, generates HTML coverage report

### GitHub Actions

- `test.yml` -- Add coverage threshold step after `make test`
- All other workflows unchanged
