---
id: TASK-010
type: Task
title: "Add auth tables to CleanupTestData"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
---

# TASK-010 — Add Auth Tables to CleanupTestData

---

## Purpose

`CleanupTestData` in `/internal/store/testutil.go` omits `auth.actors`, `auth.tokens`, `auth.skills`, and `auth.actor_skills` tables. Actor/skill data leaks across scenario tests causing INSERT constraint violations when tests share hardcoded IDs like `"dev-1"`, `"sk-exec"`.

---

## Deliverable

Add `auth.actor_skills`, `auth.skills`, `auth.actors`, and `auth.tokens` to the `tables` slice in `CleanupTestData`, ordered to respect FK constraints (junction tables first).

---

## Acceptance Criteria

- Auth tables are truncated between scenario tests
- No INSERT constraint violations from leaked actor/skill data
- Full scenario test suite passes in a single `go test` invocation
