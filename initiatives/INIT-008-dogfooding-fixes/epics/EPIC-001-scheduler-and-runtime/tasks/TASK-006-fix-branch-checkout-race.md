---
id: TASK-006
type: Task
title: "Fix branch checkout race in artifact service"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
---

# TASK-006 — Fix Branch Checkout Race in Artifact Service

---

## Purpose

`enterBranch` in `/internal/artifact/service.go` (lines 344-361) uses `git checkout` which changes the working tree for the entire OS process. The `branchMu` mutex serializes within the artifact service, but the Orchestrator holds a separate `git.GitClient` and calls `CreateBranch`, `Merge`, `Push` independently. A concurrent `MergeRunBranch` call races against an artifact write on a planning branch. Artifacts can be committed to the wrong branch under concurrent load, silently corrupting the repo.

---

## Deliverable

Replace `git checkout` with git worktrees for branch-scoped writes, or serialize all git operations through a single goroutine/lock shared between the artifact service and the orchestrator.

---

## Acceptance Criteria

- Concurrent artifact writes and run merges do not corrupt branch state
- No process-wide `git checkout` calls remain in the artifact service
- Existing scenario tests pass
