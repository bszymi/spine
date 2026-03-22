---
id: TASK-004
type: Task
title: CLI Commands
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-005-access-gateway-api/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-005-access-gateway-api/epic.md
---

# TASK-004 — CLI Commands

## Purpose

Implement CLI commands using Cobra that map to API operations.

## Deliverable

- `spine serve` — start HTTP server
- `spine migrate` — run database migrations
- `spine health` — check system health
- `spine init-repo` — initialize Git repository
- `spine artifact create/read/update/list/validate` — artifact operations
- `spine run start/status/cancel` — Run operations
- `spine task accept/reject/cancel/abandon/supersede` — task governance
- Structured JSON output and human-friendly table output
- Token configuration via environment variable or config file

## Acceptance Criteria

- All CLI commands execute the corresponding operation
- Output formats (JSON, table) work correctly
- Error messages are clear and actionable
- CLI connects to the local server or operates directly (for serve, migrate, health, init-repo)
- Integration tests verify CLI → API → service flow
