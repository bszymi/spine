---
id: TASK-005
type: Task
title: "Harden SSE per-connection timeout and replay bound"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-16
last_updated: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-005 — Harden SSE Per-Connection Timeout And Replay Bound

---

## Purpose

Two issues in `internal/gateway/handlers_events_stream.go`:

1. The global `WriteTimeout` set at `internal/gateway/server.go:265-268` is bypassed by SSE's long-lived writes. A half-closed/slow client can hold a goroutine indefinitely.
2. `ListEventsAfter` is called with a hardcoded 1000-event replay (`handlers_events_stream.go:66`). A crafted `Last-Event-ID` can force a thousand synchronous writes up-front, blocking the stream and amplifying DB load.

---

## Deliverable

- Add a per-connection inactivity timeout (e.g., cancel if no heartbeat or client ACK within 2× heartbeat interval).
- Cap replay to ~100 events per request; require the client to reconnect for older events.
- Log + metric when either kicks in.

---

## Acceptance Criteria

- Unit/integration test: a stalled client is disconnected within 2× heartbeat.
- Replay > 100 events returns only the 100 most recent post-cursor events.
- No regression in the happy-path SSE test.
