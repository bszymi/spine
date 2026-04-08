---
id: TASK-001
type: Task
title: Implement discovery-based validation step
status: Pending
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-003-branch-scoped-validation/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: implementation
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-003-branch-scoped-validation/epic.md
---

# TASK-001 — Implement Discovery-Based Validation Step

---

## Purpose

Replace single-artifact validation in the `artifact-creation` workflow with branch-scoped discovery-based validation that finds and validates all artifacts on the planning run's branch.

---

## Deliverable

Modify the automated validation step handler (in `internal/engine/step.go` or a new `internal/engine/branch_validation.go`).

### Current behavior

The validation step validates the single artifact that the planning run was started for.

### New behavior

When the validation step executes for a planning run:

1. Get the run's branch ref
2. Call `DiscoverChanges(ctx, gitClient, "main", branchRef)` to find all new artifacts
3. For each discovered artifact:
   - Run individual validation (schema, required fields, ID format, status)
   - Collect results
4. Run cross-artifact validation across the full set:
   - Parent links resolve (target exists on branch or on main)
   - No duplicate IDs within the same scope
   - Artifact types match their parent expectations (tasks under epics, epics under initiatives)
5. Aggregate results:
   - If all pass: step outcome = `passed`
   - If any fail: step outcome = `failed`, include details for every failing artifact

### Validation detail format

```json
{
  "total_artifacts": 4,
  "passed": 3,
  "failed": 1,
  "details": [
    {
      "path": "initiatives/.../TASK-003-missing-link.md",
      "errors": ["required field 'epic' is missing"]
    }
  ]
}
```

---

## Acceptance Criteria

- Validation discovers all artifacts on the branch via `DiscoverChanges`
- Each artifact is individually validated
- Cross-artifact constraints are checked across the full branch set
- Parent links that point to artifacts on the same branch are valid (not just main)
- Validation failure includes per-artifact error details
- An empty branch (no new artifacts) is a validation error
