---
id: TASK-002
type: Task
title: Add WithArtifactWriter to orchestrator
status: Pending
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
---

# TASK-002 — Add WithArtifactWriter to Orchestrator

---

## Purpose

Add the `ArtifactWriter` as an optional dependency on the orchestrator, following the existing `With*()` setter pattern.

---

## Deliverable

`internal/engine/orchestrator.go`

Add:
- `artifactWriter ArtifactWriter` field on the `Orchestrator` struct
- `func (o *Orchestrator) WithArtifactWriter(w ArtifactWriter)` setter method

---

## Acceptance Criteria

- Field and setter follow the pattern of existing `With*` methods (e.g., `WithAssignmentStore`)
- Orchestrator compiles and existing tests pass without providing an `ArtifactWriter`
- `StartPlanningRun()` will check `o.artifactWriter != nil` and return an error if not configured
