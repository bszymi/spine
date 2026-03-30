---
id: TASK-001
type: Task
title: Create initiative-lifecycle.yaml
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
---

# TASK-001 — Create initiative-lifecycle.yaml

---

## Purpose

Define the workflow that governs initiative creation through planning runs.

---

## Deliverable

`workflows/initiative-lifecycle.yaml`

Workflow structure:
- `applies_to: [Initiative]`
- `entry_step: draft`
- Steps:
  - **draft** (manual, hybrid) — actor drafts the initiative and child artifacts on the branch. Precondition: artifact status is Draft. Outcome: `ready_for_review` → review.
  - **review** (review, human_only) — reviewer evaluates the initiative plan. Outcomes: `approved` → end (commit status: In Progress), `needs_revision` → draft (commit status: Draft).

---

## Acceptance Criteria

- Workflow follows the format in `architecture/workflow-definition-format.md`
- Workflow parses correctly by Spine's workflow parser
- Steps have appropriate timeouts
- Follows the patterns of `epic-lifecycle.yaml` and `adr.yaml`
