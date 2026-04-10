---
id: TASK-003
type: Task
title: "Handle push gracefully when no credentials configured"
status: Pending
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
---

# TASK-003 — Handle push gracefully when no credentials configured

---

## Purpose

Currently, when git push fails (no auth), the run stays in `committing` state retrying indefinitely. This blocks run completion and prevents artifact status updates. When no credentials are configured, push should be skipped and the run should complete normally.

## Deliverable

`internal/engine/merge.go`, `internal/scheduler/scheduler.go` updates

Content should define:

- `SPINE_GIT_PUSH_ENABLED` config flag (default: true)
- When false, skip push entirely and complete the run
- When true but push fails with auth error, mark as permanent failure (don't retry)
- Distinguish auth failures from transient network errors
- Transient errors: retry with backoff. Auth errors: fail immediately.

## Acceptance Criteria

- Runs complete successfully when push is disabled
- Auth failures stop retry loop immediately (not after N retries)
- Transient failures still retried with exponential backoff
- Run status transitions: committing → completed (not stuck)
- Artifact status updates applied even when push is skipped
