---
id: TASK-001
type: Task
title: Implement next-ID scanner
status: Pending
epic: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-001-id-allocation-and-collision-resolution/epic.md
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
work_type: implementation
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-001-id-allocation-and-collision-resolution/epic.md
---

# TASK-001 — Implement Next-ID Scanner

---

## Purpose

Create a function that scans a parent directory at a given Git ref and returns the next available artifact ID.

---

## Deliverable

`internal/artifact/id_allocator.go`

Implement:

```go
// NextID scans the parent directory at the given ref for existing artifacts
// of the specified type and returns the next sequential ID.
//
// Example: NextID(ctx, gitClient, "initiatives/INIT-003/epics/EPIC-003/tasks", "Task", "HEAD")
// Returns: "TASK-006" if TASK-001 through TASK-005 exist.
//
// Rules:
// - Scans for directories/files matching the artifact type prefix (TASK-, EPIC-, etc.)
// - Extracts the numeric part from each match
// - Returns max+1, zero-padded per naming conventions
// - Gaps are preserved (does not fill gaps)
// - Returns TYPE-001 if no existing artifacts are found
func NextID(ctx context.Context, gitClient git.GitClient, parentDir, artifactType, ref string) (string, error)
```

Also implement slug generation:

```go
// Slugify converts a title string to a valid artifact slug.
// "Implement validation" -> "implement-validation"
// Rules: lowercase, replace spaces/underscores with hyphens, strip non-alphanumeric,
// collapse consecutive hyphens, trim leading/trailing hyphens.
func Slugify(title string) string
```

And path building:

```go
// BuildArtifactPath constructs the full path for a new artifact.
// For Tasks: parentDir/TASK-XXX-slug.md (file)
// For Epics: parentDir/EPIC-XXX-slug/epic.md (directory + file)
// For Initiatives: parentDir/INIT-XXX-slug/initiative.md (directory + file)
func BuildArtifactPath(artifactType, id, slug, parentDir string) string
```

---

## Acceptance Criteria

- NextID correctly scans a directory and returns the next sequential number
- Handles zero artifacts (returns TYPE-001)
- Handles gaps (TASK-001, TASK-003 -> returns TASK-004)
- Handles the 900-series follow-up IDs (ignores them for regular allocation)
- Zero-pads per naming conventions (3 digits for tasks/epics/initiatives, 4 for ADRs)
- Slugify produces valid slugs from arbitrary titles
- BuildArtifactPath produces paths matching naming conventions
