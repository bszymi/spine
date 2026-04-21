---
id: TASK-017
type: Task
title: Purge orphan execution_projections on rebuild and skip templates/ in discovery
status: Completed
created: 2026-04-21
last_updated: 2026-04-21
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-017 — Purge orphan execution_projections on rebuild and skip templates/ in discovery

---

## Purpose

Found during the 2026-04-21 SMP dogfooding session: `GET /api/v1/execution/tasks/ready` returned 15 rows pointing at paths that no longer exist on disk. Two distinct bugs cause this:

1. **`DeleteAllProjections` leaves `projection.execution_projections` intact.**
   `internal/store/postgres_projections.go:135` wipes `artifact_links`, `artifacts`, and `workflows`, then the rebuild repopulates them from the current HEAD. `execution_projections` is never cleared, so rows created for artifacts that have since been renamed or removed (e.g. `INIT-002-end-to-end-scenario-testing/…` paths moved to `INIT-004-…`, or an `EPIC-050` directory renamed to `epic-050`) remain forever. A full rebuild is the documented escape hatch for inconsistent projection state, but it doesn't work for this table.

2. **`artifact.DiscoverAll` indexes `templates/*.md` as real artifacts.**
   `internal/artifact/discovery.go:33` walks every `.md` file and admits anything whose frontmatter declares a valid `type`. The repo-level `templates/task-template.md` has `type: Task` by design (it's a shape example users copy) and is therefore projected as a real Task. The downstream `execution_projections` row for the template shows up in every "ready tasks" query until someone manually deletes it.

## Deliverable

### Part A — Include execution_projections in the rebuild wipe

- Extend `PostgresStore.DeleteAllProjections` (`internal/store/postgres_projections.go`) to also `DELETE FROM projection.execution_projections`.
- Confirm nothing depends on `execution_projections` rows surviving a rebuild (run_id/workflow_step linkage is populated from the same sync path that upserts the row; a rebuild re-populates it for live tasks).
- Add a projection rebuild test covering the orphan case: insert an `execution_projections` row for a path that doesn't exist in the repo, run a rebuild, assert the row is gone.

### Part B — Exclude `templates/` from artifact discovery

- In `DiscoverAll` (`internal/artifact/discovery.go`), skip any path under a top-level `templates/` directory before checking `IsArtifact`. The repo convention is that `templates/` holds shape-reference files, not governed artifacts.
- Document the rule alongside `IsArtifact` / `IsWorkflowPath` so the exclusion is discoverable.
- Add a discovery test with a `templates/task-template.md` fixture asserting it lands in `Skipped`, not `Artifacts`.

## Acceptance Criteria

- `SELECT COUNT(*) FROM projection.execution_projections e LEFT JOIN projection.artifacts a ON a.artifact_path = e.task_path WHERE a.artifact_path IS NULL` returns 0 after a full rebuild, even if orphan rows were present beforehand.
- `templates/task-template.md` is no longer returned by `/api/v1/execution/tasks/ready` on any workspace with the default templates seeded.
- Both behaviours covered by unit/scenario tests so regressions fail loudly.

## Out of Scope

- Incremental purge on artifact delete/rename (this task relies on a full rebuild to sweep orphans; an event-driven delete would be a separate improvement).
- Any SMP-side change — the relevant SMP task list was already cleaned up manually on 2026-04-21.

## Context

Found by:
- Querying `projection.execution_projections` directly on the dev Spine DB
  and joining against `projection.artifacts` — 15 rows had no matching
  artifact.
- Confirming via logs that a full rebuild logs "artifacts/workflows" counts
  but leaves `execution_projections` untouched.

Related: `TASK-003-populate-execution-projections.md` (INIT-010/EPIC-004) wired population but not teardown on disappearance.
