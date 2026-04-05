---
id: TASK-003
type: Task
title: "Upgrade required_capabilities to resolve against skill registry"
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
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-001-define-skill-domain-model.md
---

# TASK-003 — Upgrade required_capabilities to Resolve Against Skill Registry

---

## Purpose

Upgrade the existing `execution.required_capabilities` field on workflow steps to resolve against the skill registry instead of being opaque strings. This is a refinement of the existing schema, not a new field.

---

## Deliverable

1. Keep `required_capabilities` in the existing `execution` block — no new field location:
   ```yaml
   steps:
     execute:
       execution:
         required_capabilities:
           - artifact_validation
           - backend_development
   ```

2. Update the capability matching pipeline to resolve `required_capabilities` values against the skill registry:
   - If a skill entity with that name exists, use it for matching
   - If no skill entity exists, fall back to bare string matching (backward compatibility)

3. Update workflow validation to warn when a `required_capabilities` value does not match any registered skill

4. No schema shape change — `required_capabilities` stays where it is, under `execution`

5. Update documentation:
   - Update `/architecture/workflow-definition-format.md` to describe `required_capabilities` resolution against the skill registry
   - Update `/architecture/workflow-validation.md` to document the new validation warning for unregistered capabilities
   - Update `/architecture/actor-model.md` to describe how capability matching now resolves through skills

---

## Acceptance Criteria

- `required_capabilities` values resolve against the skill registry when skills exist
- Bare string matching continues to work when no matching skill entity exists
- Workflow validation warns on unregistered capability names
- No change to workflow YAML schema shape — field stays under `execution`
- Reference workflow definitions are updated with examples
- Architecture documentation is updated to reflect skill-backed capability resolution
