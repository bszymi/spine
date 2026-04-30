---
id: EPIC-005
type: Epic
title: "Merge Outcomes and Recovery"
status: Completed
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
owner: bszymi
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
---

# EPIC-005 - Merge Outcomes and Recovery

---

## Purpose

Merge affected repositories independently, record the result in the primary Spine repo, and make partial merge states recoverable.

Spine must not pretend cross-repo merges are atomic. It should instead make partial outcomes explicit, auditable, and restartable.

---

## Scope

### In Scope

- Per-repository merge outcome model
- Code-repo-first, primary-repo-last merge ordering
- Partial merge run state
- Retry and manual recovery behavior
- Branch cleanup per repository

### Out of Scope

- Distributed transactions
- Automatic semantic conflict resolution
- Rollback of already-merged code repositories

---

## Primary Outputs

- Runtime schema for per-repo outcomes
- Run status/state-machine updates for partial merge
- Primary-repo ledger updates for final outcomes
- Recovery and scheduler tests

---

## Acceptance Criteria

1. Each affected repository records its merge outcome independently.
2. Successful code repo merges are not rolled back if another repo fails.
3. The primary repo records merged, failed, skipped, and pending outcomes.
4. Failed repo branches are preserved for manual resolution.
5. Completed runs require all affected repos to merge successfully.

