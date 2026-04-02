---
id: EPIC-001
type: Epic
title: Product Specification Update
status: Completed
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
  - type: related_to
    target: /product/product-definition.md
---

# EPIC-001 — Product Specification Update

---

## Purpose

Update the product specification to describe workspace-aware hosting as a Spine product capability. The product spec should articulate what workspaces mean from a product perspective — why they exist, what isolation guarantees they provide, and how the hosting model (single vs shared) affects users.

This must happen before architecture updates (EPIC-002) so that architecture decisions are grounded in product intent.

---

## Key Work Areas

- Define "workspace" as a product concept — a named, isolated context containing a governed repository, runtime state, projection state, and actor scope
- Describe the two hosting modes from a user/operator perspective:
  - **Single mode** — one workspace per Spine instance (current behavior, default)
  - **Shared mode** — multiple workspaces in one Spine instance (operational scaling)
- Clarify what isolation means at the product level: separate data, separate Git, separate actors, no cross-workspace visibility
- Describe how users interact with workspaces (workspace ID on API calls, CLI workspace selection)
- Position workspaces relative to existing product concepts (artifacts, workflows, actors, runs)

---

## Primary Outputs

- Updated product specification document(s)

---

## Acceptance Criteria

- "Workspace" is defined as a product concept with clear isolation guarantees
- Both hosting modes (single, shared) are described from a product perspective
- The spec makes clear that workspace isolation is a product invariant, not a deployment detail
- User interaction with workspaces is described (API, CLI)
- The update is consistent with existing product principles and does not contradict the Spine Constitution
