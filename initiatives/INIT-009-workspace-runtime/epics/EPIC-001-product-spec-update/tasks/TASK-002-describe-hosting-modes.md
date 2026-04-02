---
id: TASK-002
type: Task
title: Describe hosting modes in product spec
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-001-product-spec-update/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-001-product-spec-update/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-001-product-spec-update/tasks/TASK-001-define-workspace-concept.md
---

# TASK-002 — Describe hosting modes in product spec

---

## Purpose

Describe the two hosting modes from a user/operator perspective so it's clear how workspace isolation maps to deployment.

## Deliverable

Updates to the product specification document(s).

Content should define:

- **Single mode** — one workspace per Spine instance; current default behavior; simplest deployment; strongest operational isolation
- **Shared mode** — multiple workspaces in one Spine instance; reduces operational overhead at scale; logical isolation preserved, deployment isolation relaxed
- Both modes provide the same workspace isolation guarantees at the product level
- Hosting mode is an operator decision, not a user-facing distinction — users interact with workspaces the same way regardless of hosting mode
- How users interact with workspaces: workspace ID on API requests (header), CLI `--workspace` flag or persistent config

## Acceptance Criteria

- Both hosting modes are described from a product/operator perspective
- It's clear that isolation guarantees are identical in both modes
- User interaction with workspaces is described (API, CLI)
- The spec does not prescribe implementation details — it describes capabilities and guarantees
