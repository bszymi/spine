---
id: EPIC-011
type: Epic
title: Discussion System
status: Pending
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/initiative.md
  - type: supersedes
    target: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/tasks/TASK-007-discussion-comments.md
---

# EPIC-011 — Discussion System

---

## Purpose

Implement the discussion and comments runtime defined in architecture/discussion-model.md. This enables threaded discussions on artifacts and step executions for governance review, collaboration, and audit trail.

---

## Key Work Areas

- Domain types and store for discussion threads and comments
- Thread lifecycle management (open, resolve, reopen)
- API endpoints for CRUD operations
- Thread binding to artifacts and step executions
- CLI commands for viewing and participating
- Event emission for discussion activity
- Integration with workflow steps (review comments, acceptance rationale)

---

## Primary Outputs

- Discussion thread and comment domain types with store persistence
- Database migration for discussion tables
- REST API endpoints for thread and comment operations
- CLI commands for discussion management
- Discussion events emitted via event router

---

## Acceptance Criteria

- Threads can be created on any artifact or step execution
- Comments support threaded replies
- Threads can be resolved and reopened
- Discussion state is queryable via API and CLI
- Discussion events are emitted (thread_created, comment_added, thread_resolved)
- Thread resolution integrates with workflow step completion
