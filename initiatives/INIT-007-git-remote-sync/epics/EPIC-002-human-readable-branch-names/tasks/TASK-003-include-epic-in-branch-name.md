---
id: TASK-003
type: Task
title: "Include epic context in task branch names"
status: Pending
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-002-human-readable-branch-names/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
work_type: implementation
created: 2026-04-04
last_updated: 2026-04-04
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-002-human-readable-branch-names/epic.md
---

# TASK-003 — Include Epic Context in Task Branch Names

---

## Purpose

`slugFromPath` only extracts the filename, producing branches like `spine/run/task-001-task-001-credential-schema-and-storage-4390cc54` — the artifact ID and filename both start with the task ID, causing duplication and losing epic context. Branch names should include the parent epic for clarity, e.g. `spine/run/epic-009-task-001-credential-schema-and-storage-4390cc54`.

---

## Deliverable

Update `slugFromPath` in `internal/engine/branchname.go` to extract the parent epic directory name when the artifact is inside an `epics/*/tasks/` path, and prepend it to the slug. Also deduplicate the artifact ID from the slug when the slug already starts with the ID.

Expected results:
- `epics/EPIC-009/tasks/TASK-001-credential-schema.md` with ID `TASK-001` → `epic-009-task-001-credential-schema`
- `initiatives/INIT-001/initiative.md` with ID `INIT-001` → `initiative` (unchanged)
- `epics/EPIC-001/epic.md` with ID `EPIC-001` → `epic` (unchanged)

Update tests in `branchname_test.go` accordingly.

---

## Acceptance Criteria

- Task branches include the epic slug: `spine/run/epic-XXX-task-XXX-slug-<suffix>`
- No duplication when artifact ID appears in both the ID field and filename
- Non-task artifacts (initiatives, epics, governance) are unaffected
- All existing branchname tests updated to reflect new format
