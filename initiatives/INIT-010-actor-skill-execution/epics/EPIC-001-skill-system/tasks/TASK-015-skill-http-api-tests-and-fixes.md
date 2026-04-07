---
id: TASK-015
type: Task
title: "Add skill HTTP API tests and fix actor-skill remove efficiency"
status: Completed
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
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-014-actor-skill-association-http-api.md
---

# TASK-015 — Add skill HTTP API tests and fix actor-skill remove efficiency

---

## Purpose

TASK-013 and TASK-014 added skill CRUD and actor-skill association HTTP endpoints but without gateway-level handler tests. Additionally, the `handleActorSkillRemove` handler uses an inefficient pattern — it calls `ListActorSkills` and iterates all skills to check existence before deleting, instead of checking rows affected from the DELETE operation.

## Deliverable

1. **Fix `handleActorSkillRemove`** — Replace the list-and-scan approach with a store method that returns rows affected, or use a targeted existence check.

2. **Gateway unit tests** (`internal/gateway/handlers_skills_test.go`) covering:
   - Skill CRUD: create, list, list with category filter, get, update, deprecate
   - Actor-skill: assign, assign idempotent, remove, remove 404, list

3. **Scenario tests** (`internal/scenariotest/scenarios/skill_http_api_test.go`) covering the HTTP API golden path through the full stack.

## Acceptance Criteria

- `handleActorSkillRemove` no longer calls `ListActorSkills` for existence checking
- Unit tests cover all skill and actor-skill HTTP endpoints
- Scenario tests exercise the HTTP API through the gateway
- All tests pass
