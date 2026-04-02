---
id: TASK-001
type: Task
title: Multi-workspace scheduler
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-005-background-service-scoping/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-005-background-service-scoping/epic.md
---

# TASK-001 — Multi-workspace scheduler

---

## Purpose

Adapt the scheduler to perform background maintenance across all active workspaces. As described in [components.md §6.5](/architecture/components.md), background services iterate over workspaces from `List()` and process each using its service set from the pool.

## Deliverable

Updates to `internal/scheduler/`.

Content should define:

- Scheduler receives workspace resolver (or service pool) at initialization
- On each tick, iterates over all active workspaces
- For each workspace, uses the workspace's service set to run maintenance checks
- Errors in one workspace do not block others
- In single-workspace mode, behavior is unchanged (iterates over one workspace)

## Acceptance Criteria

- Scheduler performs orphan detection per workspace independently
- Scheduler performs run timeout checks per workspace independently
- A failure in workspace A does not prevent workspace B from being processed
- Logs include workspace ID for every maintenance action
