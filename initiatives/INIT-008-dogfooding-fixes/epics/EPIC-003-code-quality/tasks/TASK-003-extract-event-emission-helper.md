---
id: TASK-003
type: Task
title: "Extract event emission helper to reduce 12+ duplicated blocks"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: implementation
created: 2026-04-04
last_updated: 2026-04-04
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
---

# TASK-003 — Extract Event Emission Helper to Reduce 12+ Duplicated Blocks

---

## Purpose

The same 8-line event emission + warn-on-error pattern is repeated 12+ times across `engine/run.go`, `engine/step.go`, `engine/merge.go`, and `engine/retry.go`:

```go
if err := o.events.Emit(ctx, domain.Event{
    EventID:   fmt.Sprintf("evt-%s-<suffix>", run.TraceID[:12]),
    Type:      domain.Event<Type>,
    Timestamp: time.Now(),
    RunID:     runID,
    TraceID:   run.TraceID,
}); err != nil {
    log.Warn("failed to emit event", "event_type", domain.Event<Type>, "error", err)
}
```

---

## Deliverable

Add an `emitRunEvent` helper method on the Orchestrator:

```go
func (o *Orchestrator) emitRunEvent(ctx context.Context, traceID, runID, suffix string, eventType domain.EventType) {
    ...
}
```

Replace all 12+ call sites with one-line calls. For events with payloads (e.g., `run_failed` with reason), add an `emitRunEventWithPayload` variant.

---

## Acceptance Criteria

- All event emission call sites use the helper
- No direct `o.events.Emit` calls remain in engine code (except the helper itself)
- Events emitted are identical (same EventID format, fields, log behavior)
- All existing tests pass
