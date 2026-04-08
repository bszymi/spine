---
id: TASK-004
type: Task
title: API endpoint for adding artifacts to a planning run
status: Draft
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: implementation
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/epic.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/tasks/TASK-001-api-endpoint.md
---

# TASK-004 — API Endpoint for Adding Artifacts to a Planning Run

---

## Purpose

Add a `POST /artifacts/add` endpoint that allows adding artifacts to an existing planning run's branch. This enables UI/management platform users to incrementally add child artifacts (e.g., tasks under an epic) without writing files to Git directly.

---

## Deliverable

Extend `internal/gateway/handlers_artifacts.go`.

### Request schema

```json
{
  "run_id": "uuid",
  "artifact_type": "Task",
  "title": "Implement validation"
}
```

- `run_id` (required): the planning run to add to
- `artifact_type` (required): Task, Epic, or ADR
- `title` (required): human-readable title

### Response schema

```json
{
  "artifact_id": "TASK-002",
  "artifact_path": "initiatives/INIT-003/.../TASK-002-implement-validation.md",
  "branch": "INIT-003/EPIC-003/EPIC-004-new-feature"
}
```

### Handler flow

1. Look up the planning run by `run_id` — verify it exists and is in the `draft` step
2. Determine the parent from the run's root artifact (e.g., if the run created EPIC-004, tasks are scoped to it)
3. Scan the **branch** (not main) for existing artifacts to allocate the next ID — because previous `add` calls or direct file writes may have already created artifacts
4. Call `Slugify(title)` and `BuildArtifactPath()`
5. Build artifact content (front-matter with id, type, title, status: Draft, parent link)
6. Write the file to the branch using `ArtifactWriter`
7. Return the artifact ID, path, and branch name

### Key difference from `POST /artifacts/create`

- `create` starts a new planning run
- `add` writes to an existing planning run's branch
- `add` scans the branch ref for next ID (not main), since earlier artifacts on the branch must be accounted for
- `add` only works while the run is in the `draft` step (actor hasn't submitted yet)

### Update `api/spec.yaml`

Add the new endpoint, request/response types, and error responses (400 invalid input, 404 run not found, 409 run not in draft step).

---

## Acceptance Criteria

- Endpoint writes an artifact to the correct branch
- ID allocation scans the branch, not just main
- Returns 409 if the run is past the draft step
- Returns 404 if the run doesn't exist
- Multiple `add` calls produce sequentially numbered artifacts
- Artifacts created via `add` are indistinguishable from artifacts written directly to the branch
