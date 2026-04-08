---
id: TASK-003
type: Task
title: Path building for non-hierarchical artifact types
status: Completed
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-004-per-type-creation-workflows/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: implementation
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-004-per-type-creation-workflows/epic.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-001-id-allocation-and-collision-resolution/tasks/TASK-001-next-id-scanner.md
---

# TASK-003 — Path Building for Non-Hierarchical Artifact Types

---

## Purpose

Extend `BuildArtifactPath()` and `NextID()` to handle artifact types that don't follow the initiative/epic/task hierarchy.

EPIC-001's path building assumes parent-scoped, sequentially numbered artifacts. The new types work differently:

| Type | Location | Naming | Example |
|------|----------|--------|---------|
| ADR | `/architecture/adr/` | Sequential 4-digit (`ADR-XXXX-slug.md`) | `ADR-0007-event-sourcing.md` |
| Governance | `/governance/` | Descriptive slug (`slug.md`) | `api-standards.md` |
| Architecture | `/architecture/` | Descriptive slug (`slug.md`) | `caching-strategy.md` |
| Product | `/product/` | Descriptive slug (`slug.md`) | `pricing-model.md` |

---

## Deliverable

Extend `internal/artifact/id_allocator.go`:

### ADR path building

```go
// NextADRID scans /architecture/adr/ at the given ref and returns the
// next sequential ADR ID with 4-digit padding.
// Example: ADR-0001 through ADR-0006 exist → returns "ADR-0007"
func NextADRID(ctx context.Context, gitClient git.GitClient, ref string) (string, error)
```

ADR path: `architecture/adr/ADR-XXXX-slug.md`

### Document path building

Documents don't have sequential IDs. The path is simply:

```go
// BuildDocumentPath returns the path for a new document artifact.
// type=Governance → governance/slug.md
// type=Architecture → architecture/slug.md
// type=Product → product/slug.md
func BuildDocumentPath(artifactType, slug string) string
```

### Collision handling for documents

Unlike sequential IDs where collisions are about numbers, document collisions are about slugs. If `governance/api-standards.md` already exists:
- Return an error — the user should pick a different title
- No auto-renumbering (descriptive names aren't fungible like TASK-006 vs TASK-007)

### Update API endpoint

The `POST /artifacts/create` handler must route to the correct path builder:

- Initiative/Epic/Task → `NextID()` + `BuildArtifactPath()` (parent-scoped, sequential)
- ADR → `NextADRID()` + ADR path builder (global scope, 4-digit sequential)
- Governance/Architecture/Product → `BuildDocumentPath()` (descriptive slug, no ID)

The `parent` field is not required for ADR, Governance, Architecture, or Product — these are not scoped under a parent artifact.

---

## Acceptance Criteria

- `NextADRID` returns correct 4-digit sequential IDs
- ADR path follows `architecture/adr/ADR-XXXX-slug.md` convention
- Document path follows `type-directory/slug.md` convention
- Duplicate slug for documents returns a clear error (not silent renumbering)
- API endpoint routes to correct path builder based on artifact type
- `parent` field validation: required for Task/Epic, rejected for ADR/Governance/Architecture/Product
