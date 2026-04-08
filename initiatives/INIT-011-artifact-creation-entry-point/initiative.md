---
id: INIT-011
type: Initiative
title: Artifact Creation Entry Point
status: Draft
owner: bszymi
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: related_to
    target: /initiatives/INIT-006-governed-artifact-creation/initiative.md
  - type: related_to
    target: /governance/constitution.md
  - type: related_to
    target: /architecture/adr/ADR-006-planning-runs.md
---

# INIT-011 — Artifact Creation Entry Point

---

## 1. Intent

Provide a complete entry point for creating artifacts through Spine's governed workflow system.

INIT-006 built the planning run infrastructure (branch-based creation, mode-aware binding, artifact-creation workflow). What is missing is the practical entry point: a user says "create a Task in EPIC-003" and Spine allocates the next ID, creates a branch, starts the creation workflow, and handles numbering collisions at merge time.

Without this, artifact creation still requires manually constructing artifact files and committing them — bypassing the governance that INIT-006 established.

---

## 2. Scope

### In Scope

- Next-ID scanner: scan an epic's task directory (or initiative directory for epics) on `main` to determine the next sequential artifact ID
- Slug generation from title
- CLI command: `spine artifact create --type Task --epic EPIC-003 --title "Implement validation"`
- API endpoint: `POST /artifacts/create` that triggers a planning run
- Branch name generation from artifact type, parent, and allocated ID
- Merge-time collision detection: if the allocated ID was taken by a concurrent merge, detect the conflict
- Renumber-and-retry: re-scan `main`, pick the next available ID, rename the artifact on the branch, retry merge
- Unit and integration tests for all components

### Out of Scope

- Per-type creation workflows (the existing generic `artifact-creation.yaml` is sufficient for now; per-type governance is a future enhancement)
- UI for artifact creation (belongs in the management platform)
- Automatic child artifact scaffolding
- Changes to existing `StartPlanningRun()` behavior

---

## 3. Success Criteria

This initiative is successful when:

1. `spine artifact create --type Task --epic EPIC-003 --title "..."` starts a planning run with the correct next ID
2. The artifact is created on a branch following naming conventions (`INIT-XXX/EPIC-XXX/TASK-XXX-slug`)
3. The creation workflow resolves via `(artifactType, mode=creation)` binding
4. If two concurrent creations allocate the same ID, the second one automatically renumbers and retries
5. ID allocation respects existing conventions: zero-padded, sequential, scoped to parent
6. All existing tests continue to pass

---

## 4. Constraints

- Must reuse existing planning run infrastructure from INIT-006
- Must reuse existing workflow binding resolution (`ResolveBindingWithMode`)
- IDs must follow governance naming conventions (TASK-XXX, EPIC-XXX, etc.)
- No central counter or distributed locking — Git merge is the serialization point
- Must comply with Constitution SS7 (disposable database) and SS1 (Git is source of truth)

---

## 5. Work Breakdown

### Epics

| Epic | Title | Purpose |
|------|-------|---------|
| EPIC-001 | ID Allocation & Collision Resolution | Next-ID scanner, slug generation, merge-time renumbering |
| EPIC-002 | Create Entry Point | CLI command, API endpoint, planning run trigger wiring |

---

## 6. Risks

- **Collision frequency** — mitigated by optimistic allocation; collisions are rare (same epic, same moment) and cheap to resolve
- **Rename complexity** — renumbering requires updating the file path, front-matter ID, title heading, and branch name; all must stay consistent
- **Race in scanning** — mitigated by scanning `main` at branch creation time; merge is the true serialization point

---

## 7. Exit Criteria

INIT-011 may be marked complete when:

- Both epics are complete
- `spine artifact create` works end-to-end through CLI and API
- Collision renumbering is tested
- All existing tests continue to pass

---

## 8. Links

- INIT-006 (Governed Artifact Creation): `/initiatives/INIT-006-governed-artifact-creation/initiative.md`
- ADR-006 (Planning Runs): `/architecture/adr/ADR-006-planning-runs.md`
- Naming Conventions: `/governance/naming-conventions.md`
