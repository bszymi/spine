---
id: TASK-002
type: Task
title: Workspace label on metrics
status: Completed
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-006-observability-workspace-identity/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-006-observability-workspace-identity/epic.md
---

# TASK-002 — Workspace label on metrics

---

## Purpose

Add workspace_id as a label on all metrics so dashboards and alerts can be scoped per workspace.

## Deliverable

Updates to `internal/observe/metrics.go`.

Content should define:

- All request-scoped metrics (HTTP latency, operation counts, error rates) include `workspace_id` label
- Background service metrics include `workspace_id` label
- Cardinality is bounded by workspace count (acceptable for label use)

## Acceptance Criteria

- HTTP request metrics include `workspace_id` label
- Background processing metrics include `workspace_id` label
- Metrics can be filtered and grouped by workspace in monitoring tools
