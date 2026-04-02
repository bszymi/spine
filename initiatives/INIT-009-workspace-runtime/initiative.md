---
id: INIT-009
type: Initiative
title: Workspace Runtime
status: Pending
owner: bszymi
created: 2026-04-02
last_updated: 2026-04-02
links:
  - type: related_to
    target: /initiatives/INIT-002-implementation/initiative.md
  - type: related_to
    target: /architecture/components.md
  - type: related_to
    target: /architecture/access-surface.md
---

# INIT-009 — Workspace Runtime

---

## 1. Intent

Enable a single Spine runtime to host multiple isolated workspace contexts, while preserving the option for dedicated single-workspace deployments.

Today, one Spine instance maps to one workspace (one Git repository, one database, one set of actors). This is simple and provides strong isolation, but creates operational and infrastructure overhead as workspace count grows. Each workspace requires its own process, database, and deployment lifecycle.

This initiative introduces a **workspace routing layer** at the runtime boundary. A workspace registry maps workspace IDs to resource handles (database URL, Git repo path, actor scope). When a request arrives with a workspace ID, Spine resolves the appropriate resources and executes against them. Spine internals remain unchanged — each workspace still gets its own logical context with its own Git client, store, and projection service.

### Design decisions established before this initiative

These decisions were made during design review and are not up for re-evaluation within this initiative:

1. **Connection routing, not deep refactoring.** Workspace awareness lives at the boundary. Spine services continue to operate against a single logical context — they do not need workspace_id parameters threaded through every method.

2. **Two resolver providers, one interface.** A `WorkspaceResolver` interface with two implementations:
   - **File/env provider** — reads workspace config from a local file or environment variables. One workspace per instance. This is what Spine does today, behind a clean interface.
   - **Database provider** — looks up workspace config from a registry table. Multiple workspaces per instance.

3. **Stateless request model.** Every API call includes a workspace ID (in shared mode). No server-side sessions. Client-side SDKs and CLI config can bind workspace ID once and attach it transparently. In single mode, workspace ID is optional for backward compatibility.

4. **True resource isolation.** Each workspace gets its own database and Git repository. No shared tables with workspace_id partitioning. Isolation is at the connection level, not the query level.

---

## 2. Scope

### In scope

**Phase 1 — Documentation (this phase)**

- Update product specification to describe workspace-aware hosting as a product capability
- Update architecture documents (components, data-model, git-integration) to describe workspace-aware runtime model

**Phase 2 — Implementation (epics to be added after Phase 1)**

- Define `WorkspaceResolver` interface and `WorkspaceConfig` type
- Implement file/env provider and database provider
- Gateway middleware for workspace resolution
- Service pool for per-workspace service sets
- Background service adaptation (scheduler, projection sync, event routing)
- Observability with workspace identity
- CLI workspace support
- Workspace provisioning: API, database creation, Git repo initialization, CLI commands

### Out of scope

- Management platform UI
- Pricing, tenancy policy, or billing
- Cross-workspace operations
- Cloud provider deployment details

---

## 3. Success Criteria

This initiative is successful when:

1. Product and architecture documentation describe workspace-aware hosting
2. A single Spine runtime can serve requests for multiple workspaces with full isolation
3. The single-workspace deployment model continues to work unchanged via the file/env provider
4. Workspace identity is present in all logs, metrics, and traces
5. No Spine internal service requires modification to its core logic — workspace routing is handled at the boundary
6. A request for workspace A cannot read or mutate workspace B's data, Git, or actors

---

## 4. Primary Artifacts Produced

**Phase 1:**

- Updated product specification
- Updated architecture documents (components, data-model, git-integration)

**Phase 2 (after documentation is complete):**

- `internal/workspace/` — resolver interface, providers, config types, service pool
- Gateway middleware for workspace resolution
- Database migration for workspace registry table
- Updated CLI with workspace flag/config support

---

## 5. Constraints and Non-Negotiables

This initiative must comply with the Spine Constitution, including:

- **Source of Truth** — Git remains authoritative per workspace; workspace routing does not change this
- **Governed Execution** — governance rules operate within workspace scope, not weakened by shared hosting
- **Actor Neutrality** — actors are scoped per workspace; one workspace's actors cannot operate in another
- **Disposable Database** — runtime and projection data remain operational, per workspace

Additional constraints:

- The file/env provider must preserve 100% backward compatibility with current Spine behavior
- Connection strings and credentials must never leak across workspace boundaries
- Global system routes (health, metrics, readiness) are exempt from workspace resolution
- In single mode, workspace ID is optional — the resolver falls back to the configured workspace

---

## 6. Risks

- **Connection pool pressure:** Many workspaces in one process means many database connection pools. Need sensible pool sizing and idle eviction.
- **Git working directory management:** Multiple repos on disk means disk space and clone-time concerns. Lazy initialization helps but doesn't eliminate this.
- **Hidden global state:** Package-level globals (e.g., `observe.GlobalMetrics`, `actor.rrIndices`) may need workspace scoping.
- **Blast radius in shared mode:** A misbehaving workspace can affect others in the same runtime.

Mitigations:

- File/env provider ships first — validates the interface without shared-runtime complexity
- Connection pool limits are configurable per workspace
- Observability ensures workspace identity is always present for debugging
- Dedicated-runtime mode remains available for workspaces that need stronger isolation

---

## 7. Work Breakdown

### Phase 1 — Documentation

| Epic | Title | Dependencies |
|------|-------|-------------|
| EPIC-001 | Product Specification Update | None |
| EPIC-002 | Architecture Update | EPIC-001 |

### Phase 2 — Implementation

| Epic | Title | Dependencies |
|------|-------|-------------|
| EPIC-003 | Workspace Registry | None |
| EPIC-004 | Gateway Workspace Routing | EPIC-003 |
| EPIC-005 | Background Service Scoping | EPIC-003, EPIC-004 |
| EPIC-006 | Observability & Workspace Identity | EPIC-003, EPIC-004 |
| EPIC-007 | Workspace Provisioning | EPIC-003, EPIC-004 |

---

## 8. Exit Criteria

INIT-009 may be marked complete when:

- Product specification describes workspace-aware hosting
- Architecture documents describe the workspace runtime model
- Both workspace resolver providers are implemented and tested
- Gateway resolves workspace context on every request (shared mode) or falls back (single mode)
- Background services operate correctly across multiple workspaces
- Observability includes workspace identity
- Single-workspace deployment via file/env provider is backward compatible
- Workspaces can be created and fully provisioned (database + Git repo) via API and CLI

---

## 9. Links

- Charter: `/governance/charter.md`
- Constitution: `/governance/constitution.md`
- Components: `/architecture/components.md`
- Access Surface: `/architecture/access-surface.md`
- Data Model: `/architecture/data-model.md`
- Git Integration: `/architecture/git-integration.md`
