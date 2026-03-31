---
id: TASK-004
type: Task
title: Update governance and architecture docs for configurable artifacts directory
status: Completed
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-003-configurable-artifacts-directory/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
work_type: implementation
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-003-configurable-artifacts-directory/epic.md
---

# TASK-004 — Update Governance and Architecture Docs

---

## Purpose

Update governance and architecture documents to reflect that artifact paths are relative to the configurable Spine artifacts directory, not the repo root.

---

## Deliverable

### governance/repository-structure.md

Add a section explaining:
- Spine artifacts live in a configurable directory (default: `spine/`)
- The directory is defined in `.spine.yaml` at the repo root
- All artifact paths in documentation and front matter are relative to this directory
- For Spine's own repo, `artifacts_dir: /` means artifacts are at the repo root

### governance/guidelines.md

Update §7 (Linking Conventions):
- Clarify that paths like `/governance/charter.md` are relative to the Spine artifacts directory
- Example: with `artifacts_dir: spine/`, the file is at `<repo>/spine/governance/charter.md`

### governance/naming-conventions.md

Add note that all path conventions (folder names, file names) are within the Spine artifacts directory.

### architecture/git-integration.md

Update to document:
- `.spine.yaml` is read at startup
- Projection sync and file discovery use the configured directory
- Git operations (commits, pathspecs) are scoped to the artifacts directory

---

## Acceptance Criteria

- All four documents updated
- No contradictions with existing content
- Paths are clearly documented as Spine-root-relative
- A new user reading the docs understands where artifacts live
