---
id: EPIC-005
type: Epic
title: Evaluation & Outcomes
status: Pending
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/initiative.md
---

# EPIC-005 — Evaluation & Outcomes

---

## Purpose

Implement governance decisions as distinct layers: step-level evaluation (runtime workflow control) and task-level acceptance (durable governed outcomes), per ADR-004.

---

## Key Work Areas

- Task acceptance recording (approved, rejected_with_followup, rejected_closed)
- Step outcome vocabulary (accepted_to_continue, needs_rework, failed)
- Task lifecycle terminal transitions
- Successor task creation on rejection with follow-up
- Acceptance metadata in task artifact YAML

---

## Primary Outputs

- Task acceptance logic in artifact service
- Step outcome routing in engine orchestrator
- Successor task creation flow
- Updated task artifact schema with acceptance fields

---

## Acceptance Criteria

- Tasks can be accepted with rationale recorded in Git
- Tasks can be rejected with follow-up, creating a linked successor task
- Tasks can be rejected and closed with no successor
- Step outcomes route correctly (continue, rework, fail)
- Acceptance metadata is durable in the task artifact YAML front matter
- Step-level and task-level outcomes use distinct vocabulary
