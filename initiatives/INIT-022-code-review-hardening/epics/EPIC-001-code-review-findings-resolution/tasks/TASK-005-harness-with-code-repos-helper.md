---
id: TASK-005
type: Task
title: "Build harness.WithCodeRepos helper for multi-repo scenarios"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: blocks
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-006-scenario-partial-merge-retry.md
  - type: blocks
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-007-scenario-partial-merge-external-resolution.md
  - type: blocks
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-008-scenario-cancel-from-partially-merged.md
---

# TASK-005 — Build harness.WithCodeRepos helper for multi-repo scenarios

---

## Purpose

The existing `multi_repo_run_lifecycle_test.go` builds its
`multiRepoStubResolver` and `multiRepoStubGitClients` per-test. Every
new multi-repo scenario (TASK-006, TASK-007, TASK-008, plus future
ones) pays the same setup cost. A reusable `harness.WithCodeRepos(...)`
helper is the unblocker for the three P1 partial-merge scenarios.

This is a P1 coverage enabler from the 2026-05-07 code review.

## Deliverable

**Note on the actual harness surface:** the existing multi-repo
scenario at
`internal/scenariotest/scenarios/multi_repo_run_lifecycle_test.go:162-206`
wires its stubs via `sc.Runtime.Orchestrator.WithRepositoryResolver(...)`
and `WithRepositoryGitClients(...)`. There is no `WorkspaceReposDir`
or gitpool hook to plug into at the harness level. The helper must
target these existing orchestrator hooks.

Add a helper in `internal/scenariotest/harness/` (or a sibling
`harness/multirepo` subpackage) that:

- Accepts a list of code-repo IDs and per-repo behaviors (e.g.
  "always succeed", "fail merge with conflict", "fail clone with
  auth").
- Internally constructs reusable implementations of
  `engine.RepositoryResolver` and `engine.RepositoryGitClients`
  (the same interfaces the in-tree `multiRepoStubResolver` and
  `multiRepoStubGitClients` satisfy at lines 360 and 376 of the
  existing test).
- Calls
  `sc.Runtime.Orchestrator.WithRepositoryResolver(resolver)` and
  `WithRepositoryGitClients(clients)` from the helper so calling
  scenarios get a working multi-repo orchestrator with a single
  call.
- Exposes per-repo controls for tests that need to flip behavior
  mid-scenario (e.g. "merge fails on first attempt, succeeds on
  retry"). A small `RepoBehavior` type with a method like
  `SetNextMergeOutcome(domain.RepositoryMergeStatus)` is sufficient.
- Migrates `multi_repo_run_lifecycle_test.go`'s existing per-test
  stubs to use the helper as the migration validator. The existing
  `multiRepoStubResolver`/`multiRepoStubGitClients` types either
  move into the helper or are deleted in favor of the helper's
  internal implementation.

## Acceptance Criteria

- A new scenario can call
  `harness.WithCodeRepos(t, sc, "billing", "payments")`
  (or the chosen helper signature) and immediately drive a
  multi-repo run without per-test stub setup.
- `multi_repo_run_lifecycle_test.go` is migrated to use the helper;
  its existing assertions continue to pass.
- The helper is documented in code comments pointing at the calling
  pattern.

## Out of Scope

- Changing existing `harness.NewTestEnvironment` semantics — additive
  only.
- Adding `RuntimeOption`-style harness configuration. The helper
  operates on the already-constructed orchestrator and does not need
  to participate in `NewTestRuntime` setup.
- Stub behaviors beyond the ones the three downstream P1 scenarios
  need. Future scenarios can extend the `RepoBehavior` surface.

## Notes

Land this task FIRST. TASK-006/007/008 block on it. After it lands,
the three downstream scenarios are 50-100 lines each.

## Resolution (2026-05-07)

**Helper:** `harness.WithCodeRepos(t, orch, specs...)` lives at
`internal/scenariotest/harness/multirepo.go`. It returns a
`map[string]*CodeRepo` keyed by repo ID with `Dir`, `Repository`,
`Client()`, `SetNextMergeFailure(err)`, and `SetNextPushFailure(err)`.
Single call replaces ~40 lines of resolver/clients boilerplate.

**Migration validators:** both pre-existing multi-repo scenarios were
moved to the helper (no API surface beyond `Dir` / `Client()` was
needed for either): `multi_repo_run_lifecycle_test.go` (the AC anchor
called out in the task body) and `cross_repo_evidence_test.go`. The
ad-hoc `multiRepoStubResolver` / `multiRepoStubGitClients` types were
deleted in favor of the helper's internal `fixedRepoResolver` /
`fixedRepoGitClients`. Both migrated scenarios pass on a clean DB.

**Behavior controls:** `SetNextMergeFailure` / `SetNextPushFailure`
queue a single-shot override against the wrapped CLIClient — exactly
what TASK-006's "fail then retry succeed" plan needs. `MergeOpts.Strategy
== "abort"` always passes through so `engine.attemptCodeRepositoryMerge`'s
post-failure `MERGE_HEAD` cleanup is never blocked by an injected
failure. Queue mechanics + abort carve-out have unit-test pins so a
regression that flipped either invariant would fail loudly rather than
turn TASK-006's retry assertion into a tautology.

**Codex iterative review:** 3 passes; one P2 surfaced and fixed:
non-`main` `DefaultBranch` specs were accepted but not realised in the
working tree. Fixed by extracting `newCodeRepoDir(t, branch)` which
runs `git branch -m main <branch>` after `testutil.NewTempRepo`, with a
unit-test pin asserting both that `refs/heads/<branch>` resolves and
that `refs/heads/main` is gone. Two subsequent codex passes returned
clean. Lint count unchanged at 206 pre-existing issues.
