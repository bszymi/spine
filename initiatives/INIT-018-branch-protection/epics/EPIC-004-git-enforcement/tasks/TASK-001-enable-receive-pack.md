---
id: TASK-001
type: Task
title: "Enable git-receive-pack in internal/githttp behind config flag"
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
---

# TASK-001 — Enable git-receive-pack in internal/githttp behind config flag

---

## Purpose

Turn on the Git push endpoint that `internal/githttp` currently blocks. This is the precondition for any Git-path enforcement — EPIC-004 TASKs 002 and 003 presuppose that push is reachable.

The flag is explicit: upgrading an existing deployment must not silently start accepting pushes.

---

## Context

Today `internal/githttp` serves `info/refs?service=git-upload-pack` and `git-upload-pack` (clone/fetch) but rejects `git-receive-pack` (push). [ADR-009](/architecture/adr/ADR-009-branch-protection.md) describes this as "enabling push is on the roadmap."

This task does not add any protection logic — `receive-pack` is enabled as a plain passthrough. TASK-002 wraps it with the pre-receive check.

---

## Deliverable

1. **Config surface.** Add a field (e.g. `git.receive_pack_enabled: bool`, default `false`) to the Spine server config. Document it in whatever config-reference doc the repo uses.

2. **Handler.** Wire the existing Git backend to serve `info/refs?service=git-receive-pack` and `git-receive-pack` when the flag is on. Deny with a clear error when the flag is off (the current behavior, reworded to reference the flag).

3. **Authentication / authorization.** Every push request must carry the same actor identity the rest of Spine uses. Reject unauthenticated pushes. Role-based authorization is out of scope for this task — a reader could still push here; the policy layer in TASK-002 stops the bad pushes.

4. **Tests.**
   - Flag off: `git push` returns the documented error.
   - Flag on: `git push` to an unprotected branch in a test workspace succeeds and advances the ref.
   - Flag on, unauthenticated: rejected before reaching the backend.

---

## Acceptance Criteria

- `git-receive-pack` is reachable over HTTPS when the config flag is on, and rejected when off.
- Existing deployments (flag default `false`) see no behavior change.
- Authentication is enforced; policy-layer enforcement is handled in TASK-002, not here.
- No protection logic is added in this task — a push to `main` with the flag on and no policy wired would succeed. TASK-002 fills the gap before any real rollout.
