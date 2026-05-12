---
id: TASK-035
type: Task
title: "Drive bootstrap-admin scenario through workspace-scoped gateway"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-12
last_updated: 2026-05-12
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-018-scenario-bootstrap-admin-idempotency.md
---

# TASK-035 — Drive bootstrap-admin scenario through workspace-scoped gateway

---

## Purpose

`internal/scenariotest/scenarios/bootstrap_admin_idempotency_test.go:140-143`
wires the gateway with direct `Store`/`Auth` and no
`WorkspaceResolver`/`ServicePool`, then calls
`auth.BootstrapInternalAdmin` manually. In platform-binding mode —
the path this scenario is meant to protect — a request resolves a
workspace, builds a workspace-scoped service set, and bootstraps from
the pooled builder/env-derived `SMP_ADMIN_TOKEN`. If that wiring or
re-resolve path regresses, this scenario still passes.

This is a P2 scenario-harness finding from the 2026-05-12 codex
review of the INIT-022 batch (commit `404742faaa`).

## Deliverable

- Drive the request through a `WorkspaceResolver` / `ServicePool` (or
  the pooled builder used in production) so the HTTP auth assertions
  cover the workspace-scoped first-touch and reload paths.
- Pin the platform-binding shape end-to-end — at least one assertion
  must observe the bootstrap effect through a pooled service set, not
  the direct `Store`/`Auth` handles.

## Acceptance Criteria

- Reverting any wiring step in the pooled-builder bootstrap chain
  (e.g., the env-derived `SMP_ADMIN_TOKEN` plumbing or the resolver's
  service-set construction) fails this scenario.
- Hermeticity preserved — the scenario still runs without a real SMP
  binding by using the same fake binding shape the rest of the
  scenariotest pool uses.
- The original single-workspace fallback assertion remains for
  coverage of the non-platform mode.

## Out of Scope

- Changes to the production bootstrap flow itself.
- New harness primitives for pool wiring (use the helpers that already
  exist; if a gap blocks this task, surface it before adding one).

## Resolution (2026-05-12)

The original `TestBootstrapInternalAdmin_IdempotencyAndRotation` stays
intact (per AC #3 — it remains the coverage of the non-platform
single-workspace fallback path). A new scenario,
`TestBootstrapInternalAdmin_PooledBuilderIdempotencyAndRotation`,
exercises the same three-phase contract end-to-end through the
production-shaped wiring chain so that every link in
`cmd/spine/newPooledWorkspaceBuilder`'s call path now has scenario
coverage.

**Files touched**

- `internal/scenariotest/scenarios/bootstrap_admin_idempotency_test.go`
  - Imports added: `context`, `time`, `secrets`, `store`, `workspace`.
  - `fixedWorkspaceResolver`: minimal `workspace.Resolver` returning
    a fixed `*workspace.Config` for one workspace ID and
    `ErrWorkspaceNotFound` for everything else. Mirrors what
    `workspace.PlatformBindingProvider` does at the interface
    boundary; production uses the platform / DB providers, but for
    the AC mutation surface the interface contract is what matters.
  - `setupPooledBootstrapAdminEnv` constructs the full chain:
    `workspace.Config` (with `DatabaseURL` pointing at `store.TestDSN()`),
    `fixedWorkspaceResolver`, a `ServiceSetBuilder` that closes over
    an `adminTokenRef *string` and invokes
    `auth.BootstrapInternalAdmin(ctx, ss.Store, ...)` with the
    pointee, and a `workspace.ServicePool` with
    `IdleCheckInterval: -1` (disabled — scenario drives `pool.Evict`
    explicitly). The gateway is wired with `WorkspaceResolver` +
    `ServicePool` and NO direct `Store`/`Auth`, so the only path to
    a successful 200 is through a fully-built pooled `ServiceSet`.
  - `drivePooledAuthenticatedRequestExpectingOK` sets both
    `Authorization: Bearer <token>` and `X-Workspace-ID:
    ws-bootstrap-pool`. A 200 here proves every step of
    workspace-resolve → `pool.Get` → builder → bootstrap → ss.Auth
    validation succeeded.
  - `resetPooledLogBufAndEvictWorkspace` clears the captured log
    buffer and evicts the workspace ServiceSet in one atomic step
    so the next request re-fires the builder against a clean log
    buffer; `rotatePooledAdminTokenTo` flips `*adminTokenRef` so the
    next builder invocation observes the rotated token, mirroring a
    redeployed `cmd/spine` process with a rotated `SMP_ADMIN_TOKEN`.
  - Per-phase assertion steps mirror the direct-bootstrap scenario's
    row-count, hash, token-id, and DEBUG-log-line checks but read
    through `sc.Runtime.Store` (same Postgres, different connection
    pool from the workspace's per-pool pgx pool).

**Acceptance criteria satisfied**

- *Reverting any wiring step in the pooled-builder bootstrap chain
  (e.g., the env-derived SMP_ADMIN_TOKEN plumbing or the resolver's
  service-set construction) fails this scenario.* ✓ — Two reverts
  verified manually before checking in:
  - Replacing the builder's `auth.BootstrapInternalAdmin` call with a
    no-op fails phase=first with `pooled chain broken — workspace
    resolve / pool.Get / builder / ss.Auth ... status 401`.
  - Setting `adminToken := ""` (env-derived plumbing reverted) also
    fails phase=first with the same 401 (BootstrapInternalAdmin
    no-ops on empty Token, ss.Store has no smp-admin row, ss.Auth
    rejects the bearer). Restored immediately after each bait check.
- *Hermeticity preserved — the scenario still runs without a real SMP
  binding by using the same fake binding shape the rest of the
  scenariotest pool uses.* ✓ — `fixedWorkspaceResolver` is the
  scenario-local fake; `workspace.Config.DatabaseURL` points at the
  shared scenariotest test DSN (`store.TestDSN()`); cleanup is
  registered on `sc.ParentT` so `pool.Close` runs before the harness's
  `db.Cleanup`.
- *The original single-workspace fallback assertion remains for
  coverage of the non-platform mode.* ✓ — Original
  `TestBootstrapInternalAdmin_IdempotencyAndRotation` is untouched
  and still passes; new test sits alongside it in the same file.

**Codex review iteration**

First-pass codex review clean on the first submission: *"The change
only adds a scenario test around pooled workspace bootstrap behavior.
I did not identify any discrete correctness issue in the added code
based on the current diff."* No follow-up needed.

**Test gates**

- `go build ./...` and `go vet ./...` clean under the `scenario`
  build tag.
- `go test -tags scenario -run 'TestBootstrapInternalAdmin'
  ./internal/scenariotest/scenarios/... -count=3` — both the original
  and the new pooled scenarios pass on every iteration.
- `go test ./internal/auth/... ./internal/gateway/...
  ./internal/workspace/... ./cmd/spine/... -count=1` — green (the
  unit suites that own the auth/gateway/workspace surfaces the
  scenario exercises end-to-end).
- `make docker-lint` — 207 issues, identical to baseline at commit
  `f0c3c4a` (no new findings on touched files).
