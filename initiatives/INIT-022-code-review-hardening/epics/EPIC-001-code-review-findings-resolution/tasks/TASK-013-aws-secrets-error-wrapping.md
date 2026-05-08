---
id: TASK-013
type: Task
title: "Wrap AWS Secrets Manager errors with %w"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-08
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-013 — Wrap AWS Secrets Manager errors with %w

---

## Purpose

`internal/secrets/aws.go:178`'s `mapAWSError` returns
`fmt.Errorf("aws secrets manager: invalid request for %s: %s: %s", ...)`
with no `%w` for the "invalid request" branch. Callers cannot
`errors.Is(err, ErrInvalidParams)` or unwrap the underlying API error.

This is a P2 error-handling finding from the 2026-05-07 code review.

## Deliverable

- Introduce `ErrInvalidRequest` (sentinel) in `internal/secrets/`.
- In `mapAWSError`, wrap with `%w` against either `ErrInvalidRequest`
  or the original AWS error (whichever shape the existing translator
  uses for other branches).
- Audit every other branch of `mapAWSError`: any branch that produces a
  formatted error without `%w` should be brought into the same shape.

## Acceptance Criteria

- `errors.Is(mapAWSError(invalid), secrets.ErrInvalidRequest)` returns
  true.
- The existing `internal/secrets/aws_test.go::TestAWSClient_ErrorMapping`
  is extended (or its table grown) to cover the new sentinel.
- Calling code that already handles other secrets-error sentinels
  (e.g. `ErrSecretNotFound`) is unaffected.

## Out of Scope

- Switching the AWS SDK error model wholesale.
- Touching the file-mounted SecretClient — its error mapping is
  separate.

## Resolution (2026-05-08)

Added `ErrInvalidRequest` to the sentinel block in
`internal/secrets/client.go` and rewrote the `InvalidParameterException
/ InvalidRequestException` branch of `mapAWSError` to wrap it with
`%w` — same shape used by the `ErrSecretNotFound` and `ErrAccessDenied`
branches. All four branches of `mapAWSError` now produce
`errors.Is`-matchable sentinels (the default branch already wrapped
`ErrSecretStoreDown`).

Audit: no caller string-matched the prior `"aws secrets manager:
invalid request"` literal — `domain.ErrInvalidParams` (referenced from
`internal/repository/...`) is a separate HTTP-layer concept, not the
secrets sentinel.

Files:

- `internal/secrets/client.go` — declared `ErrInvalidRequest`; updated
  the `SecretClient.Get` doc comment to list the new sentinel.
- `internal/secrets/aws.go` — rewrote the invalid-request branch to
  `fmt.Errorf("%w: %s: %s: %s", ErrInvalidRequest, ref, code, msg)`.
- `internal/secrets/aws_test.go` — added two cases to
  `TestAWSClient_ErrorMapping`: `InvalidParameterException` and
  `InvalidRequestException`, each asserting `errors.Is` matches
  `ErrInvalidRequest` and not the adjacent sentinels.

Test gates:

- `go test ./internal/secrets/...`: green (the
  `TestFileClient_VersionChangesOnEdit` flake reproduces 5/5 on `main`
  too — pre-existing, addressed by TASK-026, not this task).
- Full unit suite: green with `-skip TestFileClient_VersionChangesOnEdit`.
- `make docker-lint`: 206 issues — same baseline as TASK-011/012.
- `gosec ./...`: 88 issues — same as `main` (verified via `git stash`
  test).
- `codex review --uncommitted`: two consecutive clean passes.
