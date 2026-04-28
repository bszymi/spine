---
id: TASK-001
type: Task
title: Update product definition for multi-repo workspaces
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: documentation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/epic.md
---

# TASK-001 - Update Product Definition for Multi-Repo Workspaces

---

## Purpose

Describe multi-repository workspaces as a first-class product capability so that the product definition matches what Spine actually ships.

## Deliverable

Update `/product/product-definition.md` to:

- Introduce the primary vs code repository distinction.
- Add a polyrepo use case (e.g. payments platform with `api-gateway`, `payments-service`, `notification-service`) walked through end to end.
- Describe how runs span repositories and what users see when a run is in partial-merge state.
- Note the constraints (no cross-repo atomic transactions, workspace-level RBAC).

## Acceptance Criteria

- Product definition includes at least one polyrepo use case described end to end.
- Single-repo workflows remain documented as the default.
- Terminology matches the architecture docs.
- No marketing fluff added; the doc remains technical and decision-grade.
