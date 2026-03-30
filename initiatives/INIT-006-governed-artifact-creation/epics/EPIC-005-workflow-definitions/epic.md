---
id: EPIC-005
type: Epic
title: Workflow Definitions
status: Draft
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

Create lifecycle workflows that govern artifact creation through planning runs.

The first workflow is `initiative-lifecycle.yaml` which provides draft and review steps for initiative creation. This epic can run in parallel with implementation epics since workflows are YAML definitions with no code dependencies.

---

## 2. Scope

### In Scope

- `initiative-lifecycle.yaml` — draft → review → merge workflow for initiatives
- Workflow parse and validation tests

### Out of Scope

- Epic or task creation workflows (future — can be added later following the same pattern)
- Code changes

---

## 3. Success Criteria

1. `initiative-lifecycle.yaml` parses and validates correctly
2. Workflow applies to `Initiative` artifact type
3. Steps follow Spine workflow conventions (preconditions, outcomes, timeouts)
4. Added to existing workflow reference test suite

---

## 4. Primary Outputs

- `workflows/initiative-lifecycle.yaml`

---

## 5. Related Artefacts

- `workflows/epic-lifecycle.yaml` — pattern reference
- `workflows/adr.yaml` — pattern reference
- `architecture/workflow-definition-format.md`
