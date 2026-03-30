---
id: TASK-001
type: Task
title: Add RunMode to domain model
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

# TASK-001 — Add RunMode to Domain Model

---

## Purpose

Add the `RunMode` type and `Mode` field to `domain.Run` so runs can distinguish between standard execution and planning creation.

---

## Deliverable

`internal/domain/run.go`

Add:
- `RunMode` string type with constants `RunModeStandard` ("standard") and `RunModePlanning` ("planning")
- `Mode RunMode` field on the `Run` struct

---

## Acceptance Criteria

- `RunMode` type exists with two constants
- `Run` struct has `Mode` field with json/yaml tags
- Zero value (`""`) is treated as `RunModeStandard` for backward compatibility
- Existing tests compile and pass without modification
