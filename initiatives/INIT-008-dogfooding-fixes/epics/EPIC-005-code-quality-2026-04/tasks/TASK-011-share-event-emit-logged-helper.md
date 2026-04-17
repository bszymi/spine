---
id: TASK-011
type: Task
title: "Share event-emit helper across packages (event.EmitLogged)"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-011 — Share EmitLogged Helper

---

## Purpose

~10 sites build a `domain.Event{EventID: ..., Type: ..., Timestamp: time.Now(), TraceID: observe.TraceID(ctx), Payload: ...}`, call `Emit`, and on error log a warning:

- `internal/engine/emit.go` L14-25 (`emitEvent`, already the desired shape, private to engine)
- `internal/artifact/service.go` L577-608
- `internal/gateway/handlers_discussions.go` L454-474 (local reimplementation as `emitDiscussionEvent`)
- `internal/scheduler/recovery.go` L58-65
- `internal/scheduler/run_timeout.go` L64
- `internal/scheduler/timeout.go` L104
- `internal/divergence/service.go` L85
- `internal/divergence/convergence.go` L208
- `internal/projection/service.go` L307
- `internal/actor/gateway.go` L62, L121

---

## Deliverable

1. Add `internal/event/emit.go` exporting `EmitLogged(ctx context.Context, router Router, ev domain.Event)` that:
   - Fills `Timestamp` with `time.Now().UTC()` if zero.
   - Fills `TraceID` from `observe.TraceID(ctx)` if empty.
   - Calls `router.Emit` and logs a warning on error via `observe.Logger(ctx)`.
2. Replace all identified sites with `event.EmitLogged(ctx, router, ev)`.
3. Remove the private `emitEvent` from engine and the local `emitDiscussionEvent` from the gateway.

---

## Acceptance Criteria

- All sites listed above use `event.EmitLogged`.
- No package retains a private fire-and-forget emit wrapper.
- Existing tests pass unchanged.
