---
id: TASK-021
type: Task
title: "Unit tests for auth.Authorize suspended-actor path"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-021 — Unit tests for auth.Authorize suspended-actor path

---

## Purpose

`internal/auth/permissions.go:115`'s `Authorize` is exercised by
`auth_test.go:256 TestAuthorizeAllOperations`, but the
suspended/Deactivated-actor + valid-token corner is only tested for
`ValidateToken`, not for `Authorize` directly. Suspended actors with
valid tokens hitting `Authorize` would be a denial-bypass regression.

This is a P2 coverage finding from the 2026-05-07 code review.

## Deliverable

Extend `internal/auth/auth_test.go` (or add a focused
`permissions_test.go`) with cases:

- Suspended actor with valid token + valid capability → `Authorize`
  rejects with the expected error class.
- Deactivated actor with valid token → `Authorize` rejects.
- Active actor with valid token but wrong capability → existing
  rejection (regression bait).
- Active actor, valid token, valid capability → success.
- Each role (Operator / Reviewer / Observer / etc.) exercised against
  at least one capability that should and should not be granted.

## Acceptance Criteria

- New tests pass without the `integration` tag.
- Removing the actor-status check in `Authorize` causes the suspended /
  deactivated cases to fail.

## Out of Scope

- Auditing every gateway authz path — `gateway/middleware_test.go`
  covers that surface.
