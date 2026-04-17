---
id: INIT-017
type: Initiative
title: Workflow Lifecycle Governance
status: Pending
owner: bszymi
created: 2026-04-17
links:
  - type: related_to
    target: /architecture/adr/ADR-007-workflow-resource-separation.md
  - type: related_to
    target: /architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md
  - type: related_to
    target: /architecture/adr/ADR-006-planning-runs.md
---

# INIT-017 — Workflow Lifecycle Governance

---

## Purpose

Today `workflow.create` and `workflow.update` commit directly to the authoritative branch. There is no draft state, no review step, and no audit trail beyond the Git commit. ADR-007 established workflow definitions as a first-class resource; this initiative governs **how changes to that resource happen**.

The model: workflow edits flow through a planning-mode Run, exactly like artifact creation under ADR-006. `workflow.create` auto-branches and opens a draft; repeated edits on that branch stack commits; an approval step triggers the merge. The lifecycle itself is expressed as a workflow (`workflow-lifecycle.yaml`), seeded by `spine init-repo`, so teams that want stricter governance edit that one file.

## Motivation

- **Catches malformed changes before they land.** The structural validator runs at every commit, but domain-logic errors (wrong step sequence, incorrect actor type for a step, subtle retry policy) need human review — structurally valid but semantically wrong.
- **Audit.** A draft state + explicit approval makes the "who approved this and why" question answerable from Git + the Run record.
- **Self-applying governance.** Workflows govern work. Changes to workflows *are* work. Treating them the same collapses a special case.
- **Parallel to ADR-006 (planning runs).** No new machinery: extend `write_context` and the planning-run orchestrator; don't invent a new edit protocol.

## Scope

### In Scope — EPIC-001: Branch-Based Workflow Edits with Approval

- **ADR-008** documenting the decision: branch-based edits, approval step, Run-pinning behavior, operator bypass.
- **Seed `workflow-lifecycle.yaml`** via `spine init-repo` — a two-step workflow (`draft`, `review`) that becomes the default governing workflow for `workflow.*` operations.
- **Extend `workflow.create/update`** to accept `write_context { run_id }` and commit to the run's task branch instead of the authoritative branch.
- **Planning-run integration**: `workflow.create` without a `run_id` starts a planning Run under `workflow-lifecycle` and returns the run + branch. The approval outcome merges the branch.
- **Operator bypass**: operator role may call `workflow.create/update` with no `write_context` to commit directly to the authoritative branch (emergency fix / recovery). Reviewer role must go through a Run.
- **Docs sweep**: `api-operations.md`, `access-surface.md`, `validation-service.md`, `README.md`, integration-guide — reflect the new default flow.

### Out of Scope

- Rebasing an Active Run onto a newer workflow version. Today Runs are pinned by commit SHA (per ADR-001); a rebase operation is explicit opt-in and can be its own initiative.
- Replacing how workflow definitions are stored (they stay as versioned Git artifacts per ADR-001).
- Changing how ADR-007 resource separation works (`workflow.*` vs generic `artifact.*`).

## Success Criteria

1. A new `spine init-repo` produces a repository with `workflows/workflow-lifecycle.yaml` seeded and bound to `workflow.*` operations.
2. `workflow.create` (as a reviewer, no `run_id`) starts a planning Run, opens a branch, and returns the run + branch name. No commit lands on the authoritative branch until the approval step completes.
3. Subsequent `workflow.update` calls on the same `run_id` stack commits on the same branch.
4. On approval-outcome submission, the run merges the branch to the authoritative branch and the workflow becomes Active.
5. Operator role can `workflow.create/update` with no write_context and commit directly (documented escape hatch).
6. Existing Runs bound to prior workflow versions continue against their pinned commit — no cascade.

## Primary Artifacts Produced

- `/architecture/adr/ADR-008-workflow-lifecycle-governance.md`
- `/workflows/workflow-lifecycle.yaml` (seeded by `spine init-repo`)
- Code changes in `internal/workflow/service.go` (write_context support), `internal/gateway/handlers_workflows.go` (run_id routing), `internal/engine/` (merge-on-approval glue), `internal/auth/permissions.go` (operator-bypass rule).
- Updated architecture and user-facing documentation.

## Risks

- **Bootstrap deadlock**: editing `workflow-lifecycle.yaml` through itself can deadlock an in-progress governance change. Mitigation: operator bypass documented and allowed explicitly for this workflow.
- **Branch sprawl**: if workflow edits open branches that are never approved, branches accumulate. Mitigation: defer to standard Run cancellation; surfaced as a follow-up if it shows up in practice.
- **Confusion with ADR-006 planning runs**: artifacts use `run.start_planning` with `mode: creation`. Workflow edits use the same shape but a different default workflow. Mitigation: document clearly; reuse the orchestrator rather than forking.

## Exit Criteria

INIT-017 may be marked complete when all EPIC-001 tasks are Completed, ADR-008 is Accepted, and a `spine init-repo` → `workflow.create` → approval → merge round trip works end-to-end.
