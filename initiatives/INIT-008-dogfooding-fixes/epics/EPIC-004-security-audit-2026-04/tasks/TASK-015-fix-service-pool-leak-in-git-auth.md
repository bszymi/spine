---
id: TASK-015
type: Task
title: "Fix service-pool reference leak in git auth handler"
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

# TASK-015 — Fix Service-Pool Reference Leak In Git Auth Handler

---

## Purpose

`internal/gateway/handlers_git.go:148-154` (`validateGitAuth`) acquires a workspace-scoped service from `s.servicePool.Get`. The current `defer Release` is placed inside a conditional branch, so if `ValidateToken` fails the reference is never released. Repeated auth failures exhaust the pool over time.

---

## Deliverable

- Move `defer s.servicePool.Release(cfg.ID)` so it runs on every success path from `Get`, regardless of downstream validation outcome.
- Add a regression test that repeatedly fails validation and confirms pool size stays bounded.

---

## Acceptance Criteria

- 1000 consecutive failed auths do not grow pool references.
- Happy-path auth unaffected.
