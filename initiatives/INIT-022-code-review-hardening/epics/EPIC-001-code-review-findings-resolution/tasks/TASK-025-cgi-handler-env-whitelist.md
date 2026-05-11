---
id: TASK-025
type: Task
title: "Lock cgi.Handler.Env whitelist with a regression test"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-11
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

## Resolution (2026-05-11)

Added `internal/githttp/cgi_env_test.go::TestServeHTTP_CGIEnvWhitelist`.
The handler itself is unchanged — the protection was already in
place at `internal/githttp/handler.go:272-279` (explicit two-element
`Env`, no `InheritEnv`). The test runs the ServeHTTP CGI dispatch
against a stub `git-http-backend` (a `#!/bin/sh` script that dumps
its environment via `env(1)` to a fixed capture path before
emitting a minimal valid CGI response), then asserts both halves
of the whitelist invariant:

1. **Required keys present** — `GIT_PROJECT_ROOT` (matching the
   resolved repo path) and `GIT_HTTP_EXPORT_ALL=1`, plus the
   CGI-standard `REQUEST_METHOD` / `QUERY_STRING` as a smoke test
   that the dispatch path is intact.
2. **Sentinel host vars absent** — `SMP_ADMIN_TOKEN` and
   `AWS_SECRET_ACCESS_KEY` injected into the test process via
   `t.Setenv` must not appear in the CGI subprocess env. Catches
   broad regressions (`Env: os.Environ()` substitution).
3. **Strict subset** — every key in the captured env must appear
   in an explicit allow-list. The list enumerates the
   CGI-standard variables Go's `net/http/cgi` always sets
   (`SERVER_*`, `GATEWAY_INTERFACE`, `REQUEST_*`, `PATH_INFO`,
   `SCRIPT_*`, `REMOTE_*`, `PATH`, `HTTP_HOST`), the handler's
   own whitelist, `osDefaultInheritEnv` entries the stdlib
   whitelists by platform (`LD_LIBRARY_PATH` on Linux,
   `DYLD_LIBRARY_PATH` on macOS/iOS), and shell-self-injected
   variables (`PWD`, `SHLVL`, `_`, `OLDPWD`) that come from the
   POSIX shell's own startup state, not from host inheritance.

The strict-subset check is what catches narrow regressions like
`InheritEnv: []string{"AWS_SESSION_TOKEN"}` or
`InheritEnv: []string{"HTTP_PROXY"}` — both flagged by codex
review of the first two test iterations and verified to fail the
new check before submission.

**Regression-bait verification** (manual, pre-submission):

| Mutation | Result |
| --- | --- |
| `InheritEnv: []string{"SMP_ADMIN_TOKEN"}` | FAIL — sentinel leak detected. |
| `Env: os.Environ()` | FAIL — sentinels leak + whitelisted vars missing. |
| `InheritEnv: []string{"AWS_SESSION_TOKEN"}` (no sentinel for it) | FAIL — strict-subset catches unexpected key. |
| `InheritEnv: []string{"HTTP_PROXY"}` (HTTP_* namespace) | FAIL — strict-subset rejects HTTP_PROXY (only HTTP_HOST allowed). |

**Test gates**

- `go build ./...` — clean.
- `go vet ./internal/githttp/...` — clean.
- `gofmt -l internal/githttp/` — clean.
- `go test ./internal/githttp/... -count=1 -race` — green.
- `go test ./... -count=1 -race` — green (incl. the prior TASK-026
  `internal/secrets/file_test.go::TestFileClient_VersionChangesOnEdit`
  flake site, which passed this run).
- `go test -tags=scenario -count=1 ./internal/scenariotest/scenarios/...`
  — green.
- `make docker-lint` — 206 baseline unchanged (test introduced
  one `gocritic httpNoBody` finding initially; resolved by
  switching to `http.NoBody`).
- `codex review` — clean on pass 3 ("No discrete correctness
  issues were identified in the added regression test"). Passes
  1 and 2 each surfaced a P2 finding (sentinel-only check; HTTP_*
  wildcard) that the strict-subset enumeration addresses.
