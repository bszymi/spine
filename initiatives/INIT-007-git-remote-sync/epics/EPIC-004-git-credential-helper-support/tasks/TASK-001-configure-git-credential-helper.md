---
id: TASK-001
type: Task
title: "Configure Git to use external credential helper"
status: Pending
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
---

# TASK-001 — Configure Git to use external credential helper

---

## Purpose

Configure Spine's Git operations to use an external credential helper for push authentication. The helper is a script provided by the hosting platform, mounted into the container.

## Deliverable

`internal/git/cli.go` updates

Content should define:

- Set `credential.helper` in Git config during workspace initialization
- Read credential helper path from `SPINE_GIT_CREDENTIAL_HELPER` env var
- If not set, credential helper is not configured (existing behavior)
- Git environment setup in Push() to include credential helper config

## Acceptance Criteria

- When `SPINE_GIT_CREDENTIAL_HELPER` is set, Git uses it for push auth
- When not set, push behavior is unchanged (no credential helper)
- Credential helper path validated at startup (exists, executable)
- Git config set per-repo, not globally
