---
id: TASK-002
type: Task
title: "Externalize database credentials and enable SSL"
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

# TASK-002 — Externalize Database Credentials And Enable SSL

---

## Purpose

`docker-compose.yaml:9,39-41,72-73,90` and `docker-compose.test.yaml:9,23` embed default credentials (`spine:spine`, `spine_registry:spine_registry`). All connection strings use `sslmode=disable` (`docker-compose.yaml:9,57,90`), transmitting credentials and data in plaintext. `cmd/spine/main.go:280-281` only warns about `sslmode=disable` and proceeds.

---

## Deliverable

- Move credentials into a gitignored `.env` (with `.env.example` committed as template).
- Switch default `sslmode` to `require` in all non-test compose services; document a `SPINE_INSECURE_LOCAL=1` escape hatch if needed for devs.
- In `cmd/spine/main.go`, promote the `sslmode=disable` warning to a fatal startup error unless `SPINE_INSECURE_LOCAL=1`.

---

## Acceptance Criteria

- No default credentials are present in any committed compose file.
- `spine serve` refuses to start against a `sslmode=disable` URL without the explicit opt-in env var.
- `.env.example` documents all required variables.
