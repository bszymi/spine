---
id: TASK-011
type: Task
title: "Require at least one skill on actor-assigned workflow steps"
status: Completed
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-05
completed: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-010-rename-capabilities-to-skills.md
---

# TASK-011 — Require At Least One Skill on Actor-Assigned Workflow Steps

---

## Purpose

Without required skills on a step, the system cannot meaningfully select an actor to perform the work. Steps with execution modes that involve actor assignment (hybrid, human_only, ai_only) must declare at least one required skill. Automated-only steps are exempt.

---

## Deliverable

1. Add schema validation rule: steps with non-automated execution mode must have at least one `required_skills` entry
2. Update all reference workflow YAML files with appropriate `required_skills`
3. Add tests for the new validation rule
4. Update workflow-validation.md §3.5

---

## Acceptance Criteria

- Schema validation fails if an actor-assigned step has no required_skills
- Automated-only steps pass validation without required_skills
- All reference workflow definitions include required_skills
- All tests pass
