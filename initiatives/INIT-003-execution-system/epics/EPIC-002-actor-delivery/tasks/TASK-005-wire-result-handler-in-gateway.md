---
id: TASK-005
type: Task
title: "Wire ResultHandler adapter in gateway startup"
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-002-actor-delivery/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-002-actor-delivery/epic.md
  - type: follows
    target: /initiatives/INIT-003-execution-system/epics/EPIC-002-actor-delivery/tasks/TASK-003-result-ingestion.md
---

# TASK-005 — Wire ResultHandler adapter in gateway startup

---

## Purpose

TASK-003 implemented `engine.Orchestrator.IngestResult` and the gateway's `handleStepSubmit` handler, but the `ResultHandler` field is not set in `gateway.ServerConfig` during server startup in `cmd/spine/main.go`. All calls to `POST /api/v1/steps/{id}/submit` fail with "result handler not configured".

## Deliverable

- Add `resultAdapter` struct in `cmd/spine/main.go` following the existing `runAdapter` / `planningRunAdapter` pattern
- Map `gateway.ResultSubmission` → `engine.SubmitRequest` and `engine.IngestResponse` → `gateway.ResultResponse`
- Wire `ResultHandler: &resultAdapter{orch: orch}` into `gateway.ServerConfig`

## Acceptance Criteria

- `POST /api/v1/steps/{execution_id}/submit` with `outcome_id` successfully advances the workflow step
- Step transitions (execute → review → commit) work end-to-end via the API
- No new packages or interfaces needed — adapter only
