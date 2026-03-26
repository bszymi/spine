---
id: TASK-009
type: Task
title: Known Limitations Cleanup
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
---

# TASK-009 — Known Limitations Cleanup

## Purpose

Address the key known limitations documented in KNOWN-LIMITATIONS.md that affect production readiness.

## Deliverable

### WriteContext and Branch-Scoped Writes
- Wire write_context into artifact operations so writes go to the correct branch
- Branch-scoped reads/writes for step execution context

### Idempotency Deduplication
- Implement idempotency-key deduplication store
- Prevent duplicate run creation and step submissions

### Queue Consumer Delivery
- Wire step assignment delivery to actor providers
- Implement basic human notification (log or webhook placeholder)
- Connect AI provider for automated step execution

### Workflow Binding in API
- Wire ResolveBinding into the gateway run creation path
- Ensure work_type filtering works

### Run StartedAt Persistence
- Persist started_at timestamp via UpdateRunStatus or dedicated method
- Ensure run duration metrics work with persisted timestamps

## Acceptance Criteria

- Artifact writes respect the run's branch context
- Duplicate submissions are rejected with idempotency key
- Step assignments reach actor providers through the queue
- Workflow binding resolution works in the API run start path
- Run duration metrics accurately reflect wall-clock time
- KNOWN-LIMITATIONS.md updated to remove resolved items
