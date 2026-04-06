---
id: TASK-005
type: Task
title: "Improve candidate discovery and blocking transition test coverage"
status: Pending
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-06
last_updated: 2026-04-06
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/tasks/TASK-004-fix-candidate-filters-and-deprecated-skills.md
---

# TASK-005 — Improve Candidate Discovery and Blocking Transition Test Coverage

---

## Purpose

Coverage gaps in engine layer:
- `FindExecutionCandidates` at 56% — actor_type and skills filter paths untested
- `extractAllowedActorTypes` at 0%
- `containsStr` at 0%
- `CheckAndEmitBlockingTransition` at 11% — dependent re-evaluation untested
- `updateExecutionProjection` at 17%

---

## Deliverable

1. Add tests to `engine/candidates_test.go`:
   - Filter by actor type (excludes mismatched candidates)
   - Filter by skills (excludes candidates requiring unmatched skills)
   - Combined actor_type + skills + include_blocked filters
   - Test extractAllowedActorTypes with valid and invalid metadata

2. Add tests to `engine/blocking_test.go`:
   - CheckAndEmitBlockingTransition with a dependent that becomes unblocked
   - CheckAndEmitBlockingTransition with a dependent that stays blocked (partial resolution)
   - updateExecutionProjection with a store that supports the interface

3. Target: FindExecutionCandidates > 80%, CheckAndEmitBlockingTransition > 60%

---

## Acceptance Criteria

- All filter paths in candidate discovery are tested
- Blocking transition re-evaluation tested with event emission verification
- Helper functions (extractAllowedActorTypes, containsStr) tested
- `go test -cover ./internal/engine/...` shows improvement
