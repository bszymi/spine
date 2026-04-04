---
id: TASK-001
type: Task
title: "Redact database credentials from workspace API response"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-04
last_updated: 2026-04-04
completed: 2026-04-04
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
---

# TASK-001 — Redact Database Credentials from Workspace API Response

---

## Purpose

`handleWorkspaceGet` in `/internal/gateway/handlers_workspaces.go` (lines 146-152) returns the full `database_url` in the API response. This URL typically contains the PostgreSQL connection string with username and password (e.g., `postgres://spine:spine@host:5432/spine`). If the operator token is compromised or the response is logged/intercepted, database credentials are fully exposed.

---

## Deliverable

Remove or redact `database_url` from the `handleWorkspaceGet` response. Options:

1. **Remove entirely** — clients don't need the raw connection string
2. **Redact password** — return `postgres://spine:***@host:5432/spine`
3. **Return only host:port/dbname** — strip credentials entirely

Also audit `handleWorkspaceList` for the same issue.

---

## Acceptance Criteria

- GET `/api/v1/workspaces/:id` does not expose database passwords
- GET `/api/v1/workspaces` does not expose database passwords
- Existing workspace provisioning and lifecycle tests pass
