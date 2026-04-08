---
id: EPIC-004
type: Epic
title: "Per-Type Creation Workflows"
status: Completed
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
owner: bszymi
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-002-create-entry-point/epic.md
---

# EPIC-004 — Per-Type Creation Workflows

---

## 1. Purpose

Extend governed artifact creation to cover all artifact types, not just Initiative/Epic/Task.

The existing `artifact-creation.yaml` workflow covers the Draft → Pending lifecycle shared by Initiative, Epic, and Task. Three other artifact families need their own creation workflows:

- **ADR** — Proposed → Accepted. Has sequential IDs (ADR-XXXX, 4-digit). Needs architecture review.
- **Governance documents** (charter, constitution, guidelines) — Living Document initial status. Descriptive names, no sequential IDs. Lives in `/governance/`.
- **Product documents** — Living Document initial status. Descriptive names. Lives in `/product/`.
- **Architecture documents** — Living Document initial status. Descriptive names. Lives in `/architecture/`.

Governance, Product, and Architecture share the same status model pattern and can use a single shared workflow.

---

## 2. Scope

### In Scope

- `adr-creation.yaml` workflow: draft → validate → architecture review → accept/reject
- `document-creation.yaml` workflow: draft → validate → review → approve. Shared by Governance, Product, and Architecture types.
- Path building for non-hierarchical artifacts (descriptive slugs, no parent directory)
- Path building for ADRs (sequential 4-digit IDs in `/architecture/adr/`)
- Update `POST /artifacts/create` to accept ADR, Governance, Architecture, Product types
- ID allocation for ADRs (4-digit, global scope — not scoped to a parent)
- Tests for all new workflows

### Out of Scope

- Changes to the existing `artifact-creation.yaml` (Initiative/Epic/Task)
- Changes to the workflow engine itself
- Governance amendment workflows (updating existing governance docs is a separate concern)

---

## 3. Success Criteria

1. `spine artifact create --type ADR --title "Use event sourcing"` starts a planning run with `adr-creation.yaml`
2. `spine artifact create --type Governance --title "API standards"` starts a planning run with `document-creation.yaml`
3. Same for Product and Architecture types
4. ADR gets sequential 4-digit ID (ADR-0007 if ADR-0006 exists)
5. Document types get descriptive filenames (`api-standards.md`) not sequential IDs
6. Workflow binding resolves correctly: `(ADR, creation)` → `adr-creation`, `(Governance, creation)` → `document-creation`
7. No binding conflicts with existing workflows

---

## 4. Key Files

- `workflows/adr-creation.yaml` (new)
- `workflows/document-creation.yaml` (new)
- `internal/artifact/id_allocator.go` (extend for ADR 4-digit, global scope)
- `internal/artifact/id_allocator.go` (extend for descriptive-name path building)
- `internal/gateway/handlers_artifacts.go` (accept new types)

---

## 5. Dependencies

- EPIC-002 (Create Entry Point) — the API endpoint must exist
- EPIC-001 (ID Allocation) — `NextID()` must support ADR's 4-digit global scope
