---
id: TASK-003
type: Task
title: "Add HTTP server timeouts to prevent slowloris"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-04
last_updated: 2026-04-04
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
---

# TASK-003 — Add HTTP Server Timeouts to Prevent Slowloris

---

## Purpose

The `http.Server` in `/internal/gateway/server.go` (lines 187-190) is created without `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, or `IdleTimeout`. This makes the server vulnerable to slowloris attacks where an attacker opens connections and sends data very slowly, exhausting connection limits.

---

## Deliverable

Add sensible timeout defaults to the HTTP server:

```go
s.httpServer = &http.Server{
    Addr:              addr,
    Handler:           s.routes(),
    ReadHeaderTimeout: 10 * time.Second,
    ReadTimeout:       30 * time.Second,
    WriteTimeout:      60 * time.Second,
    IdleTimeout:       120 * time.Second,
}
```

Make timeouts configurable via `ServerConfig` with these defaults.

---

## Acceptance Criteria

- HTTP server has non-zero `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`
- Timeouts are configurable via `ServerConfig`
- Existing tests pass (no test relies on infinite timeouts)
