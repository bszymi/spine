---
id: TASK-001
type: Task
title: Create adr-creation.yaml workflow
status: Completed
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-004-per-type-creation-workflows/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: implementation
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-004-per-type-creation-workflows/epic.md
---

# TASK-001 — Create adr-creation.yaml Workflow

---

## Purpose

Define a creation workflow for ADRs that governs the process from initial proposal through architecture review to acceptance.

ADRs have a distinct lifecycle (Proposed → Accepted) and need a review step oriented around architectural decision-making, not general artifact review.

---

## Deliverable

`workflows/adr-creation.yaml`

```yaml
id: adr-creation
name: ADR Creation
version: "1.0"
status: Active
description: >
  Governs creation of Architectural Decision Records.
  Covers the Proposed → Accepted lifecycle with architecture review.
mode: creation

applies_to:
  - ADR

entry_step: draft

steps:
  - id: draft
    name: Draft ADR
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types: [human, ai_agent]
      required_skills: [architecture]
    description: >
      Draft the ADR content: context, decision, consequences.
      Additional supporting artifacts can be added to the branch.
    outcomes:
      - id: ready_for_review
        name: Ready for Review
        next_step: validate

  - id: validate
    name: Validate ADR
    type: automated
    execution:
      mode: automated_only
      eligible_actor_types: [automated_system]
    description: >
      Validate ADR schema, required sections, and cross-artifact links.
    outcomes:
      - id: valid
        name: Validation Passed
        next_step: review
      - id: invalid
        name: Validation Failed
        next_step: draft
    timeout: "5m"
    timeout_outcome: valid

  - id: review
    name: Architecture Review
    type: review
    execution:
      mode: human_only
      eligible_actor_types: [human]
      required_skills: [architecture, review]
    description: >
      Architecture review of the decision. Reviewer evaluates
      context, alternatives considered, and consequences.
    outcomes:
      - id: accepted
        name: Accepted
        next_step: end
        commit:
          status: Accepted
      - id: needs_revision
        name: Needs Revision
        next_step: draft
      - id: rejected
        name: Rejected
        next_step: end
        commit:
          status: Deprecated
    timeout: "168h"
    timeout_outcome: needs_revision
```

Key differences from `artifact-creation.yaml`:
- Initial status is Proposed (not Draft)
- Terminal status is Accepted (not Pending)
- Rejection produces Deprecated status
- Review step requires `architecture` skill
- Longer review timeout (7 days vs 3 days) — architectural decisions need broader input

---

## Acceptance Criteria

- Workflow YAML is valid and parseable
- `(ADR, creation)` resolves to `adr-creation` via binding resolver
- No binding conflict with existing `adr.yaml` (which is mode: execution)
- Review step requires architecture skill
- Accepted outcome sets status to Accepted
- Rejection sets status to Deprecated
