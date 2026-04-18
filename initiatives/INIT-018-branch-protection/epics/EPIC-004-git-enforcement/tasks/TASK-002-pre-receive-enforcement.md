---
id: TASK-002
type: Task
title: "Pre-receive enforcement via branchprotect.Policy"
status: Pending
work_type: implementation
created: 2026-04-18
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-004-git-enforcement/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-004-git-enforcement/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-004-git-enforcement/tasks/TASK-001-enable-receive-pack.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
---

# TASK-002 — Pre-receive enforcement via branchprotect.Policy

---

## Purpose

Wrap `git-receive-pack` with a pre-receive check that consults `branchprotect.Policy.Evaluate` for every ref update in the push. This is the enforcement half of EPIC-004 — TASK-001 turned push on, this task makes it safe.

---

## Context

[ADR-009 §3](/architecture/adr/ADR-009-branch-protection.md):

> A pre-receive check wraps `git-receive-pack` (once push is enabled). For each ref update in the push, the handler constructs a `Request` (delete vs advance inferred from the old/new SHA) and consults the policy. A `Deny` translates to a `pre-receive hook declined` error over the Git wire protocol.

Classification rule: `new_sha == 0000000...` → `Kind = Delete`; otherwise `Kind = DirectWrite`. Every Git push is a direct write from the policy's perspective — a governed merge happens inside Spine, not over the wire.

---

## Deliverable

1. **Pre-receive hook handler.** A new handler in `internal/githttp` that runs before `git-receive-pack` writes refs. Implementation can be a server-side pre-receive hook installed per workspace, or a wrapper process that parses the push command stream and calls the Go package directly — pick whichever fits Spine's Git-serving architecture (document the choice in the task PR).

2. **Request construction.** For each `(old_sha, new_sha, ref)` triple in the push:
   - Skip `spine/*` refs (policy is a no-op there — no user-authored rules target them — but we still pass them through so audit remains consistent with the API path).
   - Construct `branchprotect.Request` with `Branch = ref`, `Kind = Delete | DirectWrite`, `Actor` from the authenticated push session, `Override` from push options (TASK-003), `TraceID` fresh for this push.
   - Call `Policy.Evaluate`. `Deny` on any ref update rejects the entire push (Git pre-receive semantics).

3. **Error messages.** The rejection surface over the Git wire protocol names the rule and the branch. Keep the message under 72 chars so standard Git clients render it cleanly.

4. **Tests.**
   - Push that advances a `no-direct-write` branch → rejected, ref unchanged on server.
   - Push that deletes a `no-delete` branch → rejected, ref unchanged on server.
   - Push to an unprotected branch → succeeds.
   - Push that mixes an allowed and a denied ref update → entire push rejected (no partial application).

---

## Acceptance Criteria

- Every ref update in every push passes through `Policy.Evaluate`; no push-level bypass exists.
- Rejected pushes never mutate the server-side ref (pre-receive semantics, verified in a test).
- Rejection messages identify the rule kind (`no-delete` / `no-direct-write`) and the branch.
- `spine/*` refs evaluate as allowed and do not produce user-visible audit events.
- Policy evaluation is served from the projected runtime table (EPIC-002 TASK-003) — no Git reads in the hot path.
