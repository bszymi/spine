---
id: TASK-014
type: Task
title: "Replace direct slog.* calls with observe.Logger(ctx)"
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

# TASK-014 — Route All Logging Through observe.Logger

---

## Purpose

The codebase standardises on `observe.Logger(ctx).Info/Warn/Error(...)` (~102 sites) which propagates the trace ID through structured fields. A handful of sites bypass it with direct `slog.*`:

- `internal/gateway/server.go` L295
- `internal/delivery/circuit_breaker.go` L76, L99, L124, L132

These lines lose the trace-ID plumbing the rest of the codebase enforces, making their output harder to correlate in logs.

---

## Deliverable

1. Replace each direct `slog.Info/Warn/Error` call with `observe.Logger(ctx)`.
2. Where a `ctx` is not in scope, thread it through (circuit-breaker functions already take ctx). For the single `gateway/server.go` site, synthesize a short-lived context or accept one at the call site.
3. Confirm the log output format is unchanged apart from the added trace fields.

---

## Acceptance Criteria

- No direct `slog.Info|Warn|Error|Debug` calls remain outside the observe package itself.
- Tests pass unchanged.
