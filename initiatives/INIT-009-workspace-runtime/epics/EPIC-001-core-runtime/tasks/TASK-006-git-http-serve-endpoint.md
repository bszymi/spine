---
id: TASK-006
type: Task
title: "Add Git HTTP Serve Endpoint"
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-001-core-runtime/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
work_type: implementation
created: 2026-04-16
last_updated: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-001-core-runtime/epic.md
---

# TASK-006 — Add Git HTTP Serve Endpoint

---

## Purpose

Allow external systems (runner containers, CI tools, IDEs) to clone from Spine directly via HTTP. Spine already manages the authoritative git repository — serving it over HTTP removes the dependency on external git hosting (GitHub, GitLab) for execution workflows.

This is the key enabler for runner containers to `git clone http://spine:8080/repo --branch $BRANCH` without needing SSH keys, GitHub tokens, or external network access.

## Deliverable

Add a git smart HTTP endpoint to Spine's API server:

1. **`GET /git/info/refs?service=git-upload-pack`** — advertise refs (required for clone/fetch)
2. **`POST /git/git-upload-pack`** — serve pack data (required for clone/fetch)

Implementation options:
- **Wrap `git http-backend`** — Spine already has git installed. Set `GIT_PROJECT_ROOT` to `SPINE_REPO_PATH` and proxy requests to `git http-backend` CGI. This is the standard approach used by Gitea, cgit, etc.
- **Native Go implementation** — use `go-git` or shell out to `git upload-pack`. More control but more work.

The endpoint should:
- Be read-only (no push — Spine manages writes through its own API)
- Support shallow clones (`--depth 1`) for fast runner execution
- Support branch-specific clones (`--branch`)
- Be accessible without authentication from the internal Docker network (runner containers)
- Optionally support authentication for external access

## Acceptance Criteria

- `git clone http://spine:8080/git --depth 1 --branch main /workspace` works from a Docker container on the same network
- `git clone http://spine:8080/git --depth 1 --branch spine/run/task-001-... /workspace` works for task branches
- Read-only — `git push` is rejected
- Performance: shallow clone of a medium repo (1000 files) completes in under 2 seconds on the local network
- No authentication required from the internal network (same Docker network as Spine)
