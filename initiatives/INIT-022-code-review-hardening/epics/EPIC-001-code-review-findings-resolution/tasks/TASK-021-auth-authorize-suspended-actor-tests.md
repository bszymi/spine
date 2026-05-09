---
id: TASK-021
type: Task
title: "Unit tests for auth.Authorize suspended-actor path"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-09
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

## Resolution (2026-05-09)

**Files**

- `internal/auth/permissions.go` (MODIFIED) — added a defence-in-depth
  active-status gate at the top of `Authorize`. Pre-existing prod flow
  (`authMiddleware → ValidateToken`) already rejects non-active actors
  with `ErrUnauthorized`; the gate makes `Authorize` self-defending so
  any future direct caller — or a regression in the token path —
  cannot bypass account suspension.
- `internal/auth/permissions_test.go` (NEW) — focused tests for the
  status gate and role gate.
- `internal/auth/auth_test.go` (MODIFIED) — `TestAuthorizeAllOperations`
  and `TestAuthorizeUnknownOperation` now set `Status: ActorStatusActive`
  on their fixtures (the new gate would otherwise reject them).

**Why hardening + tests, not just tests**

The AC pins: "Removing the actor-status check in `Authorize` causes the
suspended / deactivated cases to fail." Today `Authorize` had no such
check — only `ValidateToken` at `auth.go:43` did. The AC is only
satisfiable by adding the check. This is defence-in-depth: each layer
enforces its own invariant, so a future code path that constructs an
`*Actor` outside the bearer flow cannot let suspended actors slip
through. Error class is `ErrForbidden` (matches existing `Authorize`
semantics — caller is identified, but their account is not operational)
rather than `ErrUnauthorized` (no/invalid identity).

**Test shape**

- `TestAuthorize_RejectsSuspendedActor` — Admin (would pass any role
  gate) + Suspended → `ErrForbidden`. The AC's primary regression bait.
- `TestAuthorize_RejectsDeactivatedActor` — same shape for the terminal
  lifecycle state. Deactivated is one-way (actor.go updateStatus
  rejects reactivation), so a successful Authorize would re-arm a
  tombstoned account.
- `TestAuthorize_RejectsZeroStatus` — pins that the zero-value Status
  string ("") is treated as non-active. Defence against a caller that
  builds an Actor without populating Status.
- `TestAuthorize_StatusCheckPrecedesRoleCheck` — Reader + Suspended
  hitting an Admin-only op asserts the error detail carries
  `actor_status` and **not** `required_role`. A regression that swaps
  the checks would mask suspension behind a role error and is caught
  here.
- `TestAuthorize_StatusCheckPrecedesUnknownOp` — Suspended + unknown
  op rejects on status, not "unknown operation". Without this an
  attacker with a suspended valid token could enumerate the
  operationRoles registry.
- `TestAuthorize_ActiveActorWrongCapability` — AC's role-gate
  regression bait. Active Reader + `artifact.create` → `ErrForbidden`
  with `required_role=contributor` in detail.
- `TestAuthorize_ActiveActorRightCapability` — happy path; catches an
  inverted `HasAtLeast` regression.
- `TestAuthorize_RoleMatrix` — every defined `ActorRole` (Reader /
  Contributor / Reviewer / Operator / Admin) against one capability
  it should grant and one it should deny (Admin has no deny case).
  The AC's "each role exercised against at least one grant and deny"
  matrix.

**AC verification**

Removed the new status gate from `permissions.go` and re-ran
`go test ./internal/auth/... -run 'TestAuthorize_'`: 5 tests failed
(`RejectsSuspendedActor`, `RejectsDeactivatedActor`, `RejectsZeroStatus`,
`StatusCheckPrecedesRoleCheck`, `StatusCheckPrecedesUnknownOp`).
Restored.

**Test gates**

- `go test ./internal/auth/... -count=1 -race` — green.
- `go test ./internal/gateway/... -count=1 -race` — green
  (middleware → Authorize unaffected; gateway's own actor fixtures
  already set Status: Active).
- `go test ./...` — green except the pre-existing
  `internal/secrets/file_test.go::TestFileClient_VersionChangesOnEdit`
  flake (TASK-026 territory).
- `go test -tags=scenario ./internal/scenariotest/scenarios/...` —
  green (TestAIActor_SamePermissionsAsHuman, TestGovernancePermission*
  etc. all set Status: Active).
- `gofmt -l` on touched files — clean.
- `go vet ./internal/auth/... ./internal/gateway/...` — clean.
- `make docker-lint` — 206 baseline unchanged; no new findings on the
  added/modified files.
- `codex review --uncommitted` — clean: "I did not find any discrete
  regressions in the modified or added code."
- Coverage `internal/auth`: 89.4% (no gate threshold for this task,
  but reported for context).
