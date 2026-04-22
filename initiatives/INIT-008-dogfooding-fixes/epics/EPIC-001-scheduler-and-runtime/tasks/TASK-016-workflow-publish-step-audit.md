---
id: TASK-016
type: Task
title: "Workflow publish-step audit: standardize authoritative merge across all workflows"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-22
last_updated: 2026-04-22
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/tasks/TASK-015-engine-driven-commit-outcome.md
---

# TASK-016 — Workflow publish-step audit

---

## Purpose

TASK-015 introduces a first-class internal execution mode (`type: internal`, `execution.mode: spine_only`, `execution.handler: merge`) and renames the `commit` step in `task-default.yaml` to `publish`. That fixes the immediate runner-dispatch problem but does not address a deeper inconsistency the design review surfaced:

Across `workflows/*.yaml`, publication to authoritative truth is modeled **two different ways**:

1. **Explicit step (1 workflow).** `task-default.yaml` has a dedicated `commit` step that the scheduler advances. After TASK-015, this becomes a `publish` internal step.
2. **Implicit engine behavior (7 workflows).** `adr-creation.yaml`, `adr.yaml`, `artifact-creation.yaml`, `document-creation.yaml`, `epic-lifecycle.yaml`, `task-spike.yaml`, `workflow-lifecycle.yaml` attach `commit: { status: ... }` metadata to a terminal outcome whose `next_step: end`. The merge is never a step at all — the engine performs it as a side effect of terminal-outcome advancement.

Both patterns produce the same git outcome. The difference is in observability and governance:

- The explicit pattern makes the authoritative merge a visible, auditable, retriable, timeout-bounded step in the run record.
- The implicit pattern makes the merge a side effect buried in engine code. A reader of a workflow file cannot tell that publication is happening; a reader of a run record sees the run go from `review.approved → end` with no indication that a merge occurred.

Spine's value proposition is explicit governed execution. The implicit pattern undercuts that. This task audits, decides, and standardizes.

## Deliverable

Three phases: audit, decision, migration.

### Phase 1 — Audit

Produce a table in this task (or a linked doc) covering every workflow in `workflows/*.yaml`:

| Workflow | Terminal outcomes | Merge trigger | Status transitions on merge | Runner dispatch today? |

For each workflow, answer:

1. **Is there a governed merge to the authoritative branch?** (i.e., does the engine's commit pipeline run?)
2. **If yes, is it modeled explicitly (step) or implicitly (outcome metadata)?**
3. **Are there outcomes that go to `end` *without* a merge?** (e.g., `rejected`, `deprecated` in `adr.yaml` — these write status changes but the semantics differ from accepted-and-published.) Which of those still trigger a merge via `commit:` metadata, and is that intentional?
4. **Does the workflow assume runner execution for any step that the engine could handle directly?** (Beyond `task-default`'s `commit` step — are there others?)

Also audit the SMP workflow files (`smp:` refs) for the same shape, since they share the execution model.

### Phase 2 — Decision

Pick ONE of:

- **(A) Standardize on explicit `publish` step.** Every workflow that does a governed merge gets an explicit `publish` internal step. The `commit:` metadata stays on the outcome (it's still how the engine knows the post-merge artifact status), but the merge is a distinct step rather than a hidden side effect. Workflows whose terminal outcomes don't merge (e.g., `needs_revision` loopbacks) are unaffected.
- **(B) Standardize on implicit engine behavior.** Remove the `publish` step from `task-default.yaml` (undoing part of TASK-015) and put `commit: { status: Completed }` on `review.accepted` with `next_step: end`, matching the seven other workflows. Internal execution mode stays in the platform (future-proofing), but no workflows use it yet.
- **(C) Hybrid, deliberate.** Keep both patterns but document which applies when (e.g., explicit for task-like workflows that have a dedicated execute step; implicit for creation workflows). Write validation that enforces the chosen rule per workflow category.

Strong priors from the design review: **(A) is the most consistent with Spine's explicit-governance philosophy.** (B) is the least code. (C) is the most realistic if audit findings reveal categories of workflows that genuinely behave differently.

The decision should be made with the audit table in hand, not up front. Document the rationale in this task file.

### Phase 3 — Migration

Depending on the Phase 2 decision:

- **(A)** For each of the seven workflows: add an explicit `publish` step before `end`, move `commit:` metadata onto the `published` outcome of that step, update `next_step` wiring accordingly. Scenario tests in `scenariotest/scenarios/*` updated to assert the publish step executes. Backward-compat: in-flight runs migrate as described in TASK-015.
- **(B)** Revert the explicit step from `task-default.yaml` (or keep it and leave unused?). Document that `commit:` on a terminal outcome is the canonical pattern. Remove `type: internal` / `mode: spine_only` from schema if nothing else will use it.
- **(C)** Per the documented rule. Add schema validation that rejects workflows violating the rule.

Also:

- If any non-publish engine-handled step is discovered in Phase 1 (e.g., some other step that could or should be `spine_only`), file a separate task for it. Do NOT expand scope here.
- Update `workflows/WORKFLOWS.md` (or create one if missing) to document the chosen pattern so future workflow authors know what to do.

## Acceptance Criteria

- Phase 1 audit table committed to this task file, covering all workflows in `workflows/*.yaml` and any SMP workflows in scope.
- Phase 2 decision (A / B / C) documented in this task file with rationale grounded in the audit findings.
- Phase 3 migration completed per the chosen option; schema validation updated to enforce the chosen pattern; scenario tests cover the new shape for every affected workflow.
- No workflow dispatches a runner for a step that the engine can handle directly (the underlying issue that motivated TASK-015).
- Workflow authoring documentation reflects the chosen canonical pattern.

## Tests

- **Schema validation:** table-driven tests enforce the Phase 2 rule.
- **Scenario:** one end-to-end scenario per affected workflow asserts the merge happens where expected (explicit step advance OR implicit engine behavior, per decision) and the artifact status landed on the authoritative branch matches `commit.status`.
- **Regression:** existing scenario tests for all seven currently-implicit workflows still pass after migration.

## Dependencies / sequencing

- Depends on TASK-015 shipping first (or at least the schema for `type: internal` + `mode: spine_only` landing), if Phase 2 chooses (A) or (C).
- If Phase 2 chooses (B), TASK-015 needs a follow-up revert for the `task-default.yaml` publish step. Coordinate before acting.
- SMP maintainers should be looped in if the audit finds SMP workflows in scope. A parallel SMP task may be needed.

## Out of scope

- Renaming `commit:` metadata on outcomes. That field name is load-bearing in engine code and tests; renaming it is a separate, larger refactor (`publish:` metadata block?) not required to fix the inconsistency.
- Introducing other `spine_only` handlers beyond `merge`. If the audit suggests them, file separately.
- Any changes to the runner's build/test steps. This task only concerns publication/merge.

## Open questions (for Phase 2)

- Do `rejected` / `deprecated` terminal outcomes (which exist in `adr.yaml` and `adr-creation.yaml`) belong to the publish pattern, or are they governed *non-publish* transitions that deserve their own shape?
- Is there a case where a workflow legitimately wants publication to be invisible/atomic with terminal-outcome advancement? (Performance? User-facing semantics?) If yes, that's an argument for (C).
- Should `commit:` metadata on an outcome that ISN'T attached to a `publish` step be a validation error after Phase 3, or tolerated for the implicit pattern in (B)/(C)?
