---
id: TASK-014
type: Task
title: "Add actor-skill association HTTP API endpoints"
status: Pending
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-07
last_updated: 2026-04-07
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-013-skill-crud-http-api.md
---

# TASK-014 — Add actor-skill association HTTP API endpoints

---

## Purpose

Expose actor-skill assignment and query operations via HTTP so the Spine Management Platform can manage which actors have which skills.

## Deliverable

Add HTTP handlers in `internal/gateway/` and register routes in `routes.go`:

```
POST   /api/v1/actors/{actor_id}/skills/{skill_id}   — Assign skill to actor
DELETE /api/v1/actors/{actor_id}/skills/{skill_id}   — Remove skill from actor
GET    /api/v1/actors/{actor_id}/skills              — List actor's skills
```

Each handler should:
- Use existing `store.AddSkillToActor`, `store.RemoveSkillFromActor`, `store.ListActorSkills` methods
- Assign returns 200 with the skill (idempotent — existing assignment returns success)
- Remove returns 204 on success
- List returns array of Skill objects

## Acceptance Criteria

- All three endpoints return correct responses
- Assign is idempotent (no error on duplicate)
- Remove returns 404 if assignment doesn't exist
- Auth required on all endpoints
- Error responses follow existing gateway error format
