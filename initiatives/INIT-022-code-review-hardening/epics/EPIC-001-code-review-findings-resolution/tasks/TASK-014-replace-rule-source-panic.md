---
id: TASK-014
type: Task
title: "Replace branchprotect rule_source panic with returned error"
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

# TASK-014 — Replace branchprotect rule_source panic with returned error

---

## Purpose

`internal/branchprotect/projection/rule_source.go:43` panics on a nil
constructor argument. The panic is reachable from production wiring:
a missed nil-check in `cmd/spine` becomes a process kill at startup
or later under platform-binding load.

This is a P2 error-handling finding from the 2026-05-07 code review.

## Deliverable

- Change the constructor (or guarded helper) to return an error rather
  than panic on the nil-arg branch.
- Update the one or two callers in `cmd/spine` and any wiring helper
  to surface the error (typically via `cobra` command-init failure or
  `SPINE_ENV=production` strict-startup).
- Confirm there is no remaining production-reachable panic in the
  package via `git grep "panic(" internal/branchprotect/`.

## Acceptance Criteria

- A unit test passes a nil arg to the constructor and asserts the
  returned error is non-nil and matches the expected sentinel
  (`ErrInvalidParams` is a fine choice).
- `go build ./...` passes; existing wiring continues to work.

## Out of Scope

- Other panics in the codebase. The CIDR-init panic in
  `internal/delivery/targeturl.go:212` is package-init only and
  defensible.
