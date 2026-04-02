---
id: EPIC-006
type: Epic
title: Observability & Workspace Identity
status: Pending
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
---

# EPIC-006 — Observability & Workspace Identity

---

## Purpose

Ensure workspace identity is present in all operational signals — logs, metrics, and traces. In shared runtime mode, the ability to filter and attribute signals per workspace is essential for debugging, monitoring, and accountability.

---

## Key Work Areas

- Add `workspace_id` field to structured log context (set in middleware, propagated via context)
- Add `workspace_id` label to all metrics emissions
- Background service logs include `workspace_id` for the workspace being processed
- Per-workspace health or status reporting

---

## Primary Outputs

- Updated `internal/observe/` — workspace-aware logging and metrics
- Updated gateway middleware to inject workspace into log context
- Tests verifying workspace identity in log and metric output

---

## Acceptance Criteria

- Every log line emitted during an API request includes `workspace_id`
- Every metric emitted during a request includes a `workspace_id` label
- Background service logs include `workspace_id` for each workspace processed
- It is possible to filter all operational signals for a single workspace
- Single-workspace mode still emits workspace identity (uses the configured workspace ID)
