---
id: TASK-023
type: Task
title: "Misc polish: error paths, pagination floor, worktree TOCTOU, trace-id"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: chore
created: 2026-04-16
last_updated: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-023 — Misc Polish

---

## Purpose

Low-severity hygiene items from the 2026-04 audit, bundled because each is small:

1. **Error path leaks filesystem paths** — `internal/artifact/service.go:73-121` (`safePath` errors include the rejected path). Log the detail server-side; return a generic message to the client.
2. **No pagination floor** — `internal/gateway/handlers_helpers.go:33-45` allows `limit=1`, amplifying DB queries for large result sets. Enforce a minimum (e.g. 10).
3. **Worktree TOCTOU** — `internal/artifact/service.go:367-373` removes a temp dir and then calls `git worktree add`. Git refuses to follow symlinks here so the window is benign, but the pattern is fragile. Restructure to pass a known-safe parent dir instead.
4. **Trace-ID log hygiene** — `internal/gateway/middleware.go:164-176` validates the charset but relies on structured logging to avoid ANSI injection. Document the assumption; add a regression test that log output stays escaped when trace id contains escape-like bytes.

---

## Deliverable

- Four small patches in the files above.
- Unit test per item.

---

## Acceptance Criteria

- `safePath` error responses contain no filesystem detail.
- `limit=1` produces a minimum-of-10 page.
- Worktree setup no longer relies on remove-then-create in a shared temp dir.
- Trace-ID escape-byte test asserts log output does not contain raw escapes.
