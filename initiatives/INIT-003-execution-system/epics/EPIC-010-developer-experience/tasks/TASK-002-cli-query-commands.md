---
id: TASK-002
type: Task
title: CLI Query Subcommands
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
---

# TASK-002 — CLI Query Subcommands

## Purpose

Implement the query subcommands so developers can inspect artifacts, relationships, history, and runs from the command line.

## Deliverable

- `spine query artifacts [--type X] [--status Y] [--parent Z]` — list artifacts with filters
- `spine query graph [artifact-path] [--depth N]` — display artifact relationship graph
- `spine query history [artifact-path]` — show change history for an artifact
- `spine query runs [--task X] [--status Y]` — list workflow runs
- Table and JSON output formats for all commands

## Acceptance Criteria

- All query commands return correct results from the projection service
- Filters work as documented
- Output is clear and readable in table format
- JSON output is parseable for scripting
- Commands fail gracefully when the server is unreachable
