---
id: TASK-015
type: Task
title: "[Low priority] Add projection_lag_ms and active_runs to HealthResponse"
status: Completed
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-015 — [Low priority] Add projection_lag_ms and active_runs to HealthResponse

## Purpose

The spec's `HealthResponse` includes optional `projection_lag_ms` (integer) and `active_runs` (integer) fields for operational visibility. The current health handler only returns `status` and `components`.

## Deliverable

- Query projection sync state to compute `projection_lag_ms` (time since last sync)
- Count active runs from the store for `active_runs`
- Include both fields in the health response

## Acceptance Criteria

- `GET /system/health` response includes `projection_lag_ms` when projection service is available
- `GET /system/health` response includes `active_runs` when store is available
- Missing values are omitted (not zero) when the service is unavailable
