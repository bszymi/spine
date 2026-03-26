---
id: TASK-001
type: Task
title: CLI init-repo Implementation
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
---

# TASK-001 — CLI init-repo Implementation

## Purpose

Implement the `spine init-repo` command to initialize a new Spine repository with the correct directory structure, seed governance documents, and initial configuration.

## Deliverable

- `spine init-repo [path]` — creates directory structure per repository-structure.md
- Seeds governance directory with charter, constitution, guidelines templates
- Creates `workflows/` directory
- Creates `initiatives/` directory
- Creates `templates/` directory with artifact templates
- Initializes Git repository if not already initialized

## Acceptance Criteria

- `spine init-repo` creates a valid Spine repository structure
- All required directories are created
- Seed documents are valid artifacts (parseable YAML front matter)
- Command is idempotent (running twice doesn't corrupt existing state)
- Existing files are not overwritten
