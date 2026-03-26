---
id: TASK-003
type: Task
title: Discussion CLI Commands and Events
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-011-discussion-system/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-011-discussion-system/epic.md
---

# TASK-003 — Discussion CLI Commands and Events

## Purpose

Add CLI commands for viewing and participating in discussions, and emit discussion events for observability.

## Deliverable

- spine discussion list [--artifact PATH] [--status open|resolved] — list threads
- spine discussion show [thread-id] — display thread with comments
- spine discussion comment [thread-id] [message] — add comment
- spine discussion resolve [thread-id] — resolve thread
- Emit thread_created, comment_added, thread_resolved events
- Table and JSON output formats

## Acceptance Criteria

- CLI commands follow existing patterns (newAPIClient, output format flag)
- Events use fire-and-forget pattern with proper payload
- thread_created, comment_added, thread_resolved event types already defined in domain/event.go
- Commands fail gracefully when server is unreachable
