---
id: TASK-001
type: Task
title: Discussion Domain Types and Store
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-011-discussion-system/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-011-discussion-system/epic.md
---

# TASK-001 — Discussion Domain Types and Store

## Purpose

Define the domain types for discussion threads and comments, implement PostgreSQL persistence, and create the database migration.

## Deliverable

- Domain types: DiscussionThread, Comment with status lifecycle
- Store interface methods: CreateThread, GetThread, ListThreads, UpdateThread, CreateComment, ListComments
- PostgreSQL implementation of all store methods
- Migration for discussion_threads and comments tables
- Thread binding to artifacts (artifact_path) and step executions (execution_id)

## Acceptance Criteria

- Thread and comment types have json + yaml tags
- Store methods are parameterized and use nilIfEmpty
- Migration creates tables with proper indexes
- Threads support open/resolved/reopened status
- Comments track author, timestamp, and optional parent_id for nesting
