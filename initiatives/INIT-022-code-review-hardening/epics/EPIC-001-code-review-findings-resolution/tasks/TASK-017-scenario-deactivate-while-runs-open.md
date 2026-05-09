---
id: TASK-017
type: Task
title: "Scenario: repository deactivate while runs are open"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-09
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-017 — Scenario: repository deactivate while runs are open

---

## Purpose

`NopRunReferenceChecker` is the v0.x default in
`internal/repository/manager.go::NewManager` — operators can deactivate
a repository even when active runs reference it. This is a deliberate
permissive behavior for v0.x, but no scenario locks the contract in
place. Without a guard, a future "real" `RunReferenceChecker` could
land silently and change behavior.

This is a P2 coverage finding from the 2026-05-07 code review.

## Deliverable

A scenario in `internal/scenariotest/scenarios/`:

1. Start a multi-repo run referencing `billing` (use TASK-005's
   helper).
2. While the run is active, call
   `POST /api/v1/repositories/billing/deactivate`.
3. Assert:
   - Deactivation succeeds (HTTP 200).
   - The still-open run is unaffected — it continues to read against
     its already-resolved binding.
   - `Registry.Lookup` for new operations against `billing` returns
     `ErrRepositoryInactive`.

## Acceptance Criteria

- Scenario passes deterministically.
- Replacing `NopRunReferenceChecker` with a strict checker makes the
  deactivation step fail with a `runs_active` error class — that is
  the regression bait we want to lock in for the future.

## Out of Scope

- Implementing a strict `RunReferenceChecker` — separate task in
  INIT-014's deferral list.
- Reactivation flow (deregistration is deactivate-only in v0.x).

## Resolution (2026-05-09)

Added `internal/scenariotest/scenarios/repository_deactivate_during_run_test.go`,
a scenario file with two complementary tests that lock both directions
of the v0.x permissive deactivate contract.

### Test shape

Both tests use the same skeleton (8 scenario steps each):

1. `setupDeactivateEnv` builds a `repository.Manager` + `Registry` +
   in-memory `CatalogStore` + gateway `httptest.Server` + Operator-role
   `cli.Client`, all bound to `sc.Runtime.Store` (the shared Postgres
   binding store the orchestrator already uses). The bundle is stashed
   under `stateDeactivateEnv` and the server is anchored to `sc.ParentT`
   so it survives the per-step subtest scope.
2. `setupBillingCodeRepo` wires `harness.WithCodeRepos` against
   `sc.Runtime.Orchestrator` so the run can fan its branch out to a
   real on-disk billing working tree. The orchestrator's resolver and
   the Manager's binding row are independent — that separation is
   itself part of the scenario surface.
3. `WriteAndCommit` of a single-step manual workflow (`build` step
   explicitly routed to billing).
4. `seedDeactivateTaskHierarchy` writes Initiative + Epic via
   `FixtureInitiative` / `FixtureEpic`, then writes the Task directly
   so its frontmatter can carry `repositories: [billing]` (a field
   `FixtureTask` does not expose).
5. `SyncProjections` — required so the orchestrator's
   `BindingResolver` finds the workflow.
6. `registerBillingViaAPI` — POST `/api/v1/repositories` through the
   Operator client, then sanity-asserts `Registry.Lookup` reports
   active. The pre-deactivate active state is what makes the post-
   deactivate inactive assertion a real transition.
7. `startRunReferencingBilling` calls `Orchestrator.StartRun`,
   asserts `run.Status == active` and that billing appears in
   `run.AffectedRepositories`. The run parks waiting on the manual
   step's assignment; nothing in the scenario advances it.

The tests then diverge on step 8+:

**`TestRepositoryDeactivate_NopChecker_AllowsDeactivationDuringActiveRun`**
locks the v0.x permissive contract: with the constructor default checker
in place (we pass `nil` to `NewManager` so it substitutes
`NopRunReferenceChecker{}` itself — explicitly passing the no-op would
mask a regression where the default later flips), the deactivate
endpoint returns 200, the response body's `status` field is `inactive`,
the run row is still `active` on re-read, and `Registry.Lookup` for
billing now returns `repository.ErrRepositoryInactive`.

