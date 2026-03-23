---
id: EPIC-006
type: Epic
title: Validation Integration
status: Pending
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/initiative.md
---

# EPIC-006 — Validation Integration

---

## Purpose

Enforce cross-artifact integrity during workflow execution. The validation engine exists (15 rules) but is not wired into workflow progression. This epic makes validation a gate that blocks invalid execution.

---

## Key Work Areas

- Wire validation service into workflow step preconditions
- Implement `cross_artifact_valid` condition type
- Block step progression when validation fails
- Surface validation results in run state and API responses

---

## Primary Outputs

- Validation precondition evaluation in engine orchestrator
- `cross_artifact_valid` condition implementation
- Validation failure reporting in run/step status

---

## Acceptance Criteria

- Workflow steps with `cross_artifact_valid` preconditions invoke the validation engine
- Validation failures block step activation
- Validation results are available in run status queries
- Validation errors include classification (scope conflict, architectural, implementation drift, missing prerequisite)
