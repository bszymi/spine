---
id: TASK-034
type: Task
title: "Couple run resolver to registered binding in repository-deactivate scenario"
status: Pending
acceptance: Pending
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
