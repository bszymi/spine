---
id: TASK-016
type: Task
title: "Guard skill deprecation when referenced by active workflows"
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

# TASK-016 — Guard skill deprecation when referenced by active workflows

---

## Purpose

The `POST /api/v1/skills/{skill_id}/deprecate` endpoint sets a skill's status to `deprecated` with no validation. If the skill is referenced in `required_skills` on active workflow step definitions, deprecating it creates a silent inconsistency:

- `ValidateSkills` will start emitting warnings for previously clean workflows
- No actors can be newly assigned the deprecated skill (if assignment validation is added later)
- Existing runs are unaffected but future runs using those workflows will have skill mismatches

The deprecation endpoint should check whether the skill name appears in any active workflow's `required_skills` and either block the operation or return a warning with a `force` override.

## Deliverable

1. **Pre-deprecation check in `handleSkillDeprecate`** — Before setting status to `deprecated`, query active workflow projections and scan their step definitions for `required_skills` referencing the skill's name. If found:
   - Default: return 409 Conflict with the list of referencing workflows
   - With `?force=true` query param: proceed with deprecation, return the skill with a `warnings` field listing the affected workflows

2. **Helper function** — `FindWorkflowsReferencingSkill(ctx, skillName, store) ([]string, error)` that queries `ListActiveWorkflowProjections` and inspects step definitions. This can live in `internal/workflow/` alongside `skill_validation.go`.

3. **Tests** — Unit tests for the guard (deprecate blocked, deprecate with force, deprecate unrelated skill succeeds).

## Acceptance Criteria

- Deprecating a skill referenced in active workflows returns 409 by default
- Response includes list of workflow IDs/paths that reference the skill
- `?force=true` overrides the guard and deprecates anyway, returning warnings
- Deprecating a skill not referenced in any workflow succeeds as before (200)
- Unit tests cover all three cases
