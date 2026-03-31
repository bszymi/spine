---
id: EPIC-002
type: Epic
title: Human-Readable Branch Names
status: Pending
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
owner: bszymi
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/initiative.md
---

# EPIC-002 — Human-Readable Branch Names

---

## 1. Purpose

Replace opaque branch names (`spine/run/run-0a5d0f6d`) with human-readable names that identify the artifact being worked on.

When a team sees a branch list, they should immediately know what each branch is for:
- `spine/plan/INIT-001-build-spine-management-platform` — planning run for initiative creation
- `spine/run/TASK-003-implement-start-planning-run` — standard run for task execution

---

## 2. Scope

### In Scope

- Branch naming convention: `spine/<mode>/<artifact-id>-<slug>`
  - Planning runs: `spine/plan/<artifact-id>-<slug>`
  - Standard runs: `spine/run/<artifact-id>-<slug>`
- Extract artifact ID and slug from the artifact path or content
- Handle name collisions (append short run ID suffix if branch already exists)
- Sanitize names for Git ref validity (no spaces, special chars)
- Update `StartRun()` and `StartPlanningRun()` branch name generation
- Update `CleanupRunBranch()` and `MergeRunBranch()` (branch name stored on run, no logic change needed)
- Update existing tests
- Update architecture docs (git-integration.md branch naming section)

### Out of Scope

- Changing the run ID format (stays as `run-XXXXXXXX`)
- Branch naming for divergence branches

---

## 3. Success Criteria

1. Planning run branches use `spine/plan/<artifact-id>-<slug>`
2. Standard run branches use `spine/run/<artifact-id>-<slug>`
3. Collision handling: if branch exists, append `-<short-run-id>` (e.g., `spine/plan/INIT-001-slug-0a5d0f6d`)
4. Branch names are valid Git refs
5. Existing run queries by branch name still work (branch name stored on run record)
6. All tests pass

---

## 4. Key Files

- `internal/engine/run.go` — branch name generation in `StartRun()` and `StartPlanningRun()`
- `architecture/git-integration.md` — update branch naming documentation

---

## 5. Related Artifacts

- `/architecture/git-integration.md`
