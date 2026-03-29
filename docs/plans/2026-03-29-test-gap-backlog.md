# Test gap backlog

Status update and state transition test gaps identified 2026-03-29.
High-priority and medium-priority gaps have been addressed in
`reconcile_state_test.go`. The items below remain as future work.

## Completed

All items below were addressed in `reconcile_state_test.go`:

- **Re-reconciliation idempotency** — Ready, Unlocked, and Locked
  phases reconciled multiple times; status, conditions, and phases
  verified unchanged; condition deduplication checked
- **Failed student persistence** — Failed students/spares stay Failed
  through Unlocked, Locked, and TearingDown
- **Per-student phase trace** — individual student and spare phases
  asserted at every exam phase; slug/URL immutability verified
- **Condition detail verification** — ObservedGeneration, Reason,
  Message, and LastTransitionTime stability all verified
- **Boundary timing** — transitions tested at exact unlock, lock, and
  retention deadline times; 1ms-before-unlock confirmed to stay Ready
- **Email status persistence** — EmailStatus and EmailSentAt preserved
  through Unlocked, Locked, and TearingDown
- **Condition persistence** — ProvisioningDegraded survives recovery
  and coexists with Provisioned=True after transition to Ready
- **Status.Metrics field** — MetricsSummary counts verified after
  provisioning, after email drain, with failed instances, and skipped
  during TearingDown

## Low priority (future work)

### Concurrent reconcile of same exam

`reconcile_concurrent_test.go` tests two different exams running
concurrently. No test reconciles the same exam object twice in rapid
succession to verify there are no race conditions in status updates.

**Files:** `reconcile_concurrent_test.go`

### RetentionDeadline recomputation on duration extension

The duration extension test (`reconcile_lifecycle_test.go`) verifies
`ComputedLockTime` is recalculated but does not check that
`RetentionDeadline` is also updated based on the new lock time.

**Files:** `reconcile_lifecycle_test.go`

### DryRun across phases

Dry run is only tested during the Ready phase. No test verifies that
the `DryRun` status field is preserved through subsequent phase
transitions or that a dry run cannot re-trigger after completion.

**Files:** `reconcile_error_test.go`
