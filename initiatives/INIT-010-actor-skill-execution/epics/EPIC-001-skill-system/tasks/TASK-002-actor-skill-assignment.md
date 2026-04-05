---
id: TASK-002
type: Task
title: "Actor Skill Assignment"
status: Completed
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-05
completed: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-001-define-skill-domain-model.md
---

# TASK-002 — Actor Skill Assignment

---

## Purpose

Implement the ability to attach and remove skills from actors. This replaces the current `Capabilities: []string` field with a proper many-to-many relationship between actors and skills.

---

## Deliverable

1. Add actor-skill association to the store:
   - `AddSkillToActor(ctx, actorID, skillID) error`
   - `RemoveSkillFromActor(ctx, actorID, skillID) error`
   - `ListActorSkills(ctx, actorID) ([]Skill, error)`

2. Add database migration for actor_skills junction table

3. Update actor selection logic in `internal/actor/selection.go` to use skill lookups instead of raw capability string matching

4. Provide backward compatibility: if actors still have `Capabilities` strings, they should continue to work until migrated

5. Update documentation:
   - Update `/architecture/actor-model.md` to describe actor-skill associations replacing bare capabilities
   - Update `/architecture/data-model.md` to document the actor_skills junction table
   - Update `/architecture/api-operations.md` to document skill assignment operations

---

## Acceptance Criteria

- Skills can be added to and removed from actors
- Actor skills are queryable by actor ID
- Actor selection filters by skill associations
- Existing capability-based matching continues to work during migration
- Integration tests cover add, remove, and list operations
- Architecture documentation is updated to reflect actor-skill associations
