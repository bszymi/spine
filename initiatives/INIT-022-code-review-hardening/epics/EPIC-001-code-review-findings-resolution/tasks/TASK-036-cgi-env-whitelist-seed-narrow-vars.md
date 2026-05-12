---
id: TASK-036
type: Task
title: "Seed narrow inherited vars before asserting cgi.Handler.Env whitelist"
status: Pending
acceptance: Pending
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
