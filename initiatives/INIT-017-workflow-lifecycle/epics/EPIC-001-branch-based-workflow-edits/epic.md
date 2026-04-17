---
id: EPIC-001
type: Epic
title: "Branch-Based Workflow Edits with Approval"
status: Pending
initiative: /initiatives/INIT-017-workflow-lifecycle/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-017-workflow-lifecycle/initiative.md
---

# EPIC-001 — Branch-Based Workflow Edits with Approval

---

## Purpose

Implement the workflow-lifecycle governance model: `workflow.create/update` goes through a planning-mode Run, an approval step merges the result, operator role has a direct-commit escape hatch. The lifecycle is itself a workflow (`workflow-lifecycle.yaml`) seeded by `spine init-repo`.

---

## Key Work Areas

- ADR-008 authoring (governance decision).
- Seed `workflow-lifecycle.yaml` via `spine init-repo` templates.
- Extend `workflow.Service` and handlers to accept `write_context { run_id }` and commit to the run's branch.
- Integrate with planning-run orchestrator: auto-branch on first `workflow.create`, merge on approval.
- Operator bypass rule + auth enforcement.
- Docs sweep.

---

## Acceptance Criteria

- End-to-end round trip works: fresh repo → `workflow.create` (reviewer) → branch opened → edits stacked → approval outcome submitted → branch merged → workflow active.
- Operator role can bypass the Run and commit directly (documented, enforced).
- Existing Runs continue against their pinned workflow versions — no rebase cascade.
- ADR-008 is Accepted; all related documentation is updated.
