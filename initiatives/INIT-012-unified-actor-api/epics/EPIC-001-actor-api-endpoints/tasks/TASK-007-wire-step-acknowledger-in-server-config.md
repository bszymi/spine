---
id: TASK-007
type: Task
title: "Wire StepAcknowledger in server config"
status: Completed
work_type: implementation
created: 2026-04-14
epic: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
initiative: /initiatives/INIT-012-unified-actor-api/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
  - type: depends_on
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/tasks/TASK-005-expose-step-acknowledge-endpoint.md
---

# TASK-007 — Wire StepAcknowledger in server config

---

## Context

The step acknowledge endpoint (`POST /api/v1/steps/{execution_id}/acknowledge`) returns 503 because `s.stepAcknowledger` is nil. The handler, the engine logic (`internal/engine/acknowledge.go`), and the orchestrator's `AcknowledgeStep` method all exist — but the orchestrator is not passed to the gateway's `ServerConfig`.

All other execution services are already wired: `CandidateFinder`, `StepClaimer`, `StepReleaser`, `StepExecutionLister`. The acknowledger was missed.

## Deliverable

In `cmd/spine/main.go`, add `StepAcknowledger: orch` to the `ServerConfig` initialization (around line 498), alongside the existing execution service fields.

## Acceptance Criteria

- `POST /api/v1/steps/{execution_id}/acknowledge` returns 200 (not 503) for valid requests
- Step transitions from `assigned` → `in_progress` on acknowledge
- Returns 409 if step is not in `assigned` state
- Returns 403 if actor_id doesn't match the assigned actor
