---
id: TASK-009
type: Task
title: "Move workflow step-assign logic from gateway into orchestrator"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-009 — Move Step-Assign Logic into Orchestrator

---

## Purpose

`internal/gateway/handlers_workflow.go` leaks engine state-machine logic into the HTTP layer:

- `resolveStepDef` (L378-401) opens the projection, `json.Unmarshal`s the workflow definition, and linear-scans `wfDef.Steps`. The engine already has `engine.findStepDef(wfDef, stepID)` (L497 `run.go`), and `workflow.ProjectionWorkflowProvider` already parses projections into `WorkflowDefinition`.
- `handleStepAssign` (L292-376) hand-rolls assignment record creation and the step transition that the orchestrator owns for `ClaimStep`/`ReleaseStep`/`AcknowledgeStep`.

The duplication means any change to step-assign semantics has to land in two places, and the gateway depends on internal workflow JSON shape.

---

## Deliverable

1. Add `StepAssigner` interface on the gateway server (mirror `StepClaimer`) with a single method `AssignStep(ctx, AssignRequest) (*StepExecution, error)`.
2. Add `Orchestrator.AssignStep` on the engine side, implementing the same transition logic `handleStepAssign` does today, using `engine.findStepDef` and `workflow.ProjectionWorkflowProvider`.
3. Wire `Orchestrator` into the server's `StepAssigner` field at startup (next to the existing `StepClaimer`).
4. Delete `resolveStepDef` from the gateway; `handleStepAssign` becomes a thin decode/authorize/delegate handler.

---

## Acceptance Criteria

- Gateway no longer parses workflow JSON or walks `wfDef.Steps`.
- Assignment behaviour is unchanged (verify via scenario tests).
- Orchestrator owns the state machine for assign in the same way it does for claim/release/acknowledge.
