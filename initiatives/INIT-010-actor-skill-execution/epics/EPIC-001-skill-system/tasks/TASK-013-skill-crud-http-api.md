---
id: TASK-013
type: Task
title: "Add skill CRUD HTTP API endpoints"
status: Completed
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-07
last_updated: 2026-04-07
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
---

# TASK-013 — Add skill CRUD HTTP API endpoints

---

## Purpose

The skill domain model, store, and service are fully implemented but not exposed via HTTP. The Spine Management Platform needs to proxy skill operations through the gateway. This is a gateway-only task — all internal methods already exist.

## Deliverable

Add HTTP handlers in `internal/gateway/` and register routes in `routes.go`:

```
POST   /api/v1/skills                         — Create skill
GET    /api/v1/skills                         — List skills (optional ?category= filter)
GET    /api/v1/skills/{skill_id}              — Get skill by ID
PATCH  /api/v1/skills/{skill_id}              — Update skill (name, description, category)
POST   /api/v1/skills/{skill_id}/deprecate    — Deprecate skill (set status to deprecated)
```

Each handler should:
- Use existing `store.CreateSkill`, `store.GetSkill`, `store.UpdateSkill`, `store.ListSkills`, `store.ListSkillsByCategory` methods
- Follow the existing gateway handler patterns (auth, error handling, JSON encoding)
- Require authentication via `authorize(w, r, "skill.<operation>")`
- **Register `skill.create`, `skill.read`, `skill.update`, `skill.deprecate` operations in `internal/auth/permissions.go`** so the authorize calls don't 403 on valid tokens

## Acceptance Criteria

- All five endpoints return correct responses
- Permission entries added to `auth/permissions.go` for all skill operations
- List supports optional `?category=` query parameter
- Deprecate sets status to `deprecated` and returns updated skill
- Auth required on all endpoints
- Error responses follow existing gateway error format
