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

This is the key enabler for runner containers to `git clone http://spine:8080/git/{workspace_id} --branch $BRANCH` without needing SSH keys, GitHub tokens, or external network access.

## Deliverable

Add a git smart HTTP endpoint to Spine's API server:

1. **`GET /git/{workspace_id}/info/refs?service=git-upload-pack`** — advertise refs (required for clone/fetch)
2. **`POST /git/{workspace_id}/git-upload-pack`** — serve pack data (required for clone/fetch)

In single-workspace mode, `{workspace_id}` can be omitted — `/git/info/refs` falls back to the default workspace. In shared (multi-tenant) mode, the workspace ID is required and is used to resolve the workspace's `RepoPath`.

Implementation options:
- **Wrap `git http-backend`** — Spine already has git installed. Resolve the workspace's `RepoPath` from the URL, set `GIT_PROJECT_ROOT` accordingly, and proxy requests to `git http-backend` CGI. This is the standard approach used by Gitea, cgit, etc.
- **Native Go implementation** — use `go-git` or shell out to `git upload-pack`. More control but more work.

The endpoint should:
- Be read-only (no push — Spine manages writes through its own API)
- Support shallow clones (`--depth 1`) for fast runner execution
- Support branch-specific clones (`--branch`)
- Be accessible without authentication from the internal Docker network (runner containers)
- Optionally support authentication for external access

### Security Requirements

**Network isolation:**
- Define a named Docker internal network in `docker-compose.yaml` for Spine and runner containers — this is the foundation for "no auth from internal network"
- Implement network-aware auth bypass: either a separate internal-only listener, IP allowlist (e.g., trust `172.16.0.0/12` only), or middleware that skips auth for `/git/{workspace_id}/*` only from internal IPs
- External access (outside the Docker network) must require authentication via the existing bearer token middleware

**`git http-backend` hardening (if using CGI wrapper):**
- Pin `GIT_PROJECT_ROOT` to the exact repo path — never derive it from the request URL to prevent path traversal attacks
- Explicitly set `http.receivepack = false` in the repo git config — do not rely solely on the absence of a push route
- Pass a minimal CGI environment to `git http-backend` — do not leak server environment variables
- Validate `GIT_HTTP_EXPORT_OK` or control `http.getanyfile` appropriately

**Resource protection:**
- Limit concurrent git pack operations (e.g., max 5 concurrent clones) to prevent CPU/memory/IO exhaustion
- Set a request timeout specific to git operations (e.g., 30s) — the global rate limiter (100 req/s) does not protect against long-lived clone requests
- Consider limiting max pack size to prevent excessive bandwidth consumption

**Observability:**
- Log all clone operations with source IP, requested branch/ref, and timestamp — even for unauthenticated internal requests
- Emit metrics for clone duration, pack size, and concurrent clone count

**Ref filtering (optional but recommended):**
- Consider advertising only specific branch patterns (e.g., `main`, `spine/run/*`) via `info/refs` rather than exposing all refs — branch names like `spine/run/task-001-...` leak internal task structure to unauthenticated callers

## Prerequisites

- A named Docker network must be defined in `docker-compose.yaml` for Spine and runner containers (replaces the current default bridge network for this purpose)

## Acceptance Criteria

### Functional
- `git clone http://spine:8080/git/ws-1 --depth 1 --branch main /workspace` works from a Docker container on the same network (shared mode)
- `git clone http://spine:8080/git --depth 1 --branch main /workspace` works in single-workspace mode (falls back to default workspace)
- `git clone http://spine:8080/git/ws-1 --depth 1 --branch spine/run/task-001-... /workspace` works for task branches
- Clone with an invalid or nonexistent workspace ID returns an appropriate HTTP error (404)
- Read-only — `git push` is rejected with an appropriate error
- Performance: shallow clone of a medium repo (1000 files) completes in under 2 seconds on the local network
- No authentication required from the internal Docker network (same named network as Spine)

### Security
- Requests from outside the internal Docker network are rejected unless a valid bearer token is provided
- `GIT_PROJECT_ROOT` is hardcoded — path traversal attempts (e.g., `../../etc/passwd`) return 400/403
- `git http-backend` is configured with `receivepack = false` — push attempts fail at the git protocol level, not just at the HTTP routing level
- Concurrent clone operations are bounded (e.g., 5 max) — exceeding the limit returns 429 or 503
- Git operations have a dedicated timeout (e.g., 30s) independent of the global rate limiter
- All clone operations are logged with source IP, requested ref, and timestamp
