---
id: TASK-024
type: Task
title: "Delete dead code in workflow.binding and divergence.convergence"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-024 — Delete dead code in workflow.binding and divergence.convergence

---

## Purpose

Two dead-code sites flagged by the 2026-05-07 review:

- `internal/workflow/binding.go:65` — `_ = specificCandidates` blank
  assigns a non-trivial computed value with a "TODO when structured
  applies_to is implemented" comment.
- `internal/divergence/convergence.go:226` — `_ = evalRecord //
  stored in event payload` is a misleading no-op; the value is
  already used in the payload built two lines above.

This is a P3 cleanup finding from the 2026-05-07 code review.

## Deliverable

- Delete the `specificCandidates` computation entirely. If the TODO
  is genuinely warranted, file a follow-up task; do not leave the
  computed-then-discarded value in tree.
- Delete the `_ = evalRecord` line and its comment.
- Run `git grep "_ = "` across `internal/` and confirm any other hits
  are intentional (they typically are; only flag a finding if the
  blank obscures real logic).

## Acceptance Criteria

- `go build ./...` passes.
- Existing tests pass without modification.
- The two flagged blank-assigns are gone.

## Out of Scope

- A repo-wide blank-assign sweep.
