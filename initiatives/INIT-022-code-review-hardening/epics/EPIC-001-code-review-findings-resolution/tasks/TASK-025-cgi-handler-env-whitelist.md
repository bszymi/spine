---
id: TASK-025
type: Task
title: "Lock cgi.Handler.Env whitelist with a regression test"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-025 — Lock cgi.Handler.Env whitelist with a regression test

---

## Purpose

The 2026-05-07 code review's original finding ("CGI process inherits
the full Spine server environment") was **incorrect** — codex review
of the governance scaffolding caught that
`internal/githttp/handler.go:272-279` already sets `Env` to an
explicit two-element list (`GIT_PROJECT_ROOT`, `GIT_HTTP_EXPORT_ALL`)
and does not set `InheritEnv`, so Go's `net/http/cgi` does not
inherit any parent process variables.

The protection exists. The risk is that a future refactor (adding a
new env var, switching to `os.Environ()`-derived setup, enabling
`InheritEnv`) silently regresses it. The right action is therefore a
**regression test** that locks the current minimal whitelist in
place, not a code change.

This task supersedes the original P3 hardening finding which has been
re-classified as a non-issue.

## Deliverable

- Add a unit test in `internal/githttp/handler_test.go` (or a new
  sibling) that:
  - Invokes the handler against a fixture repo with a small CGI
    "stub" git binary on PATH (or a path where git-http-backend
    runs but its env can be captured).
  - Captures the env vars the CGI subprocess sees.
  - Asserts the env contains ONLY:
    - The CGI-standard variables Go's `net/http/cgi` always sets
      (REQUEST_METHOD, QUERY_STRING, REMOTE_ADDR, etc. — read the
      stdlib for the canonical list).
    - `GIT_PROJECT_ROOT` (with the expected value).
    - `GIT_HTTP_EXPORT_ALL=1`.
  - Asserts the env does NOT contain any of the host process's
    sensitive vars: stub a `SMP_ADMIN_TOKEN=secret`, `AWS_SECRET_ACCESS_KEY=secret`,
    or similar in the test process before the request, and assert the
    CGI subprocess does not see them.

## Acceptance Criteria

- The new test passes against the current handler.
- Adding `InheritEnv: []string{"SMP_ADMIN_TOKEN"}` to the handler
  causes the test to fail (regression bait).
- Replacing `Env: []string{...}` with `Env: os.Environ()` causes the
  test to fail.

## Out of Scope

- Adding new env vars to the whitelist. If git-http-backend ever
  needs another variable, that's a separate change.
- Pinning the host's `git` binary version (separate ops concern).

## Notes

The regression-test framing emerged from codex review of
INIT-022's task scaffolding. The original finding's premise was
wrong, but locking the existing protection in place is still a
valid hardening step.
