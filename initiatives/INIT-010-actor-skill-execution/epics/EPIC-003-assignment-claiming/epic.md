---
id: EPIC-003
type: Epic
title: Workflow Step Assignment and Claiming
status: In Progress
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
owner: bszymi
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/initiative.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/epic.md
---

# EPIC-003 — Workflow Step Assignment and Claiming

---

## 1. Purpose

Extend the current push-based assignment model with pull-based claiming and release operations. Actors (especially AI agents and human dashboards) must be able to claim tasks from a pool and release them for reallocation.

The existing `DeliverAssignment()` push model remains — this epic adds complementary claim and release operations.

---

## 2. Scope

### In Scope

- Task claim operation with eligibility validation
- Task release operation for reallocation
- Audit events for claim and release actions

### Out of Scope

- Changes to existing push-based assignment (already works)
- Assignment audit logging (already emits events)
- Assignment timeout handling (already exists)

---

## 3. Tasks

| Task | Title | Dependencies |
|------|-------|-------------|
| TASK-001 | Task Claim Operation | None |
| TASK-002 | Task Release Operation | TASK-001 |
| TASK-003 | Fix Claim/Release Bugs: Nil-Check, Skill Validation, Atomicity | TASK-001, TASK-002 |
