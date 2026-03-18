---
id: TASK-002
type: Task
title: Divergence and Convergence Execution Model
status: In Progress
epic: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
---

# TASK-002 — Divergence and Convergence Execution Model

---

## Problem

The Constitution (§6) requires controlled divergence — parallel execution must be explicit, all outcomes must be preserved, and convergence must occur through explicit evaluation steps. The domain model mentions divergence and convergence points. ADR-001 and ADR-004 both list this as future work.

However, no document defines how parallel execution actually works: how divergence is initiated, how parallel branches are tracked, how convergence evaluation selects or merges outcomes, or what happens to non-selected branches.

Without this model, the Workflow Engine cannot implement parallel execution, and workflow authors cannot design workflows that use divergence.

## Objective

Define the architectural model for controlled divergence and convergence within workflow execution.

## Deliverable

`/architecture/divergence-and-convergence.md`

Content should define:

- How divergence points are triggered within a Run (what initiates parallel branches)
- How parallel branches are tracked in runtime state
- What actors may be assigned to parallel branches (same actor, different actors, mixed)
- How each branch produces artifacts or outcomes
- How convergence evaluation works — who evaluates, what criteria determine selection
- What happens to non-selected branch outputs (preserved, archived, discarded)
- How convergence results are committed to Git as durable outcomes
- Constraints from Constitution §6 (all outcomes preserved, no silent overwriting)
- Interaction with the Workflow Engine, Actor Gateway, and Artifact Service

## Acceptance Criteria

- Divergence initiation rules are clearly defined
- Parallel branch tracking model is specified
- Convergence evaluation process is documented with selection criteria
- Non-selected outcomes are handled per constitutional requirements
- Model is consistent with the domain model, Constitution §6, and ADR-001
