---
id: TASK-013
type: Task
title: "Add pagination to query.history and query.runs"
status: Draft
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-013 — Add pagination to query.history and query.runs

## Purpose

The spec's `HistoryResponse` and `RunListResponse` both extend `PaginationMeta` (`next_cursor`, `has_more`). Current handlers return `{items: ...}` without pagination metadata. Additionally, `query.runs` is missing `status` query parameter filter and proper `limit`/`cursor` support.

## Deliverable

- Add `next_cursor` and `has_more` to `handleQueryHistory` and `handleQueryRuns` responses
- Add `status` query parameter filter to `handleQueryRuns`
- Add `limit` and `cursor` parsing to `handleQueryRuns`
- Update store/projection query methods to support cursor-based pagination if not already

## Acceptance Criteria

- `GET /query/history` response includes `items`, `next_cursor`, `has_more`
- `GET /query/runs` response includes `items`, `next_cursor`, `has_more`
- `GET /query/runs` supports `status`, `limit`, and `cursor` query parameters
