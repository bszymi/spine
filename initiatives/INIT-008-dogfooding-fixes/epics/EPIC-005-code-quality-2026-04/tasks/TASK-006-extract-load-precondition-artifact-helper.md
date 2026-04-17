---
id: TASK-006
type: Task
title: "Extract loadPreconditionArtifact helper in engine/step.go"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
completed: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-006 — Extract loadPreconditionArtifact

---

## Purpose

`internal/engine/step.go` L693-757 defines four precondition checks — `checkArtifactStatus`, `checkFieldPresent`, `checkFieldValue`, `checkLinksExist` — and each opens with the same six-line preamble:

```go
path := config["path"]
if path == "" { path = run.TaskPath }
art, err := o.artifacts.Read(ctx, path, resolveReadRef(run))
if err != nil { return false }
```

---

## Deliverable

1. Extract `func (o *Orchestrator) loadPreconditionArtifact(ctx, config map[string]string, run *Run) (*domain.Artifact, bool)` returning `(artifact, ok)` where `ok=false` means "skip check / treat as failure" with the current semantics.
2. Collapse each of the four checks to its unique predicate.

---

## Acceptance Criteria

- Each check function is under 15 lines and contains only its own predicate.
- Precondition behaviour is unchanged (verify via existing engine tests).
- No change to the preconditions API surface.
