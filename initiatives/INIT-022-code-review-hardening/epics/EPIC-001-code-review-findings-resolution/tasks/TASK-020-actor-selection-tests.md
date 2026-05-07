---
id: TASK-020
type: Task
title: "Unit tests for actor selection strategies"
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

# TASK-020 — Unit tests for actor selection strategies

---

## Purpose

`internal/actor/service.go`, `selection.go`, `assignment.go`,
`prompt.go`, `ai_provider.go` lack direct unit-test coverage.
`actor/service.go:14`'s `validActorID` regex and `selection.go`'s
selection strategies (including `StrategyRoundRobin` with its
`sync.Mutex`) are critical for assignment fairness and have only
indirect coverage via gateway/engine tests.

This is a P2 coverage finding from the 2026-05-07 code review.

## Deliverable

- `internal/actor/service_test.go`: validActorID regex matrix
  (valid, invalid characters, length bounds, leading/trailing
  whitespace), CRUD happy paths against a stub store.
- `internal/actor/selection_test.go`:
  - For each strategy in the registry, a focused test exercising the
    happy path.
  - For `StrategyRoundRobin`, a test running 1000 picks across N
    actors and asserting fair distribution within tolerance.
  - Concurrency test: parallel `Pick` calls under race detector.
- `internal/actor/assignment_test.go`: minimal happy-path coverage of
  the assignment lifecycle if not already covered indirectly.

## Acceptance Criteria

- Tests pass without the `integration` tag.
- Round-robin determinism test fails if the mutex protecting the
  cursor is removed.

## Out of Scope

- AI-provider integration tests — separate concern, integration tag.
- prompt.go (mostly templating) unless trivial gaps surface.
