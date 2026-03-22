---
id: TASK-001
type: Task
title: Cross-Artifact Validation Rules
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-006-validation-service/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-006-validation-service/epic.md
---

# TASK-001 — Cross-Artifact Validation Rules

## Purpose

Implement all validation rules from Validation Service Specification §3.

## Deliverable

- Structural integrity rules (SI-001 through SI-005)
- Link consistency rules (LC-001 through LC-005)
- Status consistency rules (SC-001 through SC-005)
- Scope alignment rules (SA-001, SA-002)
- Prerequisite completeness rules (PC-001 through PC-003)
- Structured validation result with rule_id, classification, message, artifact_path
- Rule engine that evaluates applicable rules for a given artifact

## Acceptance Criteria

- All 15 rules produce correct pass/fail results
- Each rule includes a unique rule_id in the result
- Error vs warning severity matches the specification
- Rules read from Projection Store (not Git directly)
- Unit tests for every rule with fixture artifacts
- Integration tests with real artifact hierarchies
