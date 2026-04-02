---
id: TASK-001
type: Task
title: Workspace resolution middleware
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
---

# TASK-001 — Workspace resolution middleware

---

## Purpose

Add gateway middleware that extracts workspace ID from incoming HTTP requests and resolves it via the workspace resolver.

## Deliverable

Updates to `internal/gateway/middleware.go` and `internal/gateway/server.go`.

Content should define:

- Middleware that reads workspace ID from a request header (e.g., `X-Workspace-ID`) or URL path prefix
- Calls `WorkspaceResolver.Resolve()` to validate and load workspace config
- Stores resolved config in request context
- In shared mode, returns 400 for missing workspace ID, 404 for unknown workspace
- In single mode, falls back to the single configured workspace when no workspace ID is provided
- Global system routes (health, metrics, readiness) are exempt
- Runs after auth middleware

## Acceptance Criteria

- In shared mode, workspace-scoped requests without a valid workspace ID are rejected with 400
- In single mode, requests without a workspace ID succeed using the default workspace
- Global system routes are never subject to workspace resolution
- Resolved workspace config is available in request context for downstream handlers
