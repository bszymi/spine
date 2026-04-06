---
id: TASK-004
type: Task
title: "Apply candidate filters and exclude deprecated skills from eligibility queries"
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
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/tasks/TASK-002-execution-candidate-discovery-api.md
---

# TASK-004 — Apply Candidate Filters and Exclude Deprecated Skills from Eligibility Queries

---

## Purpose

Two bugs found during final review:

1. **P2 — FindExecutionCandidates ignores ActorType and Skills filters**: The implementation only filters by `IncludeBlocked`. A request like `?actor_type=human&skills=review` returns all tasks regardless of actor type or skill requirements.

2. **P2 — ListActorsBySkills SQL doesn't filter deprecated skills**: The query joins on `s.name` without checking `s.status = 'active'`. Actors with only deprecated matching skills are incorrectly reported as eligible.

---

## Deliverable

1. Apply `ActorType` and `Skills` filters in `FindExecutionCandidates`:
   - If `ActorType` is set, exclude candidates whose workflow step doesn't allow that actor type
   - If `Skills` is set, exclude candidates whose required skills don't match the provided skills

2. Add `AND s.status = 'active'` to `ListActorsBySkills` SQL query

3. Tests:
   - Test candidate filtering by actor type
   - Test candidate filtering by skills
   - Test that deprecated skills don't produce eligible actors

---

## Acceptance Criteria

- Candidates endpoint respects actor_type and skills filters
- ListActorsBySkills only matches active skills
- Behavior is consistent across all eligibility paths
