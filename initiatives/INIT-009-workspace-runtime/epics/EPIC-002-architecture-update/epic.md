---
id: EPIC-002
type: Epic
title: Architecture Update
status: Pending
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-001-product-spec-update/epic.md
  - type: related_to
    target: /architecture/components.md
  - type: related_to
    target: /architecture/data-model.md
  - type: related_to
    target: /architecture/git-integration.md
---

# EPIC-002 — Architecture Update

---

## Purpose

Update architecture documents to describe the workspace-aware runtime model. These updates ground the implementation epics that follow — implementation tasks will reference the updated architecture, not the original v0.x single-workspace descriptions.

This epic depends on EPIC-001 (product spec) so that architecture decisions align with product intent.

---

## Key Work Areas

- **`architecture/components.md`** — Update §6 Deployment Considerations to describe shared runtime model where one Spine process hosts multiple workspace contexts, each with its own repo and database, resolved via `WorkspaceResolver`
- **`architecture/data-model.md`** — Update §7 Storage Technology Guidance to describe per-workspace databases and the workspace registry as a separate coordination database
- **`architecture/git-integration.md`** — Update §2.1 Repository Scope to describe workspace-scoped repository handles, where each workspace maps to its own Git repository resolved at the request boundary
- Optionally, introduce a new architecture document (e.g., `architecture/workspace-runtime.md`) if the workspace runtime model warrants its own dedicated document rather than evolution notes across existing docs

---

## Primary Outputs

- Updated `architecture/components.md`
- Updated `architecture/data-model.md`
- Updated `architecture/git-integration.md`
- Optionally: new `architecture/workspace-runtime.md`

---

## Acceptance Criteria

- Each updated document retains accurate v0.x descriptions as historical context
- Each updated document describes the workspace-aware model clearly
- The architecture describes the `WorkspaceResolver` interface, two provider modes, and service pool as core runtime concepts
- The architecture describes workspace isolation at the resource level (database, Git, actors)
- The distinction between single mode and shared mode is explicit
- Global system routes (health, metrics) are documented as workspace-exempt
- Updated architecture is consistent with the product spec from EPIC-001
- Implementation epics (to be added later) can reference these documents as their source of truth
