---
id: TASK-007
type: Task
title: Decide multi-repo step routing model (ADR)
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: design
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
---

# TASK-007 - Decide Multi-Repo Step Routing Model (ADR)

---

## Purpose

Pin the multi-repo step execution model before TASK-004 implementation begins. The current TASK-004 description hand-waves an "unresolved state requiring explicit step routing or a fan-out strategy" — that ambiguity cascades into EPIC-005 merge ordering and EPIC-006 evidence-per-repo assumptions.

This task is a design-only prerequisite. No production code is changed.

## Deliverable

A new ADR (next available number under `architecture/adr/`) recording the chosen step routing model. The ADR must take a position on:

- **Resolution order** for a step's target repository: explicit `repository` in workflow step config → single affected code repo → primary repo fallback → multi-repo behavior.
- **Multi-repo behavior**: explicit-only (steps must declare `repository` when more than one code repo is affected and there is no single deterministic target) vs fan-out (the runtime expands the step into one execution per affected repo) vs hybrid.
- Implications for assignment payload shape and runner clone context.
- Implications for merge ordering (EPIC-005) and evidence collection per repo (EPIC-006).
- Migration impact on existing single-repo workflows (must remain unchanged).

## Acceptance Criteria

- ADR is committed under `architecture/adr/` with a status of `Accepted`.
- Decision is one of: explicit-only, fan-out, or a clearly-scoped hybrid — not left open.
- ADR documents at least one rejected alternative with rationale.
- TASK-004 description and acceptance criteria are updated to reflect the chosen model and link to the ADR.
- EPIC-004 acceptance criteria #3 ("Step execution payloads identify the target repository") is consistent with the ADR.
- Cross-reference is added to EPIC-005 and EPIC-006 epic docs noting which ADR governs the routing model.
