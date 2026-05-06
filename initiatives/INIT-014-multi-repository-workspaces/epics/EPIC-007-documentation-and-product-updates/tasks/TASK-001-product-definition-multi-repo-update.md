---
id: TASK-001
type: Task
title: Update product definition for multi-repo workspaces
status: Completed
acceptance: Approved
acceptance_rationale: |
  /product/product-definition.md now describes multi-repo workspaces
  as a first-class capability: §5.6 introduces the primary
  (kind: spine) vs code (kind: code) repository distinction, §5.7
  walks through multi-repo run lifecycle (branches per affected
  repo, per-repo merge ordering, partially-merged surfacing through
  run inspect), §5.8 makes the constraints explicit (no cross-repo
  atomic transactions, governance only in primary, workspace-level
  RBAC, isolation extends to code repos), and §6 walks the
  payments-platform polyrepo use case end to end (api-gateway,
  payments-service, notification-service) covering setup, intent,
  execution, convergence, and partial-merge handling. Single-repo
  workflows remain documented as the default. Terminology matches
  architecture (kind, partially-merged RunStatus,
  spine/run/<artifact-id>-<slug>-<run-hex> branch naming,
  RepositoryMergeStatus + failure_class). Architecture §4.5
  (Branch Cleanup) was aligned with actual code paths during review.
  Codex 2 passes clean back-to-back on the 7th and 8th attempts —
  high pass count for a doc-only PR because the new normative
  walkthrough surfaced multiple latent doc-vs-code drifts that
  needed to be reconciled inside the same PR.
last_updated: 2026-05-06
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
