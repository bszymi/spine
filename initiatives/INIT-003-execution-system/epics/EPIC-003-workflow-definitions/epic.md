---
id: EPIC-003
type: Epic
title: Workflow Definitions
status: Pending
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/initiative.md
---

# EPIC-003 — Workflow Definitions

---

## Purpose

Create the first executable workflow definitions and wire workflow binding into run creation. The `workflows/` directory doesn't exist yet — the engine can parse workflow YAML but there are no workflow files to execute.

---

## Key Work Areas

- Create `workflows/` directory with reference workflows
- Default task workflow (draft → execute → review → accept/reject → commit)
- Workflow binding integration with run creation (ResolveBinding)
- Work-type filtering in applies_to clause
- Authoring patterns and documentation

---

## Primary Outputs

- `workflows/task-default.yaml` — Default task workflow
- `workflows/task-spike.yaml` — Spike/investigation workflow
- `workflows/adr.yaml` — ADR workflow
- Workflow binding wired into engine orchestrator

---

## Acceptance Criteria

- At least one workflow definition exists and is parseable
- Workflow binding resolves correct workflow for a given task type
- Work-type filtering selects appropriate workflow variant
- Workflows are discoverable by projection service
- Reference workflows cover the most common task patterns
