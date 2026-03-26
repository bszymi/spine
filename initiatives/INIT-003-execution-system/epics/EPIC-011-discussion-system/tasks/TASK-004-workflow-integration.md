---
id: TASK-004
type: Task
title: Discussion Workflow Integration
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-011-discussion-system/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-011-discussion-system/epic.md
---

# TASK-004 — Discussion Workflow Integration

## Purpose

Integrate the discussion system with the workflow engine so that review steps can require discussion resolution before proceeding, and acceptance/rejection decisions can reference discussion threads.

## Deliverable

- Precondition type: discussions_resolved — blocks step if open threads exist on the artifact
- Acceptance rationale linking: task accept/reject can reference a discussion thread
- Review step integration: review outcomes can create or resolve threads
- Query: list open discussions for a run's artifacts

## Acceptance Criteria

- Steps with discussions_resolved precondition check for open threads
- Acceptance/rejection rationale can include thread references
- Review step completion auto-resolves associated threads (optional)
- Open discussions for a run are queryable
