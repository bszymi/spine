---
id: TASK-004
type: Task
title: "Improve ClaimStep and ReleaseStep unit test coverage"
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
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-003-assignment-claiming/tasks/TASK-003-fix-claim-release-bugs.md
---

# TASK-004 — Improve ClaimStep and ReleaseStep Unit Test Coverage

---

## Purpose

ClaimStep is at 42% coverage and several critical paths are untested:
- Skill validation during claim (actor lacks required skills)
- Atomic claim conflict (assignment insert fails)
- loadActor (0% — actor type eligibility path)
- Execution projection update after claim and release

---

## Deliverable

1. Add tests to `engine/claim_test.go`:
   - Claim fails when actor lacks required skills
   - Claim fails on concurrent conflict (assignment already exists)
   - Claim with actor type validation (eligible + ineligible types)
   - Verify execution projection is updated after successful claim

2. Add tests to `engine/release_test.go`:
   - Verify execution projection is updated after release (assignment cleared)

3. Target: ClaimStep > 75%, ReleaseStep > 85%

---

## Acceptance Criteria

- All critical ClaimStep paths tested (skill check, atomicity, actor type)
- ReleaseStep projection update tested
- `go test -cover ./internal/engine/...` shows improvement
