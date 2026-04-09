---
id: TASK-012
type: Task
title: "Fix trace ID slicing panic on short X-Trace-Id headers"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
---

# TASK-012 — Fix Trace ID Slicing Panic on Short X-Trace-Id Headers

---

## Purpose

`/internal/gateway/handlers_system.go` (line 99) does `observe.TraceID(r.Context())[:8]` which panics if a client sends a short `X-Trace-Id` header (e.g., `"ab"`). The traceIDMiddleware accepts arbitrary client values without length validation. This allows a client to crash the server with a crafted request.

---

## Deliverable

Guard all `TraceID` slicing operations with length checks. This may be resolved as part of TASK-010 (trace ID validation in middleware), but all slicing call sites should be independently safe.

---

## Acceptance Criteria

- Short trace IDs do not cause panics anywhere in the codebase
- Existing tests pass
