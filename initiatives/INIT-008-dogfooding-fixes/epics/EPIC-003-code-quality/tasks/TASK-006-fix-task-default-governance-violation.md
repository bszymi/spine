---
id: TASK-006
type: Task
title: "Fix task-default.yaml committing runtime-only In Progress status"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
---

# TASK-006 — Fix task-default.yaml Committing Runtime-Only In Progress Status

---

## Purpose

`/workflows/task-default.yaml` (line 34) commits `status: In Progress` to the Git artifact on the `draft -> execute` transition. `governance/task-lifecycle.md` explicitly states `In Progress` is a runtime-only state that must never be committed to Git. This directly contradicts the constitution's principle that runtime states must not pollute versioned artifacts.

---

## Deliverable

Change the committed status to a governed status (e.g., `Pending`) or remove the commit status from the `draft -> execute` transition and let the runtime track execution state.

---

## Acceptance Criteria

- `task-default.yaml` does not commit `In Progress` to Git artifacts
- Workflow transitions use only governed statuses from `artifact-schema.md`
- Existing scenario tests pass
