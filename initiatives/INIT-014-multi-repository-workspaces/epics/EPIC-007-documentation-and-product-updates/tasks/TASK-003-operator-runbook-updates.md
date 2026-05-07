---
id: TASK-003
type: Task
title: Operator runbook updates for multi-repo lifecycle
status: Completed
acceptance: Approved
acceptance_rationale: |
  /docs/operator-runbook.md ships with eight sections covering the
  full operator-facing surface: prerequisites + capabilities + the
  three migrations runs need (023 / 024 / 025); registering a code
  repository (CLI + API examples with X-Workspace-ID header,
  reserved-ID list, catalog/binding split per ADR-013, full v0.x
  deployment-gap accounting); inspecting catalog vs runtime binding
  state with the primary-branch-substitution note for customized
  authoritative branches; partial-merge recovery as a validated
  end-to-end walkthrough (run inspect -o json since the default
  table view hides merge_outcomes; failure_class / failure_detail
  read; retry vs resolve decision via Orchestrator.RetryRepositoryMerge
  or ResolveRepositoryMergeExternally; scheduler auto-resume via
  retryCommittingRuns gated on codeRepoOutcomesAllowResume; cancel
  path; failure modes); credential rotation pinned to the only
  supported v0.x operation (value rotation behind canonical
  secret-store://workspaces/<ws>/git ref + no-op PUT to evict the
  gitpool cache keyed on local_path/credentials_ref/UpdatedAt);
  deregistering with the deactivate-only / no-reactivation-API
  honesty note and the NopRunReferenceChecker caveat that defeats
  the gate in v0.x; consolidated failure-mode reference; and
  cross-references to internal/gateway/routes.go (canonical route
  map until api/spec.yaml gains these surfaces). All ACs satisfied:
  step-by-step entries with example commands (#1), failure modes
  documented including unresolved credential / inactive repo /
  conflicting merges (#2), links to ADR-013/014/015 +
  ADR-010/011 and to the API/CLI references (#3), validated
  end-to-end partial-merge walkthrough (#4). Codex 2 passes clean
  back-to-back on the 13th and 14th attempts — extended cascade
  for a doc-only PR because every claim in the runbook had to be
  cross-checked against actual code (capability strings, route
  registrations, response field names, gitpool cache key
  composition, secret-store URI scheme constraints, scheduler gate
  semantics, validation-vs-precondition layer separation, and
  five separate v0.x wiring gaps in stock spine serve).
last_updated: 2026-05-07
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: documentation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/tasks/TASK-002-architecture-docs-sync.md
---

# TASK-003 - Operator Runbook Updates for Multi-Repo Lifecycle

---

## Purpose

Give operators the runbook they need to register code repositories, recover from partial-merge runs, rotate credentials, and deregister repos cleanly.

## Deliverable

Add or update operator-facing documentation covering:

- Registering a code repository through the API and CLI, including credential reference setup.
- Inspecting catalog vs runtime binding state.
- Recovering from a partial-merge run via the EPIC-005 manual-resolution and retry path.
- Rotating credentials referenced by a runtime binding without disrupting in-flight runs.
- Deregistering a repository.

## Acceptance Criteria

- Each lifecycle operation has a step-by-step runbook entry with example commands.
- Failure modes are documented (unresolved credential, inactive repo, conflicting merges).
- Runbook entries link to the relevant API/CLI reference and ADRs.
- A validated end-to-end runbook walkthrough exists for the partial-merge recovery flow.
