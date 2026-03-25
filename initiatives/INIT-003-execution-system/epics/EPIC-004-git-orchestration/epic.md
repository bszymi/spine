---
id: EPIC-004
type: Epic
title: Git Orchestration Layer
status: Completed
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/initiative.md
---

# EPIC-004 — Git Orchestration Layer

---

## Purpose

Enable durable execution outcomes by implementing the Git orchestration layer. This is NOT standard Git usage — Spine owns the merge strategy, creates branches per run, and controls when outcomes are committed to the authoritative branch.

Currently, all artifact writes go directly to main. This epic implements task-branch scoped writes, the commit-then-merge flow, and automatic retry for failed Git operations.

---

## Key Work Areas

- WriteContext abstraction for branch-scoped writes
- Branch-per-run creation and lifecycle
- Commit model (step outcomes to task branch)
- Merge strategy (Spine-owned merge to authoritative branch on run completion)
- Git commit retry for stuck `committing` state

---

## Primary Outputs

- WriteContext implementation in artifact service
- Branch lifecycle management in engine orchestrator
- Merge-on-completion logic
- Commit retry mechanism in scheduler

---

## Acceptance Criteria

- Runs create isolated Git branches for artifact writes
- Step outcomes are committed to the run's branch, not main
- On run completion, the branch is merged to the authoritative branch
- Merge conflicts are detected and reported (not silently dropped)
- Runs stuck in `committing` state are retried automatically
- Branch cleanup occurs after successful merge
