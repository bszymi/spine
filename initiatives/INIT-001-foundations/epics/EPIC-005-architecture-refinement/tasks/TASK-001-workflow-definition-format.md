---
id: TASK-001
type: Task
title: Workflow Definition Format
status: In Progress
epic: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
---

# TASK-001 — Workflow Definition Format

---

## Problem

The domain model defines Workflow Definition as a core entity with attributes (states, transitions, steps, divergence/convergence points), and ADR-001 establishes that workflow definitions must be stored as versioned Git artifacts. However, no concrete format specification exists.

Without a defined format, the Workflow Engine cannot parse workflow definitions, and contributors cannot author them. The abstract model is insufficient for implementation.

## Objective

Define the concrete, parseable format for workflow definitions stored as Git artifacts.

## Deliverable

`/architecture/workflow-definition-format.md`

Content should define:

- File format (YAML, DSL, or other) and structure
- How states and transitions are declared
- How steps are defined within a workflow (step type, actor type, preconditions, validation, retry, timeout)
- How divergence points (parallel execution) and convergence points (evaluation steps) are expressed
- How workflow definitions reference artifact types they govern
- Versioning semantics — how the Workflow Engine resolves which version of a workflow applies to a given Run
- Example workflow definition for a common case (e.g., task execution with review)

## Acceptance Criteria

- Format is concrete and machine-parseable
- All domain model Workflow Definition attributes are representable
- Divergence and convergence points can be expressed
- At least one complete example workflow is provided
- Format is consistent with the domain model and ADR-001
