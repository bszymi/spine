---
id: TASK-011
type: Task
title: "Fix broader workspace isolation gap in shared mode"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
---

# TASK-011 — Fix Broader Workspace Isolation Gap in Shared Mode

---

## Purpose

`cmd/spine/main.go` (lines 370-389) never constructs a `ServicePool`, so `internal/gateway/server.go` (lines 238-270) falls back to the single global Store/Artifacts/ProjSync for most services in shared workspace mode. The isolation bypass is not limited to the four execution query handlers (TASK-006) but affects most of the gateway.

---

## Deliverable

Wire the `ServicePool` into the gateway when `SPINE_WORKSPACE_MODE=shared`, ensuring all workspace-scoped service accessors (`storeFrom`, `artifactsFrom`, `projQueryFrom`, etc.) resolve to workspace-specific instances.

---

## Acceptance Criteria

- In shared mode, each workspace uses its own store, artifact service, and projection service
- No cross-workspace data leakage through any gateway endpoint
- Single workspace mode continues to work unchanged
