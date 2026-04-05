---
id: TASK-008
type: Task
title: "Propagate skill lookup failures in actor selection"
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

# TASK-008 — Propagate Skill Lookup Failures in Actor Selection

---

## Purpose

`actorHasCapabilities` in `selection.go` returns `false` when `ListActorSkills` fails (e.g. transient DB error). This misclassifies storage failures as permanent skill mismatches, making assignment failures hard to diagnose.

Found during Codex review (P2).

---

## Deliverable

1. Change `actorHasCapabilities` to return `(bool, error)` and propagate DB errors
2. Update `filterActorsWithSkills` to handle the error — log a warning and skip the actor (or propagate)
3. Ensure transient DB failures don't silently produce "no eligible actor" errors

---

## Acceptance Criteria

- DB errors in skill lookup are logged with context
- Transient failures do not silently look like skill mismatches
- Tests cover the error propagation path
