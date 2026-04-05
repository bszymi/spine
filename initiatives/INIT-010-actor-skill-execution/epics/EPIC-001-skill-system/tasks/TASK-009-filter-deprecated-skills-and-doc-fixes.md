---
id: TASK-009
type: Task
title: "Filter deprecated skills from eligibility and fix stale documentation"
status: Completed
completed: 2026-04-05
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
---

# TASK-009 — Filter Deprecated Skills from Eligibility and Fix Stale Documentation

---

## Purpose

1. `ValidateSkillEligibility` and `actorHasCapabilities` include deprecated skills when computing eligibility, but `ValidateCapabilities` (workflow validation) excludes them. This inconsistency means a deprecated skill satisfies assignment but triggers a validation warning.

2. `actor-model.md` still describes legacy capabilities fallback that no longer exists after TASK-006.

3. `ListSkills(actorID)` on the actor service does not validate non-empty actorID.

Found during Codex review (P2) and manual audit.

---

## Deliverable

1. Filter out deprecated skills in `actorHasCapabilities` and `ValidateSkillEligibility`
2. Update `ListActorSkills` query or filter to exclude deprecated skills from eligibility matching
3. Add actorID validation to `actor.Service.ListSkills()`
4. Update `actor-model.md` to remove references to legacy capabilities fallback
5. Add test for deprecated skill exclusion

---

## Acceptance Criteria

- Deprecated skills do not satisfy `required_capabilities` during assignment
- Behavior is consistent between workflow validation and actor selection
- `ListSkills("")` returns an error instead of empty results
- `actor-model.md` accurately describes current skill-only system
- Tests cover deprecated skill exclusion
