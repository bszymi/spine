---
id: TASK-006
type: Task
title: Update CLI run start command
status: Completed
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
---

# TASK-006 — Update CLI Run Start Command

---

## Purpose

Add `--mode` and `--content` flags to `spine run start` so planning runs can be started from the CLI.

---

## Deliverable

`cmd/spine/cmd_run.go`

Add flags:
- `--mode` (string, default "standard") — run mode
- `--content` (string) — path to a file containing the artifact content (read from disk)

When `--mode planning`:
- Read artifact content from the file specified by `--content`
- Include `mode` and `artifact_content` in the API request

---

## Acceptance Criteria

- `spine run start --mode planning --content ./initiative.md initiatives/.../initiative.md` works
- `--content` reads from a file path, not inline content
- Missing `--content` with `--mode planning` prints a clear error
- Default behavior (no flags) is unchanged
