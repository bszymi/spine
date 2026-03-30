---
id: TASK-001
type: Task
title: Create artifact-creation.yaml workflow
status: Completed
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
---

# TASK-001 — Create artifact-creation.yaml Workflow

---

## Purpose

Define the generic workflow that governs artifact creation through planning runs. One workflow handles creation of all governed artifact types — no per-type creation workflows needed.

---

## Deliverable

`workflows/artifact-creation.yaml`

Workflow structure:

```yaml
id: artifact-creation
mode: creation
applies_to:
  - Initiative
  - Epic
  - Task

entry_step: draft

steps:
  - id: draft
    name: Draft Artifact
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types: [human, ai_agent]
    preconditions:
      - type: artifact_status
        config:
          status: Draft
    required_outputs:
      - artifact_content
    outcomes:
      - id: ready_for_review
        name: Ready for Review
        next_step: validate

  - id: validate
    name: Validate Artifact
    type: automated
    execution:
      mode: automated_only
      eligible_actor_types: [automated_system]
    outcomes:
      - id: valid
        name: Validation Passed
        next_step: review
      - id: invalid
        name: Validation Failed
        next_step: draft
    retry:
      limit: 2
      backoff: fixed
    timeout: "5m"
    timeout_outcome: valid

  - id: review
    name: Review Artifact
    type: review
    execution:
      mode: human_only
      eligible_actor_types: [human]
    outcomes:
      - id: approved
        name: Approved
        next_step: end
        commit:
          status: Pending
      - id: needs_revision
        name: Needs Revision
        next_step: draft
    timeout: "72h"
    timeout_outcome: needs_revision
```

Key design points:

- `mode: creation` — distinguishes this from execution workflows. Planning runs resolve to this workflow.
- `applies_to` — covers Initiative, Epic, and Task. These types share the `Draft → Pending` lifecycle. Product and ADR are excluded because their status models differ (`Living Document`/`Stable` for Product, `Proposed`/`Accepted` for ADR). Type-specific creation workflows for Product and ADR can be added later.
- **draft** step — author creates/refines the artifact and any child artifacts on the branch.
- **validate** step — automated cross-artifact validation using the existing validation service. Checks structural integrity, parent references, schema compliance. Fails back to draft on validation errors.
- **review** step — human reviewer verifies alignment with governance, product definition, and architecture. Approval sets status to `Pending` (ready for execution workflows).
- On approval, the branch merges to main via existing `MergeRunBranch()` infrastructure.

---

## Acceptance Criteria

- Workflow follows the format in `architecture/workflow-definition-format.md`
- Workflow includes the `mode: creation` field
- Workflow parses correctly by Spine's workflow parser
- Steps have appropriate timeouts
- Validate step uses automated execution mode
- Review step requires human actor
- Approved outcome sets artifact status to `Pending`
- All steps with `timeout` also have `timeout_outcome`
- Product and ADR are excluded (incompatible status models) — documented as future enhancement
