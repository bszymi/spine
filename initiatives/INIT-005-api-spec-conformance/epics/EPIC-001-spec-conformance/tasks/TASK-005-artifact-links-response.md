---
id: TASK-005
type: Task
title: "Fix ArtifactLinksResponse and add link_type/direction filters"
status: Completed
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-005 — Fix ArtifactLinksResponse and add link_type/direction filters

## Purpose

Two conformance issues with `GET /artifacts/{path}/links`:
1. Response wraps as `{items: links}` but spec expects `{artifact_path, links: [{direction, link_type, target_path}]}`
2. Spec supports `link_type` and `direction` (outgoing/incoming/both) query parameters for filtering

## Deliverable

- Updated response structure to match `ArtifactLinksResponse` schema
- Query parameter parsing for `link_type` and `direction`
- Filtering logic in the handler or store layer

## Acceptance Criteria

- Response shape is `{artifact_path: "...", links: [{direction, link_type, target_path}]}`
- `link_type` query parameter filters links by type
- `direction` query parameter filters by outgoing/incoming/both (default: outgoing)
