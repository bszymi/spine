---
id: TASK-009
type: Task
title: Improve test coverage for planning run code paths
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

# TASK-009 — Improve Test Coverage for Planning Run Code Paths

---

## Purpose

Current coverage: gateway 63.8%, engine 80.3%. The planning run feature added new code paths that need thorough coverage. This task fills gaps in handler tests, resolveWriteContext tests, and edge case coverage.

---

## Deliverable

### Gateway tests (`internal/gateway/gateway_test.go`)

Missing coverage:
- `resolveWriteContext` with planning run (no task_path) — returns branch
- `resolveWriteContext` with planning run (with task_path) — still returns branch
- `resolveWriteContext` with standard run, missing task_path — returns error
- `resolveWriteContext` with standard run, mismatched task_path — returns error
- `resolveWriteContext` with non-active run — returns error
- `handleArtifactCreate` with planning run write context
- `handleArtifactUpdate` with planning run write context (allowed for branch-local artifacts)
- Planning run start with engine error — proper error propagation
- Response body validation: verify `mode` field is present in standard and planning responses

### Engine tests (`internal/engine/run_test.go`)

Missing coverage:
- `StartPlanningRun` with store CreateRun failure — verify branch cleanup
- `StartPlanningRun` with workflow timeout configured
- `StartPlanningRun` with validation warnings (non-fatal)

### Store tests (`internal/store/postgres_integration_test.go`)

Missing coverage:
- Database-level mode DEFAULT (TASK-005 follow-up)
- CreateRun in transaction with planning mode

---

## Acceptance Criteria

- Gateway coverage reaches 70%+
- Engine coverage reaches 85%+
- All planning run code paths have at least one test
- All existing tests continue to pass
