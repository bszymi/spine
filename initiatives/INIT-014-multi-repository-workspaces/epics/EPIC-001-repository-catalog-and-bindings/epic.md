---
id: EPIC-001
type: Epic
title: "Repository Catalog and Operational Bindings"
status: Pending
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
owner: bszymi
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
---

# EPIC-001 - Repository Catalog and Operational Bindings

---

## Purpose

Define repositories as first-class workspace resources while preserving the primary Spine repository as the governed source of truth.

This epic separates stable repository identity from operational connection details. Task artifacts need stable repository IDs that survive database rebuilds; clone URLs, credentials, local paths, and active/inactive state are runtime concerns.

---

## Scope

### In Scope

- Governed repository catalog stored in the primary Spine repo
- Runtime repository bindings for clone URLs, local paths, credentials, and status
- Repository domain model and persistence
- Repository management API and CLI surface
- Provisioning and deregistration behavior

### Out of Scope

- Per-repository RBAC
- Repository mirroring or synchronization
- Cross-workspace repository sharing

---

## Primary Outputs

- `/.spine/repositories.yaml` catalog format
- Runtime repository binding schema
- Repository CRUD API and CLI commands
- Tests for repository identity, binding, and lifecycle rules

---

## Acceptance Criteria

1. Every workspace has exactly one primary repository with ID `spine`.
2. Code repositories can be registered with stable, workspace-scoped IDs.
3. Task artifacts can reference repository IDs that are reconstructible from Git.
4. Operational clone and credential data is not committed to Git.
5. Single-repository workspaces continue to work without a catalog file.

