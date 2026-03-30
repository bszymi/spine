---
id: TASK-004
type: Task
title: Store layer unit tests
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-002-domain-model-and-storage/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-002-domain-model-and-storage/epic.md
---

# TASK-004 — Store Layer Unit Tests

---

## Purpose

Add tests that verify the `mode` field is correctly persisted and retrieved.

---

## Deliverable

Tests in `internal/store/postgres_integration_test.go` (or new test file):

- Test: create run with `mode = "planning"`, retrieve, verify mode is `"planning"`
- Test: create run with default mode (empty), retrieve, verify mode is `"standard"`
- Test: query runs filtered by mode

---

## Acceptance Criteria

- Tests run as part of `go test ./internal/store/...`
- Tests cover both planning and standard modes
- All existing store tests continue to pass
