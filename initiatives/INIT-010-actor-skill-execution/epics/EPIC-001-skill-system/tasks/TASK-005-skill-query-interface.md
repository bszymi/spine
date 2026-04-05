---
id: TASK-005
type: Task
title: "Skill Query Interface"
status: Pending
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-001-define-skill-domain-model.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-002-actor-skill-assignment.md
---

# TASK-005 — Skill Query Interface

---

## Purpose

Provide query capability to list actors eligible for a workflow step based on skill requirements. This enables dashboards and AI execution engines to discover who can do what.

---

## Deliverable

1. Add store query method:
   - `ListActorsBySkills(ctx, skillNames []string) ([]Actor, error)` — returns actors possessing all specified skills

2. Add service-level query:
   - `FindEligibleActors(ctx, runID string, stepID string) ([]Actor, error)` — resolves the step's required capabilities from the workflow definition within the run context, then finds matching actors. Both `runID` and `stepID` are needed because step IDs (e.g. `execute`, `review`) are only unique within a workflow, not globally.

3. Expose through internal API (to be surfaced via gateway in EPIC-004)

4. Update documentation:
   - Update `/architecture/api-operations.md` to document the skill query endpoints
   - Update `/architecture/actor-model.md` to describe eligible-actor discovery through skills

---

## Acceptance Criteria

- Query returns only actors with all required skills (AND matching)
- Query respects actor status (only active actors)
- Query handles empty skill requirements (all active actors eligible)
- Performance is acceptable with reasonable actor/skill counts
- Unit and integration tests cover various matching scenarios
- Architecture documentation is updated to reflect skill query interface
