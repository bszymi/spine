---
id: TASK-034
type: Task
title: "Couple run resolver to registered binding in repository-deactivate scenario"
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
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-017-scenario-repository-deactivate-during-run.md
---

# TASK-034 — Couple run resolver to registered binding in repository-deactivate scenario

---

## Purpose

`internal/scenariotest/scenarios/repository_deactivate_during_run_test.go:290-291`
installs a fixed `RepositoryResolver` via `harness.WithCodeRepos`,
while the registration and deactivation steps update `env.Registry`
and `runtime.repositories`. `StartRun` never validates or branches
through the binding row being deactivated, so the test diverges from
production wiring (where the orchestrator uses the registry). The
regression this scenario is meant to lock down — deactivating an
active binding while a run holds it — can pass even if the binding
the active run resolved is not the one being deactivated.

This is a P2 scenario-harness finding from the 2026-05-12 codex
review of the INIT-022 batch (commit `e38098ad68`).

## Deliverable

- Restore `env.Registry` (or whatever resolver the orchestrator
  consults in production) as the run resolver for this scenario.
- Keep the git client wiring intact so the temp repo path remains the
  branch substrate.
- Register the temp repo path through the same registry the
  deactivation step mutates, so the run and the deactivate operation
  touch the same binding row.

## Acceptance Criteria

- Reverting the production-side binding/run coupling check makes this
  scenario fail at the deactivation assertion (it currently does not).
- The scenario remains hermetic — no extra Postgres state or fixture
  beyond what the harness already provides.
- Existing happy-path assertions hold.

## Out of Scope

- Generalising `WithCodeRepos` to consume a registry handle (a wider
  helper refactor; this task fixes the one scenario).
- Changing production resolver semantics.

## Resolution (2026-05-12)

Three coordinated changes inside the deactivate-during-run scenario
file align it with production wiring and make the deactivation
assertion load-bearing on the binding/run coupling check:

**Files touched**

- `internal/scenariotest/scenarios/repository_deactivate_during_run_test.go`
  - **Resolver swap.** `setupBillingCodeRepo` now calls
    `sc.Runtime.Orchestrator.WithRepositoryResolver(env.Registry)`
    immediately after `harness.WithCodeRepos`, replacing the in-memory
    `fixedRepoResolver` with the workspace `repository.Registry` the
    deactivate API call mutates through `Manager.Deactivate`. The
    `fixedRepoGitClients` wiring stays so branch creation continues to
    hit the temp working tree.
  - **Local-path coupling.** `registerBillingViaAPI`'s POST body now
    sends `sc.MustGet(stateBillingRepoDir).(string)` as `local_path`
    (previously a synthetic `/var/spine/...` literal). The engine does
    not consult `Repository.LocalPath` for branching today, but
    aligning the registered binding row with the actual temp dir
    removes the divergence the codex review flagged.
  - **Strict checker.** `strictRunReferenceChecker{}` (unconditional
    `true`) is replaced by `storeBackedRunReferenceChecker` (queries
    `sc.Runtime.Store.ListRunsByTask` for the scenario's task path,
    skips terminal statuses, and matches by repository ID). The
    deactivation assertion now depends on the orchestrator actually
    persisting a run row for this task with the queried repo in
    `AffectedRepositories` — not on a fixture stub.
  - **Provider plumbing.** `runReferenceCheckerProvider` is now
    `func(*scenarioEngine.ScenarioContext) repository.RunReferenceChecker`
    so the checker can close over both the store and the lazily-read
    `task_path` from `sc.State`. `setupDeactivateEnv` invokes it inside
    its Action and falls through to the Nop default when nil is
    passed.
  - Test docstrings updated to describe the new wiring and the
    invariants the AC assertion now load-bears on.

**Acceptance criteria satisfied**

- *Reverting the production-side binding/run coupling check makes
  this scenario fail at the deactivation assertion.* ✓ — Bait-check:
  replacing `Manager.Deactivate`'s `m.runs.AnyActiveRunReferences`
  call + early-return block with a `_ = m.runs` no-op fails
  `TestRepositoryDeactivate_StrictChecker_RefusesDuringActiveRun` at
  the exact step the AC names:

      step "deactivate-billing-expecting-precondition-via-manager"
      failed: Deactivate succeeded; want "precondition_failed"

  Pre-TASK-034 this revert would have left the scenario green
  because the strict checker was a fixture that did not depend on the
  call site at all. Restored immediately after the bait-check.
- *The scenario remains hermetic — no extra Postgres state or fixture
  beyond what the harness already provides.* ✓ — `ListRunsByTask` and
  `CleanupTestData` are existing harness/store APIs; no schema or
  fixture changes.
- *Existing happy-path assertions hold.* ✓ — Both deactivate scenarios
  pass under `-count=3`; the partial-merge / multi-repo / cross-repo /
  cancel-from-partially-merged / primary-repo-in-task / repository-
  deactivate scenario family is green under `-count=1`.

**Codex review iteration**

First-pass codex flagged one P2: the store-backed checker initially
scanned by `repositoryID` alone via `ListRunsByStatus`, which could
falsely match non-terminal runs from other scenarios sharing the test
database. Fixed in the same commit by scoping the scan to
`ListRunsByTask(sc.State["task_path"])`, so the regression bait
depends on the scenario's own run row rather than any active billing
run in the shared schema. Re-review clean: *"No discrete correctness
issues were found in the modified scenario test wiring. The new
store-backed checker and resolver/local-path changes appear
consistent with the intended test coverage."*

**Test gates**

- `go build ./...` and `go vet ./...` — clean (including under the
  `scenario` build tag).
- `go test ./internal/repository/... ./internal/engine/...
  ./internal/gateway/... ./internal/store/... -count=1` — green
  (no unit suite touches the deactivate-scenario shape but they
  validate `Manager`, the gateway handler, and the run-store APIs the
  scenario consumes).
- `go test -tags scenario -run 'TestRepositoryDeactivate' -count=3
  ./internal/scenariotest/scenarios/...` — both scenarios green on
  every iteration.
- `go test -tags scenario -run
  'TestRepositoryDeactivate|TestPartialMerge|TestMultiRepo|TestCrossRepo|TestCancelFromPartiallyMerged|TestPrimaryRepoInTaskRepositories'
  ./internal/scenariotest/scenarios/... -count=1` — green (no
  collateral regressions in the neighbouring scenario family).
- `make docker-lint` — 207 issues, identical to baseline at commit
  `f0c3c4a` (no new findings on touched files).
- `codex review --uncommitted` — clean after the scoping fix.
