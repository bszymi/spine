---
id: TASK-002
type: Task
title: "Seed workflow-lifecycle.yaml via spine init-repo"
status: Completed
work_type: implementation
created: 2026-04-17
epic: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/epic.md
initiative: /initiatives/INIT-017-workflow-lifecycle/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/epic.md
  - type: blocked_by
    target: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/tasks/TASK-001-adr-workflow-lifecycle.md
---

# TASK-002 — Seed workflow-lifecycle.yaml via spine init-repo

---

## Context

Every `workflow.create/update` will bind to a governing workflow. Fresh repositories need that workflow to exist before anyone can edit workflows — otherwise bootstrap deadlocks.

## Deliverable

- Author `workflows/workflow-lifecycle.yaml` with two steps:
  - `draft` — manual step, actor authors the workflow body; outcome `submitted → review`.
  - `review` — review step, reviewer approves or requests rework; outcomes `approved` (commits to authoritative branch) and `needs_rework → draft`.
- `applies_to: [Workflow]` (may require introducing `Workflow` as a valid artifact type for workflow binding — coordinate with TASK-004).
- `mode: creation` (mirrors the artifact-creation workflows).
- Set `entry_step: draft`, appropriate timeouts using `time.ParseDuration`-compatible units, and `retry` where required by schema validation.
- Register the file as a seed in `spine init-repo` so fresh repositories get it.
- Extend the existing `init-repo` tests to assert the file is created, parseable, and passes `workflow.Validate`.

## Acceptance Criteria

- `spine init-repo /tmp/fresh` produces `workflows/workflow-lifecycle.yaml`.
- The seeded file passes the workflow validation suite.
- The file is version-controlled (part of the initial commit `init-repo` produces).
- The file's existence is covered by tests.
