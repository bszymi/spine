---
id: TASK-002
type: Task
title: "Remove implicit allow-all in authorize when no actor in context"
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

# TASK-002 — Remove Implicit Allow-All in Authorize When No Actor in Context

---

## Purpose

The `authorize` helper in `/internal/gateway/middleware.go` (lines 135-146) silently allows all operations when no actor is in the request context:

```go
actor := actorFromContext(r.Context())
if actor == nil {
    // No auth middleware configured — allow in dev/test mode
    return true
}
```

While the `authMiddleware` itself fails closed (returns 401 when `s.auth == nil`), this fallback means if a route group accidentally omits the auth middleware, all authorization checks pass silently. There is no explicit dev/test mode check — this is an implicit allow-all.

---

## Deliverable

Make the dev/test bypass explicit:

1. Add a `DevMode bool` field to `ServerConfig`
2. Only allow the no-actor fallback when `DevMode` is true
3. When `DevMode` is false and actor is nil, return 401 (same as auth middleware)
4. Log a warning when the dev-mode fallback is used

---

## Acceptance Criteria

- `authorize()` returns false (401) when no actor and not in dev mode
- `DevMode: true` preserves current behavior for development
- Existing tests that rely on the fallback set `DevMode: true`
- No route group can accidentally bypass authorization in production
