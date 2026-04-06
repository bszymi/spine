---
id: TASK-003
type: Task
title: "Fix claim/release bugs: nil-check, skill validation, atomicity"
status: Completed
completed: 2026-04-06
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-003-assignment-claiming/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-06
last_updated: 2026-04-06
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-003-assignment-claiming/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-003-assignment-claiming/tasks/TASK-001-task-claim-operation.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-003-assignment-claiming/tasks/TASK-002-task-release-operation.md
---

# TASK-003 — Fix Claim/Release Bugs: Nil-Check, Skill Validation, Atomicity

---

## Purpose

Three bugs found during final review of EPIC-003:

1. **P1 — Wrong nil-check in release handler**: `handlers_release.go:14` checks `s.stepClaimer` instead of `s.stepReleaser`. Release endpoint fails when claim is unconfigured, or proceeds when release is nil.

2. **P1 — ClaimStep doesn't validate skills**: Only checks `eligible_actor_types`, never checks the actor's assigned skills against `execution.required_skills`. An actor of the right type but without required skills can claim steps they're not qualified for.

3. **P1 — ClaimStep is not atomic**: Uses read-then-write pattern (`GetStepExecution` + status check + `UpdateStepExecution`). Two concurrent callers can both see `waiting` and both succeed, creating duplicate assignments.

---

## Deliverable

1. Fix `handlers_release.go:14`: change `s.stepClaimer` to `s.stepReleaser`

2. Add skill validation to `ClaimStep`:
   - After actor type check, load actor's skills via store
   - Compare against `stepDef.Execution.RequiredSkills`
   - Return descriptive error if skills are missing

3. Make `ClaimStep` atomic:
   - Use a conditional UPDATE: `UPDATE step_executions SET status = 'assigned', actor_id = $1 WHERE execution_id = $2 AND status = 'waiting'`
   - Check rows affected — if 0, another actor already claimed (return conflict error)
   - Remove the separate GET + status check + UPDATE pattern

4. Tests:
   - Test release handler with nil stepReleaser
   - Test claim fails when actor lacks required skills
   - Test concurrent claim scenario (one wins, one gets conflict)

---

## Acceptance Criteria

- Release handler checks correct nil field
- ClaimStep validates actor skills against step requirements
- Concurrent claims are safe — exactly one succeeds
- Tests cover all three fix paths
