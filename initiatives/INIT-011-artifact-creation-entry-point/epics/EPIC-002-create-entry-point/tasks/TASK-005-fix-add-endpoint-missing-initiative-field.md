---
id: TASK-005
type: Task
title: "Fix addArtifactToRun: populate initiative field when scaffolding child tasks"
status: Completed
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: bugfix
created: 2026-04-16
last_updated: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/epic.md
  - type: relates_to
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/tasks/TASK-004-artifact-add-endpoint.md
---

# TASK-005 — Fix addArtifactToRun: Populate Initiative Field When Scaffolding Child Tasks

---

## Problem

When calling `POST /artifacts/add` to add a Task to an existing planning run, Spine scaffolds the task frontmatter but does **not** populate the `initiative` field. The subsequent validation then rejects the file with:

```
missing required field: initiative (Task)
```

The parent epic has the initiative link in its frontmatter, so Spine has all the information needed to derive the initiative field — it just doesn't do it.

## Root Cause

The artifact scaffolding in the `add` handler (step 5 in TASK-004's spec) builds frontmatter with `id`, `type`, `title`, `status`, `epic`, and `parent` link — but omits the `initiative` field. The validator requires `initiative` on all Task artifacts.

## Deliverable

When scaffolding a Task via `POST /artifacts/add`:

1. Read the parent epic's frontmatter from the branch
2. Extract the `initiative` field from the parent
3. Include it in the scaffolded task's frontmatter

This should work recursively — if adding an Epic under an Initiative, the initiative is the parent itself. If adding a Task under an Epic, the initiative comes from the epic's `initiative` field.

## Acceptance Criteria

- `POST /artifacts/add` with `artifact_type: Task` produces a file with a valid `initiative` field
- The `initiative` field is inherited from the parent epic's frontmatter
- Validation passes without manual intervention
- Existing `POST /artifacts/create` (entry point) is unaffected
