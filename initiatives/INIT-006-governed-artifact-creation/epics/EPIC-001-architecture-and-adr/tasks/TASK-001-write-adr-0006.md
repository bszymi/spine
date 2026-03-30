---
id: TASK-001
type: Task
title: "Write ADR-0006: Planning Runs"
status: Completed
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-001-architecture-and-adr/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-001-architecture-and-adr/epic.md
---

# TASK-001 — Write ADR-0006: Planning Runs

---

## Purpose

Capture the architectural decision to introduce planning runs as the mechanism for governed artifact creation.

The ADR must document the problem (chicken-and-egg: artifacts must exist on main before runs can govern them), the decision (planning runs with branch-scoped creation), alternatives considered, and consequences.

---

## Deliverable

`/architecture/adr/ADR-0006-planning-runs.md`

Content should cover:

- Context: why artifact creation currently bypasses governance
- Decision: introduce `RunMode` with `planning` variant and `StartPlanningRun()` method
- Generic creation workflow: one `artifact-creation.yaml` covers all artifact types (Initiative, Epic, Task, Product, ADR) with steps: draft → validate → review → merge
- Workflow `mode` field: new optional field on workflow definitions (`execution` / `creation`). Planning runs resolve to `mode: creation` workflows, standard runs to `mode: execution`. Absent `mode` defaults to `execution` for backward compatibility.
- Automated validation step: the creation workflow includes an automated validation step that runs cross-artifact validation before human review
- Merge trigger model: clarify that planning runs follow the existing `committing` → `MergeRunBranch()` → `completed` path. The scheduler handles merge retries. This is not a new mechanism — it reuses the existing merge infrastructure.
- Write context relaxation: planning runs allow `write_context` with `run_id` only (no `task_path` required), since the run owns the entire branch
- Alternatives considered: raw branch writes, modified StartRun(), per-type creation workflows, new artifact-request entity
- Consequences: positive (governed creation), negative (added complexity), neutral (existing runs unchanged)

---

## Acceptance Criteria

- ADR follows the template in `/templates/adr-template.md`
- ADR references Constitution §4 (governed execution) and §7 (reproducibility)
- ADR is internally consistent with the initiative design
- ADR status is `Proposed`
