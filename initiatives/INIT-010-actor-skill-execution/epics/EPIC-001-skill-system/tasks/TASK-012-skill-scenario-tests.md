---
id: TASK-012
type: Task
title: "Add scenario tests for the skill system"
status: Completed
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-06
last_updated: 2026-04-06
completed: 2026-04-06
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-011-require-skills-on-actor-steps.md
---

# TASK-012 — Add Scenario Tests for the Skill System

---

## Purpose

The skill system had unit test coverage but no scenario/integration tests. Scenario tests validate the complete flow with a real database — skill registration, actor-skill assignment, skill-based queries, workflow execution with required skills, and schema validation enforcement.

---

## Deliverable

1. Scenario test file: `internal/scenariotest/scenarios/skill_system_test.go`
2. Four test scenarios:
   - Golden path: workflow with `required_skills` runs to completion
   - Validation: schema rejects steps without `required_skills`
   - Validation: automated steps pass without skills
   - Integration: skill registration, actor-skill assignment, AND-matching queries

---

## Acceptance Criteria

- Scenario tests cover skill CRUD, actor-skill assignment, and eligible actor queries
- Workflow with `required_skills` executes successfully end-to-end
- Schema validation correctly rejects/accepts steps based on skill requirements
- `go build ./...` passes
- `go test ./...` passes (scenario tests require `-tags scenario` and PostgreSQL)
