---
id: EPIC-001
type: Epic
title: "ID Allocation & Collision Resolution"
status: Completed
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
owner: bszymi
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
---

# EPIC-001 — ID Allocation & Collision Resolution

---

## 1. Purpose

Implement the logic for automatically allocating the next artifact ID within a scope (e.g., next TASK-XXX under an epic) and handling collisions when concurrent creations compete for the same number.

---

## 2. Scope

### In Scope

- Next-ID scanner: given a parent path and artifact type, scan the directory on a given ref and return the next sequential ID
- Slug generator: convert a title string to a valid slug (lowercase, hyphen-separated)
- Path builder: combine parent path, allocated ID, and slug into the correct file/directory path per naming conventions
- Merge-time collision detector: identify when a planned artifact's ID conflicts with an ID that was merged to main after allocation
- Renumber handler: re-scan main, allocate a new ID, rename the artifact file and update front-matter fields (id, title heading), update branch name if needed
- Unit tests for all components

### Out of Scope

- CLI or API endpoints (EPIC-002)
- Changes to the merge infrastructure itself
- Per-type creation workflows

---

## 3. Success Criteria

1. `NextID("EPIC-003/tasks", "Task", ref)` returns `TASK-006` when TASK-001 through TASK-005 exist
2. Gaps are preserved (if TASK-003 is missing, next is still TASK-006, not TASK-003)
3. Slug generation produces valid folder/file names per naming conventions
4. Collision detection correctly identifies when the target path already exists on main
5. Renumber handler updates all references (file path, front-matter ID, heading) consistently

---

## 4. Key Files

- `internal/artifact/id_allocator.go` (new)
- `internal/artifact/slug.go` (new)
- `internal/engine/merge.go` (collision detection extension)

---

## 5. Dependencies

None — this is foundational work for EPIC-002.
