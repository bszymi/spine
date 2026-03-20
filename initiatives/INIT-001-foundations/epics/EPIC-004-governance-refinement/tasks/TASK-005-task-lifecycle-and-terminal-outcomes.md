---
id: TASK-005
type: Task
title: Define Task Lifecycle and Terminal Outcomes
status: Completed
epic: /initiatives/INIT-001-foundations/epics/EPIC-004-governance-refinement/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-004-governance-refinement/epic.md
---

# TASK-005 — Define Task Lifecycle and Terminal Outcomes

---

## Purpose

Define and document the Task Lifecycle and Terminal Outcomes model for Spine.

The current domain model and data model describe task states and execution flows, but do not explicitly define the boundary between operational state transitions (runtime-only) and terminal governance outcomes (durable artifact state changes committed to the main branch).

This task clarifies:

- When artifact state must be updated in the main branch
- When execution activity remains runtime-only
- What constitutes a terminal governance outcome

### Key Rule

Starting work on a task must not modify the main branch. Only terminal governance outcomes modify durable artifact state.

Work attempts that are abandoned without a governance decision do not require durable recording.

## Deliverable

`/governance/task-lifecycle.md`

Content should define:

1. The complete task lifecycle states and transitions
2. Which transitions are operational only (runtime state in the Workflow Engine / Runtime Store)
3. Which transitions are durable governance outcomes (committed to artifact state in the main branch)
4. Terminal outcomes and their effect on artifact updates, including:
   - `Completed` — deliverable accepted, task artifact updated
   - `Cancelled` — task withdrawn before completion, rationale recorded
   - `Rejected` — deliverable evaluated and not accepted (with or without follow-up per ADR-004)
   - `Superseded` — task replaced by successor work, link to successor recorded
   - `Abandoned` — task stopped by governance decision, rationale recorded
5. How non-terminal execution activity (starting work, assigning actors, retrying steps) remains in runtime state only
6. Cross-references to:
   - [Data Model](/architecture/data-model.md) — Git truth vs runtime state boundary
   - [System Components](/architecture/components.md) — Workflow Engine and Runtime Store behavior
   - [Domain Model](/architecture/domain-model.md) — Entity lifecycles
   - [ADR-004](/architecture/adr/ADR-004-evaluation-and-acceptance-model.md) — Evaluation and acceptance outcomes
   - [Constitution](/governance/constitution.md) — Source of Truth (§2), Governed Execution (§4)

## Acceptance Criteria

- Task lifecycle states are fully enumerated
- Operational (runtime-only) transitions are clearly distinguished from durable governance outcomes
- Terminal outcomes are defined with their effect on artifact state in the main branch
- The rule that starting work does not modify the main branch is explicitly stated
- Document is consistent with the domain model, data model, and ADR-004
