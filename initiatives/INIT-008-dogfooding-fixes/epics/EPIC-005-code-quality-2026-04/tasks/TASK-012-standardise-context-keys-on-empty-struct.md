---
id: TASK-012
type: Task
title: "Standardise context keys on empty-struct types"
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

# TASK-012 — Empty-Struct Context Keys

---

## Purpose

Two context-key conventions coexist:

- `type contextKey string` + string constants — `internal/gateway/middleware.go` L19-25, `internal/observe/trace.go` L9-18.
- Empty-struct keys — `internal/githttp/handler.go` L271 (`repoPathKey{}`), `internal/artifact/write_context.go` L16, `internal/workflow/write_context.go` L19/L36.

Empty-struct keys are the idiomatic Go approach: collision-free across packages, zero-allocation, and they don't leak the key label through `%+v` on a context dump. The split is ad-hoc.

---

## Deliverable

1. Convert `internal/gateway/middleware.go` context keys to `type <name>Key struct{}` with exported/unexported visibility matching current surface.
2. Convert `internal/observe/trace.go` trace-id context key the same way.
3. Update all call sites (`context.WithValue` and `ctx.Value`) to pass the new key values; no logical change.

---

## Acceptance Criteria

- No `type contextKey string` or string-valued context key remains in the codebase.
- All tests pass unchanged.
