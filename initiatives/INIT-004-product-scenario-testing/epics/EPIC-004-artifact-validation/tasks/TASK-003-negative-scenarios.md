---
id: TASK-003
type: Task
title: "Negative Artifact Scenarios"
status: Completed
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-004-artifact-validation/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-004-artifact-validation/epic.md
---

# TASK-003 — Negative Artifact Scenarios

---

## Purpose

Implement negative scenarios that validate the system correctly rejects invalid artifacts: missing parent references, invalid frontmatter, broken links, and schema violations.

## Deliverable

Scenario test suite covering:

- Task without parent Epic is rejected
- Epic without parent Initiative is rejected
- Artifact with invalid frontmatter (missing required fields) is rejected
- Artifact with broken links (referencing non-existent targets) is detected
- Duplicate IDs are rejected

## Acceptance Criteria

- Each invalid artifact creation attempt is rejected with a clear error
- Error messages identify the specific validation failure
- System state remains clean after rejection (no partial artifacts)
- All Constitution-mandated constraints are covered by negative scenarios
