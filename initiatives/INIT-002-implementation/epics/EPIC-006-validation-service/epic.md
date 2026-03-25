---
id: EPIC-006
type: Epic
title: Validation Service
status: Completed
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-003-projection-service/epic.md
---

# EPIC-006 — Validation Service

---

## Purpose

Build the Validation Service — the component that performs cross-artifact consistency checks during workflow execution. After this epic, governance validation is automated and enforceable.

---

## Validates

- [Validation Service Specification](/architecture/validation-service.md) — Rules, classifications, contract
- [Workflow Validation](/architecture/workflow-validation.md) — Workflow-specific validation
- [Constitution](/governance/constitution.md) §11 — Cross-Artifact Validation

---

## Acceptance Criteria

- All 15 validation rules from validation-service.md §3 are implemented
- Mismatch classification produces correct categories
- `cross_artifact_valid` precondition integration with Workflow Engine works
- `system.validate_all` scans all artifacts and produces a structured report
- Validation reads from Projection Store for performance
- Validation results include rule_id, artifact_path, and severity
- Unit tests for every rule; integration tests for cross-artifact scenarios
