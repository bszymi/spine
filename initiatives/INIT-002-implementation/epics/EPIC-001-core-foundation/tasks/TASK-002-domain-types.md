---
id: TASK-002
type: Task
title: Domain Types
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
---

# TASK-002 — Domain Types

## Purpose

Define the core domain types in `internal/domain` that all components share.

## Deliverable

Go types for:
- Artifact (path, ID, type, status, metadata, content, links)
- Workflow Definition (ID, version, status, applies_to, steps, divergence/convergence points)
- Step Definition (ID, type, execution, outcomes, retry, timeout)
- Run (ID, task_path, workflow, status, trace_id, timestamps)
- StepExecution (ID, run_id, step_id, status, actor_id, attempt, outcome, error_detail)
- DivergenceContext, Branch, ConvergenceResult
- Event (ID, type, timestamp, actor_id, run_id, artifact_path, payload)
- Actor (ID, type, name, role, capabilities, status)
- Error types and failure classifications
- Status enums matching runtime-schema.md constraints

## Acceptance Criteria

- All domain types compile and are importable by other packages
- Status enums match artifact-schema.md §6 and runtime-schema.md §4.0
- Types include JSON serialization tags
- Unit tests verify enum validity and type construction
