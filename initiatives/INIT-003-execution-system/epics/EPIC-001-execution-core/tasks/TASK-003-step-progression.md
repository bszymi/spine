---
id: TASK-003
type: Task
title: Step Progression Engine
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-001-execution-core/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-001-execution-core/epic.md
---

# TASK-003 — Step Progression Engine

## Purpose

Implement the step progression logic that evaluates preconditions, requests actor assignment, processes results, evaluates outcomes, and determines the next step.

## Deliverable

- `internal/engine/step.go` — Step progression methods on the orchestrator
- `ActivateStep(runID, stepID)` — evaluate preconditions, transition to waiting/assigned
- `SubmitStepResult(runID, stepID, result)` — process actor result, evaluate outcome, route to next step
- Precondition evaluation (artifact_status, field_present, field_value, links_exist)
- Outcome routing — map outcome ID to next_step or terminal

## Acceptance Criteria

- Steps with met preconditions activate successfully
- Steps with unmet preconditions are blocked
- Actor assignment is requested when step activates
- Submitted results are validated against step required_outputs
- Outcomes route to the correct next step
- Terminal outcomes (next_step: end) trigger run completion
- Step state transitions use existing `workflow.StepMachine`
