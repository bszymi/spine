---
id: TASK-005
type: Task
title: Task-to-Workflow Binding Model
status: Pending
epic: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/epic.md
  - type: depends_on
    target: /initiatives/INIT-001-foundations/epics/EPIC-005-architecture-refinement/tasks/TASK-001-workflow-definition-format.md
---

# TASK-005 — Task-to-Workflow Binding Model

---

## Problem

The Workflow Definition Format (TASK-001) defines how workflows are structured, but does not specify how artifacts are bound to workflows at creation or execution time. Several open questions remain:

- Must every Task carry an explicit workflow reference, or can workflows be inferred?
- Is workflow assignment manual, template-driven, or rule-based?
- When a Run starts, how exactly is the workflow version pinned — by version field or Git SHA?
- Can a task's workflow binding change after creation, and under what governance rules?
- How does the engine validate workflow-task compatibility?
- How do branches created during divergence inherit governance from the parent Run?

Without answers, the Workflow Engine cannot resolve which workflow governs a given artifact, and workflow assignment remains ambiguous.

## Objective

Define the canonical model for how artifacts are bound to workflows, including assignment, resolution, versioning, mutability, and branch inheritance.

## Deliverable

`/architecture/task-workflow-binding.md`

Content should answer:

1. Does every Task require an explicit workflow reference?
2. Do we introduce `work_type` or equivalent classification?
3. Is workflow chosen manually, by template, or by rules?
4. When a Run starts, is the workflow version snapshotted by Git SHA?
5. Can workflow binding change after a task is created?
6. If yes, under what governance rules?
7. How does the engine validate that a workflow is compatible with the task?
8. How do branches inherit governance from the Run?

After the primary document, make cross-reference updates to:

- Domain Model
- Task Lifecycle
- Components
- Access Surface (if relevant)
- Artifact Schema (if new front-matter fields like `workflow` or `work_type` are introduced)

## Acceptance Criteria

- All 8 binding questions are answered with clear decisions
- Binding model is consistent with Workflow Definition Format, Domain Model, and ADR-001
- Cross-references in affected documents are updated
- Any new front-matter fields are reflected in Artifact Schema
