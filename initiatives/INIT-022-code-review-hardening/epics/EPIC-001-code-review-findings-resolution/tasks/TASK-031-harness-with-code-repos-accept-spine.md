---
id: TASK-031
type: Task
title: "Accept explicit primary repo in harness.WithCodeRepos resolver"
status: Pending
acceptance: Pending
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
