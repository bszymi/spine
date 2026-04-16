---
id: TASK-020
type: Task
title: "Refuse to start with dev-mode auth in production"
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

# TASK-020 — Refuse To Start With Dev-Mode Auth In Production

---

## Purpose

`internal/gateway/server.go:258-260` and `internal/gateway/middleware.go:140-148` allow an explicit dev-mode bypass that skips `authMiddleware` and short-circuits `authorize()` to `true`. Useful locally, dangerous if ever enabled in production. A `slog.Warn` at startup is the only guard.

---

## Deliverable

- Introduce a `SPINE_ENV` variable with values `production` / `staging` / `development`.
- When `SPINE_ENV=production`, fail startup if dev-mode auth is enabled.
- When `SPINE_ENV` is unset and `devMode=true`, log a loud warning but allow startup.
- Surface the effective env in `/system/status`.

---

## Acceptance Criteria

- `SPINE_ENV=production` + dev-mode → startup error.
- `SPINE_ENV=development` + dev-mode → warning + continue.
- Unit test covers both paths.
