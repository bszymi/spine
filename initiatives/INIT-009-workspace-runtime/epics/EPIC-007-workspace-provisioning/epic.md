---
id: EPIC-007
type: Epic
title: Workspace Provisioning
status: Pending
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-004-gateway-workspace-routing/epic.md
  - type: related_to
    target: /architecture/data-model.md
  - type: related_to
    target: /architecture/git-integration.md
---

# EPIC-007 — Workspace Provisioning

---

## Purpose

Provide a complete flow for creating and provisioning workspaces in shared runtime mode. Without this, workspaces can only be created by manual SQL inserts and manual infrastructure setup. This epic delivers API endpoints, CLI commands, and automated provisioning of the database and Git repository for each new workspace.

---

## Key Work Areas

- API endpoints for workspace lifecycle management (create, list, get, deactivate)
- Database provisioning: create a new PostgreSQL database for the workspace and run schema migrations
- Git repository provisioning: initialize a workspace Git repository on disk
- CLI commands for workspace management
- End-to-end provisioning flow: one API call creates the registry entry, database, and Git repo

---

## Primary Outputs

- Workspace management API endpoints in gateway
- Database provisioning service in `internal/workspace/`
- Git repo provisioning in `internal/workspace/`
- CLI `spine workspace` subcommands
- Integration tests for full provisioning flow

---

## Acceptance Criteria

- `POST /api/v1/workspaces` creates a fully provisioned workspace (registry entry, database, Git repo)
- `GET /api/v1/workspaces` lists all workspaces with status
- `GET /api/v1/workspaces/{id}` returns workspace details
- `POST /api/v1/workspaces/{id}/deactivate` marks workspace inactive and stops serving requests for it
- The provisioned database has all Spine schemas (runtime + projection) applied
- Fresh repo mode: the provisioned Git repository is initialized with Spine structure
- Clone repo mode: an existing remote repo is cloned; if it's already a Spine repo, its contents are synced into the projection database; if not, Spine structure is added
- CLI `spine workspace create/list/deactivate` commands work against the API
- Provisioning is atomic — if any step fails, partial resources are cleaned up
