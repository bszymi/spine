---
id: TASK-002
type: Task
title: Sync architecture docs with shipped multi-repo behavior
status: Completed
acceptance: Approved
acceptance_rationale: |
  Architecture docs now reflect the shipped multi-repo behavior:
  engine-state-machine.md adds the partially-merged Run state to
  §2.1 with three transition rows in §2.2 (committing →
  partially-merged via git.code_repo_partial_failure;
  partially-merged → committing via git.retry_partial_merge;
  partially-merged → cancelled via run.cancel) and a recovery row
  in §2.4 that points operators at the two supported orchestrator
  recovery APIs. runtime-schema.md §4.1 adds partially-merged to
  the runs.status comment + CHECK constraint matching migration 023.
  error-handling-and-recovery.md gains §5.4 with the full
  partial-merge recovery contract (non-terminal classification,
  resolve-and-resume vs cancel paths, scheduler gate on
  codeRepoOutcomesAllowResume, branch preservation semantics, and
  explicit callouts that `merged` is Spine-only and `skipped` is
  internal-only — operator recovery flows only through
  RetryRepositoryMerge and ResolveRepositoryMergeExternally).
  components.md §4.2 tightens the Artifact Service responsibilities
  to clarify primary-only governance artifact authority while
  documenting multi-repo merge coordination, with cross-references
  to ADR-013 and ADR-015. execution-evidence.md §1 adds ADR-013 +
  ADR-014 cross-references; the placeholder filename
  ADR-014-evidence.md is replaced with the real
  ADR-014-validation-policy-as-governed-artifact.md across three
  occurrences (execution-evidence.md §4.4, validation-policy.md
  §6.3 + §8.4 example). AC #1 (every INIT-014 ADR linked from at
  least one architecture doc) and AC #2 (no out-of-scope behavior
  referenced — verified via grep audit of "atomic transaction",
  "two-phase commit", "submodule", "subtree", "mirror",
  "monorepo migration", "per-repo permission") both satisfied.
  Codex 2 passes clean back-to-back on the 11th and 12th attempts —
  high pass count for a doc-only PR because a normative recovery
  walkthrough surfaced multiple latent doc-vs-code drifts in the
  partial-merge recovery surface (scheduler auto-resume vs operator
  trigger; branch-tip-only fix is insufficient; supported-target
  status set; operator-API-only resolution path; code repos as
  execution-targets only).
last_updated: 2026-05-07
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: documentation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/tasks/TASK-001-product-definition-multi-repo-update.md
---

# TASK-002 - Sync Architecture Docs with Shipped Multi-Repo Behavior

---

## Purpose

Reconcile `/architecture/git-integration.md`, `/architecture/multi-repository-integration.md`, and adjacent component docs with the behavior actually delivered across EPIC-001 through EPIC-006.

## Deliverable

Update architecture documentation so:

- Single-repo assumptions are removed wherever multi-repo is now the default behavior.
- The git client pool, lazy clone, and credential resolution flow are documented.
- Run lifecycle, step routing (per the EPIC-004 routing ADR), and merge ordering match the implementation.
- Validation policy artifacts and execution evidence are documented in the relevant sections.
- Diagrams and examples reflect the final shipped behavior.

## Acceptance Criteria

- All ADRs produced under INIT-014 are linked from at least one architecture doc.
- No architecture doc references behavior that was scoped out of INIT-014.
- Single-repo behavior is documented as the backward-compatible default.
- Cross-component terminology is consistent (catalog vs binding, primary vs code, etc.).
- Diagrams match the running implementation.
