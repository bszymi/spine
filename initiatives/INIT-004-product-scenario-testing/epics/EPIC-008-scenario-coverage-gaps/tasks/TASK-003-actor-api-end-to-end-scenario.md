---
id: TASK-003
type: Task
title: "Actor API end-to-end execution scenario"
status: Completed
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
  - type: blocked_by
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
---

# TASK-003 — Actor API end-to-end execution scenario

---

## Purpose

INIT-012 introduced actor registration, step execution query, eligible actor IDs, actor-type filtering, step acknowledge, and auto-assignment for automated steps. None of these flows are covered by scenario tests. The only existing claim scenario (`TestClaiming_ClaimAndRelease`) releases the step rather than progressing through to result submission.

## Deliverable

Scenario tests covering the full actor polling loop:

- **Human actor golden path**: register actor → run starts → poll `/execution/steps` → claim step → acknowledge step (status becomes `in_progress`) → submit result → workflow advances to next step
- **AI agent golden path**: same as above with `ActorTypeAIAgent`; verify eligible_actor_types filtering excludes the step from an `automated_system` poller
- **Automated system golden path**: automated step is auto-assigned on run start (no claim required) → automated actor polls, finds it, submits result → workflow advances
- **Actor type filtering**: `automated_system` actor polls and receives only steps with `eligible_actor_types` containing `automated_system`; human-targeted steps are absent
- **Acknowledge idempotency**: acknowledging an already-`in_progress` step returns success without changing the step
- **Release and re-claim**: actor claims step, releases it, different actor claims it and completes it successfully

## Acceptance Criteria

- Full claim → acknowledge → submit → advance cycle validated in a single scenario
- Actor type filtering verified: each actor type sees only its eligible steps
- Auto-assignment for automated steps: step is assigned without an explicit claim call
- All scenarios pass with `go test -tags=scenario`
