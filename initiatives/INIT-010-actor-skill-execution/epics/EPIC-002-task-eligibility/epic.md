---
id: EPIC-002
type: Epic
title: Task Eligibility and Execution Readiness
status: Completed
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
owner: bszymi
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/initiative.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
---

# EPIC-002 — Task Eligibility and Execution Readiness

---

## 1. Purpose

Spine must determine whether a task is ready to be executed based on state, dependencies, and workflow requirements — and expose this determination through a queryable API.

Precondition evaluation already exists in the engine. This epic adds explicit dependency/blocking detection and an API for discovering execution-ready tasks. These are prerequisites for AI execution engines and human dashboards.

---

## 2. Scope

### In Scope

- Explicit dependency and blocking detection with queryable blocked status
- Execution candidate discovery API filtered by actor type, skills, and dependencies

### Out of Scope

- Task readiness evaluation logic (already exists in engine preconditions)
- Actor-type eligibility checks (already exist via execution mode filtering)
- Readiness scoring or prediction

---

## 3. Tasks

| Task | Title | Dependencies |
|------|-------|-------------|
| TASK-001 | Dependency and Blocking Detection | None |
| TASK-002 | Execution Candidate Discovery API | TASK-001, TASK-003 |
| TASK-003 | Wire Blocking Detection into Run Lifecycle and Bootstrap | TASK-001 |
| TASK-004 | Apply Candidate Filters and Exclude Deprecated Skills | TASK-002 |
| TASK-005 | Improve Candidate Discovery and Blocking Transition Test Coverage | TASK-004 |
