---
id: TASK-002
type: Task
title: Create document-creation.yaml workflow
status: Draft
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-004-per-type-creation-workflows/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: implementation
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-004-per-type-creation-workflows/epic.md
---

# TASK-002 — Create document-creation.yaml Workflow

---

## Purpose

Define a shared creation workflow for Governance, Architecture, and Product documents. These three types share the same status model (Living Document → Stable/Foundational) and the same creation pattern.

---

## Deliverable

`workflows/document-creation.yaml`

```yaml
id: document-creation
name: Document Creation
version: "1.0"
status: Active
description: >
  Governs creation of Governance, Architecture, and Product documents.
  These share the Living Document initial status and a review-based
  approval process.
mode: creation

applies_to:
  - Governance
  - Architecture
  - Product

entry_step: draft

steps:
  - id: draft
    name: Draft Document
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types: [human, ai_agent]
      required_skills: [planning]
    description: >
      Draft the document content. Additional related documents
      can be added to the branch.
    outcomes:
      - id: ready_for_review
        name: Ready for Review
        next_step: validate

  - id: validate
    name: Validate Document
    type: automated
    execution:
      mode: automated_only
      eligible_actor_types: [automated_system]
    description: >
      Validate document schema, required front-matter fields,
      and cross-artifact references.
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
    name: Review Document
    type: review
    execution:
      mode: human_only
      eligible_actor_types: [human]
      required_skills: [review]
    description: >
      Review the document for accuracy, completeness, and
      alignment with existing governance/architecture/product docs.
    outcomes:
      - id: approved
        name: Approved
        next_step: end
        commit:
          status: Living Document
      - id: needs_revision
        name: Needs Revision
        next_step: draft
    timeout: "72h"
    timeout_outcome: needs_revision
```

Key characteristics:
- Applies to three types: Governance, Architecture, Product
- Initial status on the branch is Living Document (the artifact is born as a living doc)
- Approved outcome commits with status Living Document
- No "rejected closed" — documents are either revised or abandoned (cancellation)

---

## Acceptance Criteria

- Workflow YAML is valid and parseable
- `(Governance, creation)` → `document-creation`
- `(Architecture, creation)` → `document-creation`
- `(Product, creation)` → `document-creation`
- No binding conflicts with any existing workflow
- All three types share this single workflow
