---
id: TASK-004
type: Task
title: Tests for per-type creation workflows
status: Draft
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-004-per-type-creation-workflows/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: testing
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-004-per-type-creation-workflows/epic.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-004-per-type-creation-workflows/tasks/TASK-003-path-building-for-non-hierarchical-types.md
---

# TASK-004 — Tests for Per-Type Creation Workflows

---

## Purpose

Validate that all artifact types can be created through their respective creation workflows.

---

## Deliverable

### Workflow definition tests

`workflows/reference_workflows_test.go` (extend)

- `adr-creation.yaml` parses correctly
- `document-creation.yaml` parses correctly
- No binding conflicts: `TestNoBindingConflicts` updated to include new workflows
- Binding resolution:
  - `(ADR, creation)` → `adr-creation`
  - `(Governance, creation)` → `document-creation`
  - `(Architecture, creation)` → `document-creation`
  - `(Product, creation)` → `document-creation`

### Path building tests

`internal/artifact/id_allocator_test.go` (extend)

- `NextADRID`: empty dir → ADR-0001, existing ADR-0006 → ADR-0007
- `NextADRID`: 4-digit zero padding
- `BuildDocumentPath`: Governance → `governance/slug.md`
- `BuildDocumentPath`: Architecture → `architecture/slug.md`
- `BuildDocumentPath`: Product → `product/slug.md`
- Duplicate slug detection for documents

### Scenario tests

`internal/scenariotest/scenarios/per_type_creation_test.go`

1. **ADR creation**
   - `spine artifact create --type ADR --title "Use event sourcing"`
   - Verify: ADR-XXXX allocated, branch created, `adr-creation` workflow resolved
   - Progress: draft → validate → architecture review → accepted
   - Verify: ADR on main with status Accepted

2. **Governance document creation**
   - `spine artifact create --type Governance --title "API standards"`
   - Verify: `governance/api-standards.md` path, `document-creation` workflow
   - Progress: draft → validate → review → approved
   - Verify: document on main with status Living Document

3. **Architecture document creation**
   - Same flow as governance but in `/architecture/`

4. **Product document creation**
   - Same flow as governance but in `/product/`

5. **Duplicate slug rejection**
   - Create `governance/api-standards.md`
   - Try to create another Governance doc with the same title
   - Verify: returns error (duplicate path)

---

## Acceptance Criteria

- All workflow definition tests pass
- All path building tests pass
- All five scenario tests pass
- No binding conflicts across all workflows
