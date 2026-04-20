---
id: TASK-003
type: Task
title: "Push-option override (spine.override=true) with operator gate and governance event"
status: Completed
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
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-004-git-enforcement/tasks/TASK-002-pre-receive-enforcement.md
---

# TASK-003 — Push-option override (spine.override=true) with operator gate and governance event

---

## Purpose

Expose the Git-path equivalent of `write_context.override`. An operator pushing with `git push -o spine.override=true` can bypass a matching rule for that single push; the governance event is the authoritative audit record (ADR-009 §4).

---

## Context

[ADR-009 §4](/architecture/adr/ADR-009-branch-protection.md):

- Override is per-operation, signaled via Git push options.
- Only operator+ actors can effectively set it; contributors who try get a clean rejection, not silent acceptance.
- Governance event `branch_protection.override` is emitted on every honored override with the full payload, including `pre_receive_ref (old_sha, new_sha, ref)`.
- The Git path does **not** rewrite client-produced commits to add a trailer — the event is the sole record on this path.

---

## Deliverable

1. **Push-option parsing.** The pre-receive handler reads push options (the Git wire protocol surfaces them alongside the ref updates). Parse `spine.override=true`; unknown option keys are ignored.

2. **Propagation.** Set `branchprotect.Request.Override = true` on every `Request` for this push when the push option is present. The role gate in `branchprotect.Policy` (EPIC-002 TASK-002) handles the operator check; no additional logic here.

3. **Governance event.** On every honored override (per ref update, not per push — a single push can override multiple rules), emit `branch_protection.override` with payload:
   ```
   {
     actor_id, branch, rule_kinds, operation,
     trace_id, run_id: null, commit_sha: <new_sha or null for deletion>,
     pre_receive_ref: { old_sha, new_sha, ref }
   }
   ```

4. **No trailer rewriting.** Explicit test: pushed commits are byte-identical on the server to what the client sent. No pre-receive hook rewrites them.

5. **Tests.**
   - Operator push with `-o spine.override=true` to a `no-direct-write` branch → allowed, one event per overridden ref update, commits unmodified.
   - Contributor push with the same option → rejected with the "no override authority" reason.
   - Push with `-o spine.override=true` that does not need override (unprotected branch) → allowed, no event emitted.
   - Mixed push (one ref needs override, one does not) → both allowed if actor is operator, event emitted only for the protected ref.

---

## Acceptance Criteria

- `git push -o spine.override=true` is the documented Git-path override mechanism; no alternative signaling (flag in ref name, commit trailer, etc.) is honored.
- Only operator+ actors can effectively use it.
- Governance event is emitted exactly once per overridden ref update with the full ADR-009 §4 payload.
- The Git server never mutates client-produced commits.
- Unused override (flag set on a push that did not need it) does not emit events — avoids log pollution.
