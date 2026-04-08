---
id: TASK-003
type: Task
title: Unit tests for ID allocation and collision resolution
status: Pending
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-001-id-allocation-and-collision-resolution/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: testing
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-001-id-allocation-and-collision-resolution/epic.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-001-id-allocation-and-collision-resolution/tasks/TASK-002-collision-detection-and-renumber.md
---

# TASK-003 — Unit Tests for ID Allocation and Collision Resolution

---

## Purpose

Comprehensive test coverage for the next-ID scanner, slug generator, path builder, and collision renumber logic.

---

## Deliverable

`internal/artifact/id_allocator_test.go` and `internal/artifact/renumber_test.go` (or equivalent)

### NextID tests

- Empty directory returns TYPE-001
- Sequential IDs (001-005) returns TYPE-006
- Gaps preserved (001, 003, 005 returns TYPE-006, not TYPE-002)
- 900-series follow-up IDs are excluded from scanning
- Different artifact types use correct padding (3 digits for Task/Epic/Init, 4 for ADR)
- Non-matching files/directories in the scan path are ignored

### Slugify tests

- Basic title: "Implement validation" -> "implement-validation"
- Special characters stripped: "Add API (v2)" -> "add-api-v2"
- Consecutive hyphens collapsed: "foo--bar" -> "foo-bar"
- Leading/trailing hyphens trimmed
- Underscores replaced: "some_thing" -> "some-thing"

### BuildArtifactPath tests

- Task: produces `parentDir/TASK-XXX-slug.md`
- Epic: produces `parentDir/EPIC-XXX-slug/epic.md`
- Initiative: produces `parentDir/INIT-XXX-slug/initiative.md`
- ADR: produces correct path with 4-digit padding

### Collision renumber tests

- ID conflict detected correctly
- Renumber updates file path, front-matter ID, and heading
- Non-ID conflicts are not caught by collision handler
- Max retry limit is respected

---

## Acceptance Criteria

- All test cases pass
- Edge cases for numbering (overflow, malformed IDs) are covered
- Tests use mock git client (no real Git operations needed for unit tests)
