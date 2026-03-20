---
id: TASK-003
type: Task
title: Git Client Interface and CLI Implementation
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
---

# TASK-003 — Git Client Interface and CLI Implementation

## Purpose

Implement the GitClient interface with a CLI-based implementation that shells out to the `git` binary.

## Deliverable

- `internal/git/client.go` — GitClient interface (per Implementation Guide §3.1)
- `internal/git/cli.go` — CLI implementation (subprocess execution)
- `internal/git/testutil.go` — Test helpers (temp repo creation, fixture setup)
- Commit with structured trailers (Trace-ID, Actor-ID, Run-ID, Operation per Git Integration §5)
- Branch operations (create, delete, merge, list)
- File operations (read, list, diff)
- Error handling with retry for transient failures

## Acceptance Criteria

- All GitClient interface methods implemented and tested
- Integration tests run against real temporary Git repos
- Commit messages include structured trailers
- Merge operations support fast-forward and merge-commit
- Error classification (transient vs permanent) per Error Handling §5
- Idempotent commit detection via Trace-ID
