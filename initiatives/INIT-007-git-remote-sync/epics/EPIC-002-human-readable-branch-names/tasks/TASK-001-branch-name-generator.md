---
id: TASK-001
type: Task
title: Implement human-readable branch name generator
status: Completed
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-002-human-readable-branch-names/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
work_type: implementation
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-002-human-readable-branch-names/epic.md
---

# TASK-001 — Implement Human-Readable Branch Name Generator

---

## Purpose

Replace the opaque `spine/run/<run-id>` branch naming with a human-readable format that includes the artifact identity.

---

## Deliverable

`internal/engine/run.go` (or a new `internal/engine/branchname.go`)

Add a function:

```go
func generateBranchName(mode RunMode, artifactID, slug, runID string) string
```

Logic:
- Planning runs: `spine/plan/<artifact-id>-<slug>` (e.g., `spine/plan/INIT-001-build-spine-management-platform`)
- Standard runs: `spine/run/<artifact-id>-<slug>` (e.g., `spine/run/TASK-003-implement-start-planning-run`)
- Sanitize slug: lowercase, replace non-alphanumeric with hyphens, trim to reasonable length (max 60 chars for the slug portion)
- Collision handling: if the branch already exists, append `-<8-char random portion of run-id>`. Run IDs have format `run-XXXXXXXX` — extract the hex portion after `run-` (e.g., run ID `run-0a5d0f6d` → suffix `-0a5d0f6d`, giving `spine/plan/INIT-001-slug-0a5d0f6d`). Do not include the `run-` prefix in the suffix.

Extract artifact ID and slug from:
- For planning runs: parse from `artifact_content` front matter (`id` field) and derive slug from the artifact path
- For standard runs: parse from the existing artifact on main (`id` field) and derive slug from the path

Update `StartRun()` and `StartPlanningRun()` to use `generateBranchName()` instead of `fmt.Sprintf("spine/run/%s", runID)`.

---

## Acceptance Criteria

- Planning run branches: `spine/plan/<id>-<slug>`
- Standard run branches: `spine/run/<id>-<slug>`
- Slugs are sanitized for Git ref validity
- Collision appends short run ID suffix
- Run record `BranchName` stores the full generated name
- Unit tests for name generation, sanitization, and collision handling
