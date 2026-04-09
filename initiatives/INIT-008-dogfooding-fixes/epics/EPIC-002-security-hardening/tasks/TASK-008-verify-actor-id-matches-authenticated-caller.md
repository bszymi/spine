---
id: TASK-008
type: Task
title: "Verify actor_id matches authenticated caller in claim/release"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
---

# TASK-008 — Verify actor_id Matches Authenticated Caller in Claim/Release

---

## Purpose

`handleExecutionClaim` in `/internal/gateway/handlers_claim.go` (lines 23-36) and `handleExecutionRelease` in `/internal/gateway/handlers_release.go` (lines 23-40) accept arbitrary `actor_id` in the request body without verifying it matches the authenticated caller. Any authenticated actor can impersonate another actor ID when claiming or releasing steps.

---

## Deliverable

Add validation that `req.ActorID` matches the authenticated actor from the request context.

---

## Acceptance Criteria

- Claim/release reject requests where body actor_id differs from authenticated actor
- Returns 403 Forbidden on mismatch
- Existing tests pass
