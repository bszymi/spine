---
id: TASK-017
type: Task
title: "Scenario: repository deactivate while runs are open"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-07
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
