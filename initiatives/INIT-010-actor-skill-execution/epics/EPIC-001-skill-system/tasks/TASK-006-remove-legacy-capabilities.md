---
id: TASK-006
type: Task
title: "Remove legacy capabilities field from Actor"
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
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-002-actor-skill-assignment.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-003-workflow-skill-requirement-definition.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-004-skill-eligibility-validation.md
---

# TASK-006 — Remove Legacy Capabilities Field from Actor

---

## Purpose

The `Capabilities []string` field on the Actor domain model and the `capabilities jsonb` column in `auth.actors` are the legacy capability system that skills replace. Once all capability matching resolves through the skill registry (TASK-003) and eligibility validation uses skills (TASK-004), the legacy field is dead code.

Leaving it in place creates confusion — two sources of truth for what an actor can do — and risks divergence between the capabilities field and the actor_skills table.

---

## Deliverable

1. Remove `Capabilities []string` from `domain.Actor` struct in `internal/domain/actor.go`

2. Remove all capability-related code in the store layer:
   - Remove `capabilities` from all actor SQL queries (SELECT, INSERT, UPDATE)
   - Remove `json.Marshal`/`json.Unmarshal` for capabilities in `internal/store/postgres.go`

3. Remove capability-based matching fallback in `internal/actor/selection.go` (added in TASK-002 for backward compatibility)

4. Remove capability-based fallback in workflow capability resolution (added in TASK-003)

5. Add database migration to drop the column:
   ```sql
   ALTER TABLE auth.actors DROP COLUMN capabilities;
   ```

6. Update all tests that set or assert on `Capabilities` field

7. Update documentation:
   - Update `/architecture/actor-model.md` to remove references to the `capabilities` field on actor records
   - Update `/architecture/domain-model.md` to remove `capabilities` from Actor attributes
   - Update `/architecture/runtime-schema.md` to remove `capabilities` from `auth.actors` if documented

---

## Acceptance Criteria

- `Capabilities` field no longer exists on the Actor struct
- `capabilities` column no longer exists in `auth.actors` table
- No code references the legacy capabilities field
- All capability matching goes through the skill registry exclusively
- `go build ./...` passes
- `go test ./...` passes
- Architecture documentation reflects the removal
