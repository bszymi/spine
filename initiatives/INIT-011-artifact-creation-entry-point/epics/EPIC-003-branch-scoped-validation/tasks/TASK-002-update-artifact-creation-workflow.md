---
id: TASK-002
type: Task
title: Update artifact-creation workflow for branch-scoped validation
status: Draft
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-003-branch-scoped-validation/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: implementation
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-003-branch-scoped-validation/epic.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-003-branch-scoped-validation/tasks/TASK-001-discovery-based-validation-step.md
---

# TASK-002 — Update Artifact-Creation Workflow for Branch-Scoped Validation

---

## Purpose

Update `workflows/artifact-creation.yaml` to reflect the new branch-scoped validation behavior and make the draft step explicitly open-ended (actor can add multiple artifacts before submitting).

---

## Deliverable

Modify `workflows/artifact-creation.yaml`.

### Changes

1. **Draft step**: clarify that the actor can add artifacts incrementally (via API or direct file writes) before submitting
2. **Validation step**: update description to reflect that it discovers and validates all branch artifacts, not just the initial one
3. **Validation failure**: loops back to draft step so the actor can fix issues across any of the branch artifacts

### Updated workflow structure

```yaml
steps:
  - id: draft
    name: Draft Artifacts
    type: manual
    execution:
      mode: hybrid
    description: >
      Create and refine artifacts on the planning branch.
      Additional artifacts can be added via POST /artifacts/add
      or by writing files directly to the branch.
      Submit when all artifacts are ready for validation.
    outcomes:
      - id: ready_for_validation
        name: Ready for Validation
        next_step: validate

  - id: validate
    name: Validate Branch Artifacts
    type: automated
    execution:
      mode: automated_only
    description: >
      Discover all new artifacts on the branch (diff against main).
      Validate each individually and run cross-artifact checks.
    outcomes:
      - id: passed
        name: Validation Passed
        next_step: review
      - id: failed
        name: Validation Failed
        next_step: draft

  - id: review
    # ... existing review step unchanged
```

---

## Acceptance Criteria

- Workflow YAML is valid and parseable
- Draft step description reflects multi-artifact support
- Validation step description reflects branch-scoped discovery
- Validation failure loops back to draft
- Existing workflow tests still pass
- No binding conflicts with other workflows
