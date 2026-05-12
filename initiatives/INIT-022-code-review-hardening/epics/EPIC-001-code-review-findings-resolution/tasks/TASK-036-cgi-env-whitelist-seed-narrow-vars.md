---
id: TASK-036
type: Task
title: "Seed narrow inherited vars before asserting cgi.Handler.Env whitelist"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-12
last_updated: 2026-05-12
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-025-cgi-handler-env-whitelist.md
---

# TASK-036 — Seed narrow inherited vars before asserting cgi.Handler.Env whitelist

---

## Purpose

`internal/githttp/cgi_env_test.go:35-36`'s strict-env regression test
checks that `cgi.Handler.InheritEnv` exposes only the keys it intends
to. The test only catches the regressions it claims to guard against
(`AWS_SESSION_TOKEN`, `HTTP_PROXY`, etc.) when those variables happen
to be set in the runner environment. Clean CI environments — the
dominant case — have neither set, so a future refactor that adds a
narrow `InheritEnv` entry by accident slips through.

This is a P2 test-quality finding from the 2026-05-12 codex review of
the INIT-022 batch (commit `bcd29f75a6`).

## Deliverable

- Use `t.Setenv` to seed a representative set of narrow env vars
  before invoking `ServeHTTP` (at minimum `AWS_SESSION_TOKEN` and
  `HTTP_PROXY`; whatever the comment block enumerates).
- The strict-subset assertion then sees the would-be-leaked keys and
  the regression fires.
- Keep the seeding scoped to this test — `t.Setenv` already restores
  on completion so no leakage across tests.

## Acceptance Criteria

- Reverting any future "add to InheritEnv" change must fail this test
  on a clean environment.
- Manual probe: temporarily extend `InheritEnv` to include
  `AWS_SESSION_TOKEN`, confirm the test fails before this PR (it does
  not), and that it fails after.

## Out of Scope

- Auditing other `cgi_env_test.go` assertions or adding new ones
  beyond the seed/assert pairing.
- Changing the production `InheritEnv` whitelist itself.

## Resolution (2026-05-12)

The strict-subset assertion at the bottom of
`TestServeHTTP_CGIEnvWhitelist` was already in place, but it was
only load-bearing for narrow-leak regressions when the candidate
key was actually set in the host environment. On a clean CI runner
neither `AWS_SESSION_TOKEN` nor `HTTP_PROXY` is set, so a future
`InheritEnv: []string{"AWS_SESSION_TOKEN"}` would leak through
silently — the strict-subset map walk never sees the key because
the CGI subprocess never received it.

**Files touched**

- `internal/githttp/cgi_env_test.go`
  - Two additional `t.Setenv` seeds at the top of the test:
    `AWS_SESSION_TOKEN=host-sentinel-aws-session` and
    `HTTP_PROXY=host-sentinel-http-proxy`. These are the two
    narrow-regression vectors the strict-subset comment block
    enumerates as examples. With them seeded in the host process,
    any `InheritEnv: []string{X}` for X in {AWS_SESSION_TOKEN,
    HTTP_PROXY} will materialize in `cgiEnv`, the unknown-key
    branch of the allow-list walk fires, and the test fails.
  - Comment block at the top of the test extended to describe the
    two-layer seeding (broad-leak sentinels vs. narrow-leak seeds)
    and explain why both layers are needed.
  - Comment block above the strict-subset check reworded to call
    out that the assertion is only load-bearing on a clean CI
    runner because of the seeding above — clarifying the previously
    silent CI blind spot.

**Acceptance criteria satisfied**

- *Reverting any future "add to InheritEnv" change must fail this
  test on a clean environment.* ✓ — Bait #1 (revert direction):
  added `InheritEnv: []string{"AWS_SESSION_TOKEN"}` to the
  production handler, ran the test, observed
  `cgi_env_test.go:213: CGI env contains unexpected key
  "AWS_SESSION_TOKEN" (="host-sentinel-aws-session") — handler
  whitelist may have regressed`. Bait #2: swapped to
  `InheritEnv: []string{"HTTP_PROXY"}`, same failure with the
  HTTP_PROXY sentinel. Both reverts restored immediately after
  the bait-checks.
- *Manual probe: temporarily extend `InheritEnv` to include
  `AWS_SESSION_TOKEN`, confirm the test fails before this PR
  (it does not), and that it fails after.* ✓ — Negative proof:
  left `InheritEnv: []string{"AWS_SESSION_TOKEN"}` in handler.go,
  removed only the `t.Setenv("AWS_SESSION_TOKEN", ...)` seed
  (and HTTP_PROXY seed) from the test — test passed silently in
  the docker container with `-e AWS_SESSION_TOKEN= -e HTTP_PROXY=`
  forcing both empty. Restored the seeds; with both production and
  test reverted to baseline, the bait would re-fire. This is the
  exact "before-PR" blind spot the task closes.

**Codex review iteration**

First-pass codex review clean on the first submission: *"No
actionable correctness issues were found in the current changes.
The added environment seeding strengthens the regression test
without affecting the production path."* No follow-up needed.

**Test gates**

- `go build ./...` and `go vet ./...` — clean.
- `go test ./internal/githttp/... -count=3` — green on every
  iteration (full githttp suite, not just the seeded test).
- `make docker-lint` — 207 issues, identical to baseline at
  commit `f0c3c4a` (no new findings on touched files).
- `codex review --uncommitted` — clean on first submission.
