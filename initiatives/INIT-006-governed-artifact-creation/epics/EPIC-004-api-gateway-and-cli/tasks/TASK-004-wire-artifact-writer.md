---
id: TASK-004
type: Task
title: Wire ArtifactWriter in server setup
status: Completed
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
---

# TASK-004 — Wire ArtifactWriter in Server Setup

---

## Purpose

Connect the `ArtifactWriter` dependency to the orchestrator during server startup so planning runs can write artifacts.

---

## Deliverable

### 1. Server startup

`cmd/spine/main.go`

In the `serve` command setup:
- Pass the existing `artifactSvc` (which implements `ArtifactWriter`) to the orchestrator via `WithArtifactWriter()`

### 2. Scenario test harness

`internal/scenariotest/harness/runtime.go`

In `WithRuntimeOrchestrator()` (or wherever the test orchestrator is constructed):
- Pass the test artifact service to the orchestrator via `WithArtifactWriter()`

Without this, EPIC-006 scenario tests will hit the `artifactWriter == nil` guard in `StartPlanningRun()` and fail with `ErrUnavailable`.

---

## Acceptance Criteria

- `artifact.Service` satisfies the `ArtifactWriter` interface
- Orchestrator receives the writer during server startup
- Orchestrator receives the writer in the scenario test harness
- No changes needed if `artifact.Service` isn't available (graceful degradation)
