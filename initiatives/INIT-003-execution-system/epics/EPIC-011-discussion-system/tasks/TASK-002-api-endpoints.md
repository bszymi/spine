---
id: TASK-002
type: Task
title: Discussion API Endpoints
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-011-discussion-system/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-011-discussion-system/epic.md
---

# TASK-002 — Discussion API Endpoints

## Purpose

Implement REST API endpoints for creating, reading, and managing discussion threads and comments.

## Deliverable

- POST /api/v1/discussions — create thread (bound to artifact or execution)
- GET /api/v1/discussions — list threads with filters (artifact_path, status)
- GET /api/v1/discussions/{thread_id} — get thread with comments
- POST /api/v1/discussions/{thread_id}/comments — add comment
- POST /api/v1/discussions/{thread_id}/resolve — resolve thread
- POST /api/v1/discussions/{thread_id}/reopen — reopen thread
- Permissions: reader can view, contributor can create/comment, reviewer can resolve

## Acceptance Criteria

- All endpoints follow existing gateway patterns (auth, error handling, JSON responses)
- Thread creation validates the target exists (artifact or execution)
- Comments support optional parent_id for threading
- Resolve/reopen follow thread state machine
- Endpoints return proper error codes for invalid states
