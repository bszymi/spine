---
id: TASK-008
type: Task
title: "Prefer credential helper over env var for git push token"
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

# TASK-008 — Prefer Credential Helper Over Env Var For Git Push Token

---

## Purpose

`SPINE_GIT_PUSH_TOKEN` (`cmd/spine/main.go:317-319`, `internal/git/cli.go:44-52`) is read into memory and passed to git via `GIT_ASKPASS`. The token sits in `os.Environ()` for the lifetime of the process — visible to child processes, core dumps, and `/proc/<pid>/environ` on Linux. The existing `GIT_ASKPASS` mechanism is good; the env var itself is the weak link.

---

## Deliverable

- When `SPINE_GIT_CREDENTIAL_HELPER` is configured, ignore `SPINE_GIT_PUSH_TOKEN` entirely and emit a warning if both are set.
- When only the env var is set, unset it from the process env immediately after copying into the `GIT_ASKPASS` temp file.
- Document the credential-helper path as the recommended production mode.

---

## Acceptance Criteria

- With helper configured, `os.Environ()` does not contain the token after startup.
- Existing push tests still pass under both modes.
- README/docs updated to reflect the recommended mode.
