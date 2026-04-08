---
id: TASK-001
type: Task
title: API endpoint for artifact creation
status: Pending
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: implementation
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/epic.md
---

# TASK-001 — API Endpoint for Artifact Creation

---

## Purpose

Add a `POST /artifacts/create` endpoint to the gateway that orchestrates the full artifact creation flow: validate inputs, allocate ID, resolve workflow, and start a planning run.

---

## Deliverable

Extend `internal/gateway/handlers_artifacts.go` (or create a new handler file).

### Request schema

```json
{
  "artifact_type": "Task",
  "parent": "EPIC-003",
  "title": "Implement validation"
}
```

- `artifact_type` (required): Task, Epic, or Initiative (ADR excluded — no creation workflow exists for it yet)
- `parent` (required for Task/Epic): parent artifact ID — the endpoint resolves this to a path
- `title` (required): human-readable title for the artifact

### Response schema

```json
{
  "run_id": "uuid",
  "artifact_id": "TASK-006",
  "artifact_path": "initiatives/INIT-003/.../TASK-006-implement-validation.md",
  "branch": "INIT-003/EPIC-003/TASK-006-implement-validation",
  "workflow_id": "artifact-creation"
}
```

### Handler flow

1. Validate inputs (type valid, title non-empty, parent exists)
2. Resolve parent path from parent ID (e.g., EPIC-003 -> full path)
3. Call `NextID()` to allocate the next ID within the parent scope
4. Call `Slugify(title)` to generate the slug
5. Call `BuildArtifactPath()` to compute the target path
6. Call `ResolveBindingWithMode(ctx, ..., artifactType, "", "creation")` to find the workflow
7. Build initial artifact content (front-matter with id, type, title, status: Draft, parent link)
8. Call `StartPlanningRun()` with the artifact content and target path
9. Return run ID, artifact ID, path, branch, and workflow ID

### Update `api/spec.yaml`

Add the new endpoint schema, request/response types, and error responses (400 for invalid input, 404 for parent not found, 409 for workflow conflict).

---

## Acceptance Criteria

- Endpoint creates a planning run with the correct artifact content
- Parent artifact is validated (must exist, must be the right type — epic for task, initiative for epic)
- Invalid inputs return 400 with clear error messages
- Missing parent returns 404
- Response includes all fields needed for the caller to track the run
