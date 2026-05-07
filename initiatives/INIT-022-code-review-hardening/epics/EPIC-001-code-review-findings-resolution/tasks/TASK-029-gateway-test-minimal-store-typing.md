---
id: TASK-029
type: Task
title: "Per-handler typed minimal-store for gateway tests"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-010-split-store-interface.md
---

# TASK-029 — Per-handler typed minimal-store for gateway tests

---

## Purpose

`internal/gateway/handlers_tokens_test.go:21-24` (and similar tests in
the package) embed the `store.Store` interface directly so unimplemented
methods panic at runtime — a refactor that touches an ancillary store
call in the same handler will break tests in surprising ways instead
of failing the typecheck.

This is a P3 test-quality finding from the 2026-05-07 code review.

## Deliverable

- After TASK-010 lands the role-interface split, migrate gateway
  handler tests so each test takes the narrowest role interface its
  handler under test actually depends on (e.g. `AuthStore` for
  token tests).
- Replace the `store.Store`-embedded stubs with typed minimal stubs
  (one per role-interface, ideally generated or hand-coded once).
- This change might land alongside TASK-010 or as its follow-up
  cleanup PR.

## Acceptance Criteria

- All gateway tests pass.
- Refactoring an unrelated `store.Store` method (renaming a parameter,
  for example) only breaks the tests that exercise that method, not
  every gateway test in the package.

## Out of Scope

- Until TASK-010 lands, this task remains blocked on the role-interface
  partition. If TASK-010 is descoped, this task is descoped as well.