**`TestRepositoryDeactivate_StrictChecker_RefusesDuringActiveRun`** is
the AC bullet 2 mutation target. A `strictRunReferenceChecker` (always
returns true) is wired in via `setupDeactivateEnv`'s checker
parameter; the test asserts `Manager.Deactivate` returns a
`*domain.SpineError` with `Code == domain.ErrPrecondition` and a
message containing the operator-facing reason
(`"active runs referencing it"`). The binding row stays
`active` (verified through both `Registry.Lookup` and a direct
`GetRepositoryBinding` re-read), and the run is unaffected. This is
the regression bait the AC asked for: if a future commit swaps the
constructor default to a strict checker, `Nop…` test fails and
`Strict…` test still passes — the failure mode points operators
straight at the change.

### Layering choice (HTTP vs Manager)

The permissive test exercises the deactivate flow through the gateway
HTTP handler so a regression in handler→manager wiring is caught. The
strict-checker test calls `Manager.Deactivate` directly because
`cli.Client.Post` collapses non-2xx responses into an opaque
`"API error (412): ..."` string and the assertion needs the typed
`*domain.SpineError` sentinel. The handler is a thin pass-through to
`mgr.Deactivate` (already exercised in the permissive test), so the
direct call does not lose meaningful coverage on the strict path.

### AC mapping

- "Scenario passes deterministically" — both tests pass on the test
  Postgres instance with `-race` clean.
- "Replacing `NopRunReferenceChecker` with a strict checker makes the
  deactivation step fail with a `runs_active` error class" — the
  manager surfaces this as `domain.ErrPrecondition` (code
  `precondition_failed`); the `StrictChecker` test pins exactly that
  shape, including the substring `"active runs referencing it"` in
  the message so the operator-visible reason can't drift silently.
  The task's "runs_active error class" wording reflects an early
  proposal; the implementation that ships today uses the
  `ErrPrecondition` class to match the rest of the deactivate /
  precondition surface, and the resolution doc records that
  reconciliation in line with TASK-016's `started_at`-vs-`timeout_at`
  pattern.

### Codex review

- Pass 1 flagged that passing `repository.NopRunReferenceChecker{}`
  explicitly to `setupDeactivateEnv` masked the constructor-default
  contract the test was claiming to lock in. Fixed by passing `nil`
  through `setupDeactivateEnv` for the `NopChecker` test, with an
  inline comment explaining why the indirection is load-bearing.
- Pass 2 clean: "no actionable correctness issues."

### Files

- `internal/scenariotest/scenarios/repository_deactivate_during_run_test.go`
  — new scenario file under the `//go:build scenario` tag. Contains
  the workflow YAML + task frontmatter constants, the
  `repoDeactivateEnv` bundle struct, a `strictRunReferenceChecker`
  fake, the two top-level tests, and 10 scenario-step helpers
  (`setupDeactivateEnv`, `setupBillingCodeRepo`,
  `seedDeactivateTaskHierarchy`, `registerBillingViaAPI`,
  `startRunReferencingBilling`,
  `deactivateBillingViaAPIExpectingSuccess`,
  `deactivateBillingExpectingPreconditionViaManager`,
  `assertRunStillActive`, `assertRegistryReportsBillingInactive`,
  `assertBillingStillActive`). Helper actor names, workspace IDs, and
  init/epic IDs are namespaced (`init-902`/`epic-902`,
  `op-deactivate-{nop,strict}`) so the two tests cannot collide on
  shared `auth.actors` or artifact-path keys when the suite runs
  sequentially against a single test database.
- `initiatives/.../TASK-017-scenario-deactivate-while-runs-open.md` —
  this artifact, with status flipped to Completed / Approved.

### Test gates

- `go test -tags scenario -count=1 -run TestRepositoryDeactivate_
  ./internal/scenariotest/scenarios/...`: green (both tests, every
  step subtest passes).
- `go test -tags scenario -race -count=1 -run TestRepositoryDeactivate_
  ./internal/scenariotest/scenarios/...`: green.
- `make docker-lint` (no scenario tag): 206 issues — same baseline as
  TASK-011 through TASK-016. With `--build-tags scenario` the
  scenario-only baseline is 49 issues, also unchanged. Zero new
  findings in the new file.
- `gofmt -l`: clean on the new file.
- `codex review --uncommitted`: clean on pass 2 after addressing the
  pass-1 finding above.
