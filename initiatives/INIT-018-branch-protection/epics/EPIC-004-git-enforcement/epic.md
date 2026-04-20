---
id: EPIC-004
type: Epic
title: "Branch Protection — Git-Path Enforcement"
status: Completed
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
---

# EPIC-004 — Branch Protection — Git-Path Enforcement

---

## Purpose

Enable `git-receive-pack` in `internal/githttp` and enforce branch protection on every push. After this epic, `git push origin :main` (delete) and `git push origin local:main` (direct write) against a Spine-hosted repo are rejected by a pre-receive check that consults the same `branchprotect.Policy` the API path uses.

Push has been read-only until now (per ADR-009 §3 context). This epic both turns it on and makes it safe to turn on.

---

## Key Work Areas

- Enable `git-receive-pack` in `internal/githttp` behind a config flag (default off for existing deployments; on for new ones).
- Pre-receive enforcement via `branchprotect.Policy`.
- Push-option-based override: `git push -o spine.override=true`, operator-only, governance event emitted.
- Scenario tests exercising real pushes; docs sweep for the Git Integration Contract.

---

## Primary Outputs

- `internal/githttp` serving `receive-pack` behind a config flag.
- Pre-receive handler that calls `branchprotect.Policy.Evaluate` for every ref update in the push.
- Push-option parser for `spine.override`, wired through the same operator gate as the API path.
- `branch_protection.override` governance events on the Git path with full payload.
- Updated Git Integration Contract describing the enforced push surface.

---

## Acceptance Criteria

- `git-receive-pack` is reachable over HTTPS behind a documented config flag.
- A push that deletes a `no-delete`-protected ref is rejected with a `pre-receive hook declined` error that names the rule.
- A push that advances a `no-direct-write`-protected branch is rejected unless the pusher is `operator+` and sent `-o spine.override=true`.
- Every honored override emits a `branch_protection.override` governance event with the ADR-009 §4 payload, including `pre_receive_ref (old_sha, new_sha, ref)`.
- The Git push path does not rewrite client-produced commits to add a trailer (ADR-009 §4).
- Scenario tests exercise real pushes via a real Git client against a running `internal/githttp` instance.
- Git Integration Contract is updated to describe the enabled, enforced push surface.
