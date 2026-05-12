---
id: TASK-031
type: Task
title: "Accept explicit primary repo in harness.WithCodeRepos resolver"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-12
last_updated: 2026-05-12
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-005-harness-with-code-repos-helper.md
---

# TASK-031 — Accept explicit primary repo in harness.WithCodeRepos resolver

---

## Purpose

`internal/scenariotest/harness/multirepo.go:182-185`'s
`WithCodeRepos`-installed `RepositoryResolver` only contains the code
repo specs and returns `ErrRepositoryNotFound` for `spine`. When a
task explicitly declares `repositories: [spine, ...]` — a valid task
shape — `engine.StartRun` calls `checkRepositoryPreconditions` for
each declared ID before branch creation and the run fails at setup
even though the primary repository is always available. The helper
also rejects a `spine` spec at intake.

This is a P2 scenario-harness finding from the 2026-05-12 codex review
of the INIT-022 batch (commit `cb9f7b2c77`).

## Deliverable

- Accept `spine` (or whichever ID the production wiring uses for the
  primary repo) in both `WithCodeRepos`'s input validation and the
  resolver it installs.
- Resolve `spine` to the harness's primary repo working tree without
  needing a duplicate spec entry from the caller.
- Add a unit test that drives `repositories: [spine, code]` through a
  representative scenario and observes `StartRun` succeed.

## Acceptance Criteria

- Existing scenarios that don't name `spine` are unchanged.
- A new scenario with `repositories: [spine, code-foo]` reaches
  branch creation; reverting this fix fails it at
  `checkRepositoryPreconditions` with `ErrRepositoryNotFound`.
- The harness fails fast when caller-supplied code specs collide with
  the primary repo ID (intentional configuration error, not silent
  override).

## Out of Scope

- Renaming the primary repo ID or changing the production registry
  convention.
- Reworking the resolver to consult `env.Registry` (TASK-034 covers a
  related but distinct binding-coupling concern).

## Resolution (2026-05-12)

Threaded an explicit `primary *TestRepo` argument through
`harness.WithCodeRepos`. The helper now builds a primary
`*repository.Repository` (Kind=KindSpine, LocalPath=primary.Dir) once
per call and hands it to `fixedRepoResolver` and `fixedRepoGitClients`
as a dedicated field, alongside the existing code-repo map. The
resolver's `Lookup(repository.PrimaryRepositoryID)` returns that
record; the git-clients view's `Client(repository.PrimaryRepositoryID)`
returns `primary.Git`. The intake-side rejection of
`CodeRepoSpec.ID == "spine"` is retained — the primary repo's working
tree is taken from the explicit argument, not from a code spec, and
re-registering it as a code spec is a configuration error.

**Files touched**

- `internal/scenariotest/harness/multirepo.go` — signature change +
  primary-record assembly + resolver/clients structural change.
- `internal/scenariotest/harness/multirepo_internal_test.go` — added
  `TestFixedRepoResolverReturnsPrimary` and
  `TestFixedRepoGitClientsReturnsPrimary` to pin the new behaviour at
  the unit layer.
- `internal/scenariotest/scenarios/primary_repo_in_task_repositories_test.go`
  (new, build-tag `scenario`) —
  `TestPrimaryRepoInTaskRepositories_AnchorsTASK031` declares
  `repositories: [spine, billing]` on the task frontmatter and asserts
  `StartRun` succeeds and creates the run branch in both repos.
  Reverting the resolver change alone fails this test at
  `checkRepositoryPreconditions` with a wrapped
  `repository.ErrRepositoryNotFound` for id `spine`.
- `internal/scenariotest/scenarios/{cancel_from_partially_merged,
  cross_repo_evidence, multi_repo_run_lifecycle,
  partial_merge_external_resolution, partial_merge_retry,
  repository_deactivate_during_run}_test.go` — updated all six
  scenario callers to pass `sc.Repo` as the new positional argument.

**Acceptance criteria satisfied**

- *Existing scenarios that don't name `spine` are unchanged.* ✓ —
  Full `go test -tags scenario ./...` green across 38 packages; the
  pre-existing multi-repo scenarios (lifecycle, partial-merge retry /
  external / cancel, cross-repo evidence, repository-deactivate) all
  pass with only the call-site update.
- *A new scenario with `repositories: [spine, code-foo]` reaches
  branch creation; reverting this fix fails it at
  `checkRepositoryPreconditions`.* ✓ — Covered by
  `TestPrimaryRepoInTaskRepositories_AnchorsTASK031`, which observes
  `repository precondition passed` for both `spine` and `billing` in
  the structured log and confirms the run branch exists in both
  working trees post-StartRun.
- *Harness fails fast when caller-supplied code specs collide with the
  primary repo ID.* ✓ — The `spec.ID == repository.PrimaryRepositoryID`
  guard is retained; its comment now points callers at the explicit
  primary argument as the right place to supply the primary working
  tree. Triggered via `t.Fatalf` like the other intake errors.

**Test gates**

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `gofmt -l` on the new files — clean (pre-existing baseline drift in
  `multirepo_internal_test.go` is unchanged).
- `go test -tags scenario ./... -count=1` — green across 38 packages.
- `make docker-lint` — 207 issues, identical to baseline at commit
  `c1c8227` (no new findings).
- `codex review --uncommitted` — clean: *"No discrete correctness
  issues were found in the current staged, unstaged, or untracked
  changes."*
