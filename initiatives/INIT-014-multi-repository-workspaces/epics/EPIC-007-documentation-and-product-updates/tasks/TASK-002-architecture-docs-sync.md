---
id: TASK-002
type: Task
title: Sync architecture docs with shipped multi-repo behavior
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: documentation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/tasks/TASK-001-product-definition-multi-repo-update.md
---

# TASK-002 - Sync Architecture Docs with Shipped Multi-Repo Behavior

---

## Purpose

Reconcile `/architecture/git-integration.md`, `/architecture/multi-repository-integration.md`, and adjacent component docs with the behavior actually delivered across EPIC-001 through EPIC-006.

## Deliverable

Update architecture documentation so:

- Single-repo assumptions are removed wherever multi-repo is now the default behavior.
- The git client pool, lazy clone, and credential resolution flow are documented.
- Run lifecycle, step routing (per the EPIC-004 routing ADR), and merge ordering match the implementation.
- Validation policy artifacts and execution evidence are documented in the relevant sections.
- Diagrams and examples reflect the final shipped behavior.

## Acceptance Criteria

- All ADRs produced under INIT-014 are linked from at least one architecture doc.
- No architecture doc references behavior that was scoped out of INIT-014.
- Single-repo behavior is documented as the backward-compatible default.
- Cross-component terminology is consistent (catalog vs binding, primary vs code, etc.).
- Diagrams match the running implementation.
