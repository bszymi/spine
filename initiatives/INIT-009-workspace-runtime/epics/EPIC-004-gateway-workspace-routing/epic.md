---
id: EPIC-004
type: Epic
title: Gateway Workspace Routing
status: Pending
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
  - type: related_to
    target: /architecture/components.md
---

# EPIC-004 — Gateway Workspace Routing

---

## Purpose

Wire workspace resolution into the HTTP request path and CLI commands. Implement the service pool described in [components.md §6.5](/architecture/components.md) so that each request executes against workspace-scoped services without modifying Spine's internal service logic.

---

## Key Work Areas

- Gateway middleware to extract and validate workspace ID from requests
- Service pool that lazily creates and caches per-workspace service sets
- Context propagation — workspace-scoped services set in `context.Context`
- Handler refactoring to pull services from context
- CLI `--workspace` flag and persistent workspace config
- Connection pool management — idle workspace eviction
- Error handling for unknown or misconfigured workspace IDs

---

## Primary Outputs

- `internal/workspace/pool.go` — service pool for per-workspace dependency sets
- Gateway middleware in `internal/gateway/middleware.go`
- Updated handler wiring in `internal/gateway/server.go`
- CLI workspace flag in `cmd/spine/`
- Integration tests demonstrating two workspaces in isolation within one runtime

---

## Acceptance Criteria

- In shared mode, workspace-scoped API requests require a workspace ID; missing or invalid IDs return clear errors
- In single mode, workspace ID is optional — middleware falls back to the single configured workspace
- Global system routes (health, metrics, readiness) are exempt from workspace resolution
- The service pool lazily initializes a complete service set on first request for a workspace
- Subsequent requests for the same workspace reuse cached services
- Two concurrent workspaces in the same runtime cannot access each other's data or Git
- CLI commands accept `--workspace` flag or read from persisted config
- Idle workspace service sets are evicted after a configurable timeout
