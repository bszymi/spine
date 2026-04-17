---
id: TASK-001
type: Task
title: "Author ADR-008 — Workflow Lifecycle Governance"
status: Completed
work_type: documentation
created: 2026-04-17
epic: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/epic.md
initiative: /initiatives/INIT-017-workflow-lifecycle/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/epic.md
---

# TASK-001 — Author ADR-008 — Workflow Lifecycle Governance

---

## Context

ADR-007 made workflow definitions a first-class resource. This ADR captures how *changes to that resource* are governed: branch-based edits via a planning-mode Run, approval-gated merge, Run-pinning behavior preserved, operator bypass for recovery. Must be Accepted before the implementation tasks land.

## Deliverable

Create `/architecture/adr/ADR-008-workflow-lifecycle-governance.md` (status: `Proposed` during review, `Accepted` before merge). Cover:

- **Context**: reference ADR-001 (storage), ADR-006 (planning runs), ADR-007 (resource separation). Explain the gap — ADR-007 created the resource but not the edit flow.
- **Decision**:
  - Every `workflow.create/update` flows through a planning-mode Run bound to `workflow-lifecycle.yaml`.
  - Repeated edits on the same `run_id` stack commits on the Run's branch.
  - Approval outcome merges the branch; the workflow becomes Active at merge commit.
  - Existing Runs stay pinned to the workflow commit SHA captured at `run.start` (per ADR-001); no cascade.
  - Operator role may bypass the Run and commit directly (recovery escape hatch).
  - `workflow-lifecycle.yaml` is itself a workflow — teams extend it by editing that one file.
- **Consequences**: audit/draft state gained; edits cost a branch + approval; bootstrap requires the seed workflow to exist before its own governance applies.
- **Cross-references**: list the other ADRs, workflow-definition-format, workflow-validation.

## Acceptance Criteria

- ADR-008 exists, passes artifact schema validation, and is marked Accepted before any other INIT-017 task lands.
- Decision rationale is unambiguous — a future contributor can tell why branch-based + approval is preferred over direct commit.
- Operator bypass conditions are explicit (who, when, what it does to audit).
