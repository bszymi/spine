---
id: TASK-008
type: Task
title: Fix CLI documentation for planning run flags
status: Pending
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
---

# TASK-008 — Fix CLI Documentation for Planning Run Flags

---

## Purpose

The CLI `spine run start` command uses `--task` flag for the artifact path, but TASK-006 acceptance criteria shows a positional argument form. The README and any CLI usage docs should be updated to document the correct flag-based invocation for planning runs.

---

## Deliverable

Updates to:
- `README.md` — add planning run CLI example: `spine run start --task <path> --mode planning --content <file>`
- `CONTRIBUTING.md` — if CLI usage is documented there, update accordingly
- CLI command help text — verify `--mode` and `--content` flag descriptions are clear

---

## Acceptance Criteria

- README documents planning run CLI usage with correct flag-based syntax
- No references to positional argument form for task path
- CLI help text (`spine run start --help`) shows all three flags with descriptions
