---
id: TASK-010
type: Task
title: "Decompose ActivateStep and SubmitStepResult in engine/step.go"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
completed: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-010 — Decompose ActivateStep / SubmitStepResult

---

## Purpose

`internal/engine/step.go` L24-184 (`ActivateStep`, 161 lines) and L185-301 (`SubmitStepResult`, 117 lines) each mix multiple responsibilities in a single function:

- `ActivateStep`: precondition eval, persisting validation errors, step-state transition, auto-actor resolution, assignment record creation, assignment-request construction, event emission, projection update.
- `SubmitStepResult`: terminal-state idempotency, outcome validation, divergence branching, run advancement, event emission, convergence trigger.

The concentration makes either function hard to read end-to-end and forces every test to touch every concern.

---

## Deliverable

1. `ActivateStep`: extract
   - `preparePreconditionFailure(ctx, ...)` — persist failure + emit.
   - `resolveAutoActor(ctx, ...)` — consolidate the auto-actor selection currently split between this function and existing helpers.
   - `buildAssignmentRequest(stepDef, ...)` — assignment record + request payload.
2. `SubmitStepResult`: extract
   - `routeStepOutcome(stepDef *StepDefinition, outcome Outcome, exec *StepExecution, run *Run, now time.Time) decision` returning a small decision struct indicating `{advance, branch, terminate}`.
3. Target: each top-level function under 60 lines, extracted helpers individually testable.
4. Add focused unit tests for `routeStepOutcome` covering the advance / branch / terminate paths.

---

## Acceptance Criteria

- Both functions under 60 lines.
- New unit tests cover `routeStepOutcome` decision branches.
- Existing engine tests pass unchanged.
