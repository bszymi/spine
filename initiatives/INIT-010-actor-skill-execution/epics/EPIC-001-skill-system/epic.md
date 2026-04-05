---
id: EPIC-001
type: Epic
title: Skill System and Capability Matching
status: Completed
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
owner: bszymi
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/initiative.md
---

# EPIC-001 — Skill System and Capability Matching

---

## 1. Purpose

Replace the current bare-string capability model with a first-class skill system. Actors must have associated skills that determine whether they are eligible to execute certain workflow steps. Skills represent capabilities rather than roles.

Currently, actors declare `Capabilities: []string` and workflows declare `required_capabilities: []string`. Matching is simple string equality with no management, querying, or validation infrastructure. This epic introduces skills as workspace-scoped entities with lifecycle management.

---

## 2. Scope

### In Scope

- Skill domain model (skill_id, name, description, category, status)
- Skill persistence (create, update, disable, list)
- Actor skill assignment (add, remove, list)
- Workflow step skill requirement declarations
- Skill eligibility validation during step assignment
- Skill-based actor query interface

### Out of Scope

- Skill proficiency levels or scoring
- Skill versioning or compatibility matrices
- Skill inference or auto-detection
- UI for skill management (management platform)

---

## 3. Tasks

| Task | Title | Dependencies |
|------|-------|-------------|
| TASK-001 | Define Skill Domain Model | None |
| TASK-002 | Actor Skill Assignment | TASK-001 |
| TASK-003 | Upgrade required_capabilities to Resolve Against Skill Registry | TASK-001 |
| TASK-004 | Skill Eligibility Validation | TASK-002, TASK-003 |
| TASK-005 | Skill Query Interface | TASK-001, TASK-002 |
| TASK-006 | Remove Legacy Capabilities Field from Actor | TASK-002, TASK-003, TASK-004 |
| TASK-007 | Backfill Existing Capabilities into Skills During Migration | TASK-006 |
| TASK-008 | Propagate Skill Lookup Failures in Actor Selection | None |
| TASK-009 | Filter Deprecated Skills from Eligibility and Fix Stale Docs | None |
