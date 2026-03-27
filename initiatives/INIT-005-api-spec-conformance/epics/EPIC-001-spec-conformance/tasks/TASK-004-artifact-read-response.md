---
id: TASK-004
type: Task
title: "Fix ArtifactReadResponse field names and add source_commit"
status: Completed
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-004 — Fix ArtifactReadResponse field names and add source_commit

## Purpose

The handler serializes `domain.Artifact` directly, whose JSON tags use `path`, `type`, `id`. The spec expects `artifact_path`, `artifact_type`, `artifact_id`. The spec also requires `source_commit` (the git commit the artifact was read from).

## Deliverable

- Response DTO or explicit response map in `handleArtifactRead` that maps domain fields to spec field names
- `source_commit` field populated from the git ref used to read the artifact

## Acceptance Criteria

- `GET /artifacts/{path}` response uses `artifact_path`, `artifact_type`, `artifact_id` field names
- Response includes `source_commit` with the git commit SHA
- All fields from `ArtifactReadResponse` schema are present: `artifact_path`, `artifact_id`, `artifact_type`, `status`, `title`, `metadata`, `content`, `source_commit`
