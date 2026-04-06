---
id: INIT-010
type: Initiative
title: Actor Skills, Task Eligibility, and Execution Queries
status: In Progress
owner: bszymi
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: related_to
    target: /initiatives/INIT-003-execution-system/initiative.md
  - type: related_to
    target: /governance/constitution.md
  - type: related_to
    target: /architecture/components.md
---

# INIT-010 — Actor Skills, Task Eligibility, and Execution Queries

---

## 1. Intent

Complete the foundational Spine Core capabilities required for the management platform to function. Spine already has a working actor model, artifact system, workflow engine, AI actor integration, and event/audit infrastructure. What remains is:

1. **Formal skill system** — Actors currently declare capabilities as bare strings. Spine needs a first-class skill model so capabilities can be managed, queried, and matched against workflow requirements.

2. **Execution readiness infrastructure** — Dependency and blocking detection must be explicit and queryable. An API must exist for discovering tasks ready for execution based on actor type, skills, and dependency status.

3. **Claim and release model** — The current assignment model is push-based. Actors (especially AI agents and human dashboards) need the ability to pull/claim tasks from a pool and release them back.

4. **Execution-focused projections and queries** — Artifact projections exist but lack execution state views. The system needs projections and query APIs optimized for operational task discovery.

These capabilities are prerequisites for the management platform's dashboards, AI execution engines, and human task interfaces.

---

## 2. Scope

### In Scope

- Skill domain model, persistence, and assignment to actors
- Workflow step skill requirement declarations
- Skill eligibility validation during assignment
- Skill-based actor query interface
- Dependency and blocking detection for tasks
- Execution candidate discovery API
- Task claim and release operations
- Execution-focused projection schema
- Execution query API (ready, blocked, assigned, eligible)

### Out of Scope

- Management platform UI (separate initiative)
- Actor registration or lifecycle changes (already complete in INIT-003)
- Event/audit system changes (already complete)
- AI actor interface changes (already complete)
- Artifact creation/validation changes (already complete)
- Dashboards, billing, agent marketplace (management platform)

---

## 3. Success Criteria

This initiative is successful when:

1. Skills are first-class entities that can be created, assigned to actors, and required by workflow steps
2. Assignment and claiming validate skill eligibility — assignment fails if required skills are missing
3. Tasks expose blocked/ready status based on dependency completion
4. An API exists to query tasks ready for execution filtered by actor type, skills, and dependencies
5. Actors can claim tasks from a pool and release them for reallocation
6. Execution projections support operational queries for task discovery

---

## 4. Work Breakdown

| Epic | Title | Dependencies |
|------|-------|-------------|
| EPIC-001 | Skill System and Capability Matching | None |
| EPIC-002 | Task Eligibility and Execution Readiness | EPIC-001 |
| EPIC-003 | Workflow Step Assignment and Claiming | EPIC-001, EPIC-002 |
| EPIC-004 | Execution Projections and Query Infrastructure | EPIC-002, EPIC-003 |

---

## 5. Constraints and Non-Negotiables

- Skills must be workspace-scoped
- Skill matching must apply uniformly to all actor types (human, AI, automated)
- Claiming must go through the same governance checks as push-based assignment
- Projections must remain disposable and rebuildable from Git artifacts
- All operations must emit audit events

---

## 6. Risks

- **Skill proliferation:** Without governance, workspaces may accumulate redundant or overlapping skills. Mitigation: keep the skill model simple; category and status fields allow cleanup.
- **Claim contention:** Multiple actors claiming the same task simultaneously. Mitigation: atomic claim operation with optimistic locking.
- **Projection staleness:** Execution projections may lag behind Git state. Mitigation: event-driven updates with sync state tracking (existing pattern).

---

## 7. Links

- Constitution: `/governance/constitution.md`
- Components: `/architecture/components.md`
- Execution System: `/initiatives/INIT-003-execution-system/initiative.md`
