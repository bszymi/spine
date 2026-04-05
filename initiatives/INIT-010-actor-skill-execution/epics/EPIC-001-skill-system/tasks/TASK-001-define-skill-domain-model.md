---
id: TASK-001
type: Task
title: "Define Skill Domain Model"
status: Pending
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
---

# TASK-001 — Define Skill Domain Model

---

## Purpose

Define the core data structure representing skills in Spine. Currently capabilities are opaque strings on the actor struct. This task introduces a first-class skill entity that can be persisted, managed, and matched.

---

## Deliverable

1. Define `Skill` struct in `internal/domain/` with fields:
   - `SkillID` (string, UUID)
   - `Name` (string, unique within workspace)
   - `Description` (string)
   - `Category` (string, e.g. "development", "review", "operations")
   - `Status` (active, deprecated)
   - `CreatedAt`, `UpdatedAt` (time.Time)

2. Define `SkillStatus` type with valid values and transitions

3. Add skill persistence to the store:
   - `CreateSkill(ctx, skill) error`
   - `GetSkill(ctx, skillID) (Skill, error)`
   - `UpdateSkill(ctx, skill) error`
   - `ListSkills(ctx) ([]Skill, error)`
   - `ListSkillsByCategory(ctx, category) ([]Skill, error)`

4. Add database migration for skills table

Skills must be workspace-scoped.

5. Update documentation:
   - Update `/architecture/domain-model.md` to include the Skill entity and its relationships to Actor
   - Update `/architecture/data-model.md` to document the skills table schema
   - Update `/architecture/actor-model.md` to describe the skill system as the formalization of capabilities
   - Update `/architecture/runtime-schema.md` if it documents database tables

---

## Acceptance Criteria

- Skill struct is defined with all required fields
- Store methods for CRUD operations are implemented
- Database migration creates the skills table
- Unit tests cover skill creation, retrieval, update, and listing
- Skills are scoped to the workspace (no cross-workspace leakage)
- Architecture documentation is updated to reflect the skill domain model
