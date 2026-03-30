---
id: EPIC-005
type: Epic
title: Workflow Definitions
status: Completed
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
owner: bszymi
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/initiative.md
---

# EPIC-005 — Workflow Definitions

---

## 1. Purpose

Create the generic artifact creation workflow and extend the workflow definition format with a `mode` field to distinguish creation workflows from execution workflows.

One workflow (`artifact-creation.yaml`) governs creation of artifact types that share the `Draft → Pending` lifecycle — Initiative, Epic, and Task. Product and ADR are excluded because their status models differ; type-specific creation workflows for those can be added later.

---

## 2. Scope

### In Scope

- `artifact-creation.yaml` — generic workflow: draft → validate → review → merge
- Workflow `mode` field (`execution` / `creation`) added to the workflow definition format
- Workflow parser updated to read the `mode` field
- Workflow binding resolution updated for planning runs (filter by `mode: creation`)
- Workflow parse and validation tests

### Out of Scope

- Changes to existing execution workflows (task-default, epic-lifecycle, adr)
- AI-assisted validation (future enhancement)

---

## 3. Success Criteria

1. `artifact-creation.yaml` parses and validates correctly
2. Workflow applies to Initiative, Epic, and Task (types sharing the Draft → Pending lifecycle)
3. The `mode` field is recognized by the workflow parser
4. Planning runs resolve to the creation workflow, standard runs resolve to execution workflows
5. No binding conflict between `artifact-creation.yaml` and existing type-specific execution workflows
6. Added to existing workflow reference test suite

---

## 4. Primary Outputs

- `workflows/artifact-creation.yaml`
- Updated workflow parser (`internal/workflow/`)
- Updated workflow binding resolution

---

## 5. Related Artifacts

- `workflows/epic-lifecycle.yaml` — pattern reference
- `workflows/adr.yaml` — pattern reference
- `architecture/workflow-definition-format.md`
