---
id: TASK-013
type: Task
title: "Wrap AWS Secrets Manager errors with %w"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-07
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
