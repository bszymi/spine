---
id: TASK-004
type: Task
title: Route steps to target repositories
status: Completed
acceptance: Approved
acceptance_rationale: ADR-015 implemented end-to-end. step.repository on StepDefinition + repository_id on StepExecution (migration 024). Strict YAML decoding (yamlsafe.DecodeIntoStrict) and strict JSON decoding (workflow.UnmarshalProjectionDefinition) close every workflow ingestion path against silent-drop misroute. validateStepRouting at run start AND for planning runs enforces existence + active + opt-in checks with the spine exception baked in. Assignment payload exposes RepositoryID, CloneURL (always workspace git HTTP), BranchName as single-value fields. Crash-recovery (Scheduler.lookupEntryStep) preserves the routed repo. AC (i)-(ix) covered; codex 5 passes (3 with findings, last 2 clean back-to-back).
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
last_updated: 2026-05-05
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-002-create-run-branches-across-repositories.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-007-step-routing-decision-adr.md
  - type: related_to
    target: /architecture/adr/ADR-015-multi-repo-step-routing.md
---

# TASK-004 - Route Steps to Target Repositories

---

## Purpose

Tell actors and automated runners which repository a step should operate in, implementing the routing model accepted in [ADR-015](/architecture/adr/ADR-015-multi-repo-step-routing.md).

## Deliverable

Implement repository context resolution for step execution exactly as specified in [ADR-015](/architecture/adr/ADR-015-multi-repo-step-routing.md). No new design choices live in this task — it is implementation only.

The implementation must:

- Add an optional `repository` field to `StepDefinition` (per ADR-015 *Workflow step schema*).
- Add a `repository_id` field to `StepExecution`, populated at step activation by applying the ADR-015 resolution rule (`step.repository` if set, otherwise `spine`). The field is immutable for the lifetime of the row.
- Validate at run start (per ADR-015 *Validation*) that every step's resolved target repository (a) is registered in the workspace — a code repo via `/.spine/repositories.yaml`, or `spine` via the always-implicit primary entry (per [ADR-013 §2.1](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md), single-repo workspaces may omit the catalog file entirely), (b) is active — code repos require an active runtime binding, while `spine` is always considered active for as long as the workspace is, and (c) is a member of `task.repositories ∪ {spine}`. Return a typed `invalid_step_repository` error naming the offending step ID and unresolved repository when validation fails. The run is not created. The `spine` exception means default-spine runs in single-repo workspaces continue to start without any catalog file or binding row.
- Validate at workflow load that any present `repository` field matches the catalog ID format `^[a-z0-9]+(-[a-z0-9]+)*$`, max 64 chars.
- **Reconcile `domain.StepDefinition` with committed workflow YAMLs**, then **upgrade the workflow parser to strict-decode mode**. Several committed workflows (e.g., `workflows/adr-creation.yaml`, `workflows/document-creation.yaml`) already use step-level fields not present on `StepDefinition` — at minimum `description:`. Audit every committed workflow under `workflows/` and lift every used-but-undeclared step field onto `StepDefinition` (preferring declaration over stripping). Then enable `yaml.NewDecoder(...).KnownFields(true)` on the typed `StepDefinition` decode so unknown step fields are rejected at workflow load. The strict-decode upgrade MUST ship before any workflow YAML commits a `repository:` value — without strict decoding, a pre-TASK-004 binary silently drops the field and misroutes the step to `spine`. See ADR-015 *Schema versioning* for the full rollout invariant.
- Surface the resolved `repository_id`, `clone_url`, and `branch_name` in step assignment payloads (single-value fields per ADR-015 *Assignment payload shape*).
- Emit structured logs and a metric on each routing decision (step ID, resolved repository, source: explicit / default-spine).

## Acceptance Criteria

- Behavior matches [ADR-015](/architecture/adr/ADR-015-multi-repo-step-routing.md). The PR description cites the ADR path.
- Step assignments include `repository_id`, `clone_url`, and `branch_name` as single-value fields.
- Steps without an explicit `repository` resolve to `spine` (the primary repository ID reserved by ADR-013) — including in tasks with one or more code repos in `task.repositories`. The "implicit single code repo" default is **not** implemented.
- Run start fails with `invalid_step_repository` (and the run is not created) when a step's resolved repository is unknown, inactive, or absent from `task.repositories ∪ {spine}`.
- Single-repo workspaces and existing workflows continue to operate without any YAML change. Every existing workflow step omits `repository` and resolves to `spine`.
- Tests cover: (i) explicit `repository: <code-repo>` resolves to that repo, (ii) omitted `repository` resolves to `spine`, (iii) explicit `repository: spine` is accepted as equivalent to omission, (iv) unknown repo ID at run start → `invalid_step_repository`, (v) inactive repo at run start → `invalid_step_repository`, (vi) repo not in `task.repositories ∪ {spine}` at run start → `invalid_step_repository`, (vii) malformed `repository` value at workflow load → workflow validation failure, (viii) single-repo workflow on a multi-code-repo task: every step resolves to `spine` (no implicit fan-out, no implicit single-code-repo default), (ix) workflow YAML with an unknown step field (e.g., `repositorY:` or `repos:`) is rejected at workflow load (proves the strict-decode upgrade landed).

