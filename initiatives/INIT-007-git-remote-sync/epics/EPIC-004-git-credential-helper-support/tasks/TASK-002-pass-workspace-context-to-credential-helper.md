---
id: TASK-002
type: Task
title: "Pass workspace context to credential helper"
status: Pending
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
---

# TASK-002 — Pass workspace context to credential helper

---

## Purpose

In shared deployment mode, multiple workspaces share one Spine instance. The credential helper needs to know which workspace's credentials to fetch. Pass workspace identity via environment variable when invoking Git.

## Deliverable

`internal/git/cli.go` updates

Content should define:

- Set `SPINE_WORKSPACE_ID` environment variable when calling `git push`
- In shared mode, use the workspace ID from the run/context
- In dedicated mode, use the single workspace ID from config
- Credential helper script reads this to call the correct platform API

## Acceptance Criteria

- `SPINE_WORKSPACE_ID` set in Git push environment in shared mode
- Credential helper can read the workspace ID from environment
- Works for all push scenarios: run merge, artifact commit, branch push
