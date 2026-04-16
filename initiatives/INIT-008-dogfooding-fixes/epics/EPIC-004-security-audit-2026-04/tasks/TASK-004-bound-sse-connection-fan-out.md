---
id: TASK-004
type: Task
title: "Bound SSE connection fan-out per actor"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-16
last_updated: 2026-04-16
completed: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-004 — Bound SSE Connection Fan-Out Per Actor

---

## Purpose

`internal/gateway/handlers_events_stream.go:78` allocates a per-connection channel (`make(chan domain.Event, 100)`) with no per-actor or per-IP connection cap. `rateLimitMiddleware` governs request initiation, not long-lived streams. A single authenticated actor can hold thousands of concurrent connections, exhausting memory and goroutines.

---

## Deliverable

- Track in-flight SSE subscriptions per actor ID in a concurrent map.
- Reject new subscriptions (429) past a configurable cap (default 5).
- Emit a metric for rejections.
- Release the slot in the existing defer/cleanup path when the handler returns.

---

## Acceptance Criteria

- Integration test: opening a 6th concurrent stream for the same actor returns 429.
- Closing a stream frees the slot (verified by a follow-up successful connect).
- Cap is tunable via `SPINE_SSE_MAX_CONN_PER_ACTOR`.
