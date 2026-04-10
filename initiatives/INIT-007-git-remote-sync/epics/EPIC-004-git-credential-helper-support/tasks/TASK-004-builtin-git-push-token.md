---
id: TASK-004
type: Task
title: "Support SPINE_GIT_PUSH_TOKEN for standalone deployments"
status: Pending
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-004-git-credential-helper-support/epic.md
---

# TASK-004 — Support SPINE_GIT_PUSH_TOKEN for standalone deployments

---

## Purpose

Users running Spine without a management platform (CLI, Docker, self-hosted) need a simple way to authenticate git push. A single environment variable containing a PAT or deploy token is the simplest approach — no credential helper, no external API.

## Deliverable

`internal/git/cli.go`, `internal/git/credentials.go`

Content should define:

### Credential resolution chain

Spine resolves push credentials in priority order:

1. **External credential helper** (`SPINE_GIT_CREDENTIAL_HELPER`) — if set, Git uses it for all pushes. This is the SMP/custom platform integration point.
2. **Built-in token** (`SPINE_GIT_PUSH_TOKEN`) — if set, Spine rewrites the remote URL to inject the token as HTTPS auth (`https://x-access-token:{token}@host/repo.git`).
3. **Git native** — whatever the user configured in their git config (SSH keys, credential store, etc.). Spine does nothing, Git handles it.
4. **None** — push fails gracefully per TASK-003.

### Implementation

- Read `SPINE_GIT_PUSH_TOKEN` at startup, validate non-empty
- Before `git push`, if token is set and remote is HTTPS, rewrite URL with embedded token
- Token never logged — redact from all log output
- Support optional `SPINE_GIT_PUSH_USERNAME` (defaults to `x-access-token` for GitHub/GitLab)
- Remote URL rewrite is temporary (in-memory only, never written to git config)

## Acceptance Criteria

- Setting `SPINE_GIT_PUSH_TOKEN=ghp_xxx` makes push work to GitHub
- Token works with GitHub, GitLab, Bitbucket HTTPS remotes
- Token never appears in logs (redacted)
- Does not interfere when credential helper is also configured (helper takes priority)
- Remote URL on disk unchanged (rewrite is per-push, in-memory only)
