---
id: TASK-001
type: Task
title: Engine Orchestrator Interfaces
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-001-execution-core/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-001-execution-core/epic.md
---

# TASK-001 — Engine Orchestrator Interfaces

## Purpose

Define the engine orchestrator struct and its interfaces to the existing services (workflow engine, artifact service, actor gateway, store, event router).

## Deliverable

- `internal/engine/orchestrator.go` — Orchestrator struct with constructor and dependency injection
- `internal/engine/interfaces.go` — Internal interfaces consumed by the orchestrator
- Clear boundary between orchestrator and existing services

## Acceptance Criteria

- Orchestrator struct accepts all required service dependencies
- Interfaces are defined for workflow resolution, artifact operations, actor assignment, and store access
- Existing services satisfy the interfaces without modification (or with minimal adapter)
- Package compiles and has unit tests for construction
