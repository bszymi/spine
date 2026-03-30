---
id: TASK-001
type: Task
title: Add ArtifactWriter interface
status: Completed
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
---

# TASK-001 — Add ArtifactWriter Interface

---

## Purpose

Define the `ArtifactWriter` interface that `StartPlanningRun()` uses to create artifacts on branches. This decouples the engine from the artifact service implementation and enables testing with mocks.

---

## Deliverable

`internal/engine/interfaces.go`

Add:
```go
type ArtifactWriter interface {
    Create(ctx context.Context, path, content string) (*artifact.WriteResult, error)
}
```

---

## Acceptance Criteria

- Interface is defined in `interfaces.go` alongside existing engine interfaces
- Interface matches the signature of `artifact.Service.Create()`
- No implementation changes needed — `artifact.Service` already satisfies this interface
