---
id: TASK-001
type: Task
title: Define governed repository catalog format
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: design
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
---

# TASK-001 - Define Governed Repository Catalog Format

---

## Purpose

Define the Git-versioned catalog that gives code repositories stable workspace-scoped IDs, and record the identity model as an architectural decision.

## Deliverable

1. New ADR (next available number under `architecture/adr/`) capturing the repository identity model: primary-vs-code distinction, governed catalog vs runtime binding split, ID stability guarantees, and the decision to keep operational data out of Git.
2. Update `/architecture/multi-repository-integration.md` (and `/architecture/git-integration.md` where it touches single-repo assumptions) to define `/.spine/repositories.yaml`.
3. Update `governance/artifact-schema.md` to document the catalog file as a governed artifact.

The catalog format should include:

- Repository ID
- Kind: `spine` or `code`
- Human display name
- Default branch
- Optional role or description
- Status rules for catalog entries

Operational fields such as clone URL, credentials, local path, and tokens must be excluded from the catalog.

## Acceptance Criteria

- ADR is committed and linked from the catalog documentation.
- Catalog format is documented with examples.
- `spine` is reserved as the primary repository ID.
- Repository IDs are lowercase alphanumeric with hyphens.
- The catalog is optional for existing single-repo workspaces.
- Runtime-only data is explicitly excluded.
- `governance/artifact-schema.md` references the catalog file.

