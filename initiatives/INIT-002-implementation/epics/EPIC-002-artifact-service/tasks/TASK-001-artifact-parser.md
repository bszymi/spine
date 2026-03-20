---
id: TASK-001
type: Task
title: Artifact Parser
status: Pending
epic: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-002-artifact-service/epic.md
---

# TASK-001 — Artifact Parser

## Purpose

Parse Markdown files with YAML front matter into domain Artifact types.

## Deliverable

- Parse YAML front matter from Markdown files
- Extract metadata fields (id, type, status, title, links, etc.)
- Parse link entries into typed structures
- Separate front matter from Markdown body content
- Handle malformed files gracefully (return errors, don't panic)

## Acceptance Criteria

- Parser correctly extracts all fields defined in artifact-schema.md §5
- Parser handles all artifact types (Initiative, Epic, Task, ADR, Governance, Architecture, Product)
- Invalid YAML produces structured parse errors
- Missing required fields produce validation errors
- Unit tests cover all artifact types with fixture files
