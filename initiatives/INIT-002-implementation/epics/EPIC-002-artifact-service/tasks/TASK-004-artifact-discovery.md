---
id: TASK-004
type: Task
title: Artifact Discovery
status: Pending
epic: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
---

# TASK-004 — Artifact Discovery

## Purpose

Scan a Git repository to discover all artifacts and workflow definitions.

## Deliverable

- Full repository scan (find all `.md` files with valid front matter)
- Workflow definition discovery (find all `.yaml` files in `/workflows/`)
- Change discovery from Git diff (incremental — per Git Integration §8.2)
- Classify discovered files by artifact type
- Handle non-artifact files gracefully (skip without error)

## Acceptance Criteria

- Full scan discovers all artifacts in the test repository
- Incremental scan from a commit diff detects created, modified, and deleted artifacts
- Workflow definitions are discovered separately from Markdown artifacts
- Non-artifact files (README, config, etc.) are skipped
- Integration tests with real Git repos containing mixed file types
