---
id: TASK-005
type: Task
title: "Operator Bypass and Documentation Sweep"
status: Pending
work_type: implementation
created: 2026-04-17
epic: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/epic.md
initiative: /initiatives/INIT-017-workflow-lifecycle/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/epic.md
  - type: blocked_by
    target: /initiatives/INIT-017-workflow-lifecycle/epics/EPIC-001-branch-based-workflow-edits/tasks/TASK-004-planning-run-integration.md
---

# TASK-005 — Operator Bypass and Documentation Sweep

---

## Context

The branch+approval flow is the default. Operator role retains a direct-commit path for recovery (e.g. the `workflow-lifecycle` workflow is itself broken and the governance flow deadlocks). All public documentation must reflect the new default and the escape hatch.

## Deliverable

- **Auth / service behavior**:
  - `workflow.create/update` with no `write_context` and caller role `operator` (or higher) → commits directly to the authoritative branch, bypassing the Run entirely.
  - `workflow.create/update` with no `write_context` and caller role `reviewer` → starts a planning Run (the TASK-004 path).
  - Document the difference explicitly in a comment in `internal/gateway/handlers_workflows.go`.
  - Audit-log the bypass path with a clear event/log field (`workflow.bypass = true` on commit trailer) so audits can distinguish bypass from governed commits.
- **Docs sweep**:
  - `/architecture/api-operations.md` §3.2 — reflect the new default flow and the operator bypass.
  - `/architecture/access-surface.md` §3.2.2 — same.
  - `/architecture/validation-service.md` — mention that approval-step review is separate from structural validation.
  - `/README.md` — CLI + API Endpoints tables updated if any endpoint shape changes.
  - `docs/integration-guide.md` — workflow edit flow prose, if present.
  - Add an entry to the ADR-007 §Future Work section linking to ADR-008 as its successor on this specific question.
- **Tests**:
  - Operator role with no `write_context` commits directly.
  - Reviewer role with no `write_context` starts a Run (from TASK-004).
  - Audit trailer differs between the two paths.

## Acceptance Criteria

- Operator bypass works and is tested.
- All architecture + governance + user docs reflect the ADR-008 flow.
- Commit trailers distinguish bypass from governed merges.
- Full `go test ./... -count=1` passes; `go vet` clean.
