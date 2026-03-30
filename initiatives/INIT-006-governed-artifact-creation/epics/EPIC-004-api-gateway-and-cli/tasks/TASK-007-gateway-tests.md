---
id: TASK-007
type: Task
title: Gateway and API tests
status: Pending
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
---

# TASK-007 — Gateway and API Tests

---

## Purpose

Add handler-level tests for the planning run API and the relaxed write context.

---

## Deliverable

Tests in `internal/gateway/` (following existing `gateway_test.go` patterns):

**handleRunStart tests:**
- Planning mode with valid content: returns 202 with mode=planning
- Planning mode without content: returns 422
- Invalid mode value: returns 422
- Standard mode: existing behavior unchanged

**resolveWriteContext tests:**
- Planning run, no task_path: returns branch name
- Planning run, with task_path: still returns branch name (task_path ignored)
- Standard run, missing task_path: returns error
- Standard run, mismatched task_path: returns error

---

## Acceptance Criteria

- Tests use the existing `fakeStore` pattern
- All new tests pass
- All existing gateway tests continue to pass
