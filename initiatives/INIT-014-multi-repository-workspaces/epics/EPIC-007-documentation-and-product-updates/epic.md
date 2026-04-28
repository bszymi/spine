---
id: EPIC-007
type: Epic
title: "Documentation and Product Updates"
status: Pending
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
owner: bszymi
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
---

# EPIC-007 - Documentation and Product Updates

---

## Purpose

Bring the product definition, architecture documentation, and operator-facing guides in line with multi-repository support so that initiative Success Criterion #7 has a clear owner.

This epic intentionally lands last — every prior epic delivers behavior that the docs need to describe accurately. Docs written earlier would either lie about unfinished behavior or churn through every implementation epic.

---

## Scope

### In Scope

- Update `/product/product-definition.md` to describe multi-repo workspaces, primary vs code repos, and the spine-repo-as-ledger model.
- Update `/architecture/git-integration.md` and `/architecture/multi-repository-integration.md` so single-repo assumptions are removed where they no longer apply.
- Update operator/runbook docs covering registration, partial-merge recovery, and credential handling.
- Reconcile diagrams and examples with the final routing/merge/evidence behavior.

### Out of Scope

- Marketing or external-facing collateral.
- Migration tooling docs (none ships in this initiative).
- Runner-image documentation (lives with INIT-009).

---

## Primary Outputs

- Updated product definition reflecting multi-repo as a first-class feature.
- Updated architecture docs with consistent terminology across components.
- Operator docs for repository registration, manual resolution, and credential rotation.

---

## Acceptance Criteria

1. Product definition explicitly describes multi-repo workspaces and includes at least one polyrepo use case end to end.
2. Architecture docs no longer assume a single repository per workspace where multi-repo behavior is shipped.
3. Operator docs cover the full lifecycle: register, run, partial-merge recovery, deregister.
4. Cross-references between docs and ADRs (TASK-001/TASK-007 ADRs from EPIC-001/006, TASK-007 ADR from EPIC-004) are valid.
5. No doc references unimplemented behavior.
