---
id: EPIC-006
type: Epic
title: "Cross-Repo Execution Evidence"
status: Completed
acceptance: Approved
acceptance_rationale: |
  All seven tasks completed and approved on main: TASK-001 (evidence
  schema + canonical YAML on-disk format), TASK-002 (validation
  policy schema + ADR linkage), TASK-003 (check runner with
  POSIX-process lifecycle + Result→CheckResult normalization),
  TASK-004 (EV-* validation rule family wired into the engine),
  TASK-005 (production query layer: loader, summary, querier,
  gateway accessor, CLI inspect rendering), TASK-006 (scenariotest
  end-to-end coverage of all five EV-* rule classes plus query
  visibility), and TASK-007 (architecture/execution-evidence.md
  with full schema + AC mapping). Cross-repo evidence is now
  observable via run inspect, blocking-check failures gate publish,
  warning-severity policy failures surface as warnings without
  blocking, missing evidence is visible before publish, and the
  governance authority remains on the primary repo while code repos
  produce deterministic evidence. Production wiring of the EV-*
  resolvers into the gateway validation engine is intentionally
  deferred to a follow-on epic (EPIC-007) that will plumb resolver
  construction through the workspace ServiceSet and surface
  evidence-aware validation in the run.precondition path.
last_updated: 2026-05-06
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
owner: bszymi
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
---

# EPIC-006 - Cross-Repo Execution Evidence

---

## Purpose

Prove that code repository changes satisfy governed intent without turning code repositories into governance authorities.

The primary repo remains the ledger. Code repos produce deterministic evidence: changed commits, check results, policy results, and ADR-linked validation outcomes.

---

## Scope

### In Scope

- Execution evidence model recorded in the primary repo
- Validation policy artifacts linked from ADRs or architecture docs
- Per-repo check execution and result capture
- Workflow preconditions for required evidence
- Reporting and query support for evidence status

### Out of Scope

- AI-only semantic validation as a blocking rule
- Full source-code indexing
- Build or deployment orchestration beyond invoking declared checks

---

## Primary Outputs

- Evidence schema and storage location
- ADR-linked validation policy format
- Check runner integration boundary
- Validation rules that consume evidence
- End-to-end evidence scenario tests

---

## Acceptance Criteria

1. A task can require evidence for each affected repository.
2. ADRs can link to deterministic validation policies.
3. Required checks produce structured results tied to repo, branch, and commit.
4. Missing or failed required evidence blocks publication.
5. Evidence is auditable from primary-repo history.

Evidence is keyed by `(execution_id, repository_id)` where `repository_id` is the single value resolved per execution under [ADR-015](/architecture/adr/ADR-015-multi-repo-step-routing.md). One execution row attributes to exactly one repository — no N-way evidence aggregation across fanned-out step instances is required.
