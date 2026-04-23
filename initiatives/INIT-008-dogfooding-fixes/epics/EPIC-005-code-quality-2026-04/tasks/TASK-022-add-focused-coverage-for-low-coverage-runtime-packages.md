---
id: TASK-022
type: Task
title: Add focused coverage for low-coverage runtime packages
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: test
created: 2026-04-24
last_updated: 2026-04-24
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-022 — Add focused coverage for low-coverage runtime packages

---

## Purpose

The review test run passed, but coverage remains thin around several packages that carry operational risk: projection, delivery, workspace, git, and store. Raising coverage here should target branchy behavior and failure paths, not broad line-count chasing.

## Deliverable

- Add unit tests around `internal/projection` workflow projection, branch-protection projection, and error paths.
- Add delivery tests for webhook target validation once TASK-026 lands, dispatcher failure classification, and TLS-config failures.
- Add workspace tests for service-pool initialization coordination once TASK-020 lands.
- Add git tests for shared ref validation once TASK-019 lands and push-auth edge cases not already covered.
- Add store tests for dynamic query builders and pagination/cursor behavior where feasible without requiring a live DB.

## Acceptance Criteria

- `go test -cover ./internal/...` shows meaningful coverage gains for `internal/projection`, `internal/delivery`, `internal/workspace`, and `internal/git`.
- New tests exercise at least one failure path per touched package.
- No test relies on external network access.
- Full `go test ./...` remains green.
