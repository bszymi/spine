---
id: TASK-016
type: Task
title: "Workflow publish-step audit: standardize authoritative merge across all workflows"
status: Cancelled
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

## Resolution — Cancelled

**Outcome: no code changes needed. Task cancelled on 2026-04-22 after Phase 1 audit.**

The audit (Phase 1 below) found that runner dispatch for the authoritative merge is already **zero across all 8 workflows in `workflows/*.yaml`** as of commit `e146210`. The only workflow that ever dispatched a runner for merge was `task-default`, and TASK-015 removed it. The other 7 workflows were never broken — they've always used implicit `commit:` metadata on a terminal outcome, which the engine's `CompleteRun(hasCommit=true) → MergeRunBranch` path has always handled directly.

The "two different patterns across workflows" framing that motivated this task turned out to conflate two separate concerns:

1. *Who performs the merge?* (Uniformly: the engine. Always has.)
2. *How visible is the merge in the audit trail?* (Explicit publish step → extra step-execution event. Implicit commit metadata → merge subsumed into `EventRunCompleted`.)

Concern (1) is solved. Concern (2) is a matter of preference, and the audit found that each workflow's current shape matches what it needs: `task-default` benefits from the explicit step (the review acceptance and the publication are distinct governance events), and the 7 implicit workflows do not (their review outcome literally is the governance decision).

Shipping Phase 3 under any of (A/B/C) would have added maintenance surface (YAML churn in A, reversal of just-shipped TASK-015 work in B, speculative validator rules in C) without fixing a concrete problem.

The audit content below is retained as the record of what was examined. Anyone revisiting the shape-of-publication question should start here.

### If this comes up again

Reopen only if one of these is true:
- A new workflow genuinely needs per-merge audit granularity that `EventRunCompleted` doesn't provide.
- Someone attempts to add a new runner-dispatched merge step (the TASK-015 validator loophole-close — `type: automated` with no execution block — already prevents the specific pattern that bit `task-default`).
- A workflow author is unsure which pattern to follow, at which point the hybrid rule articulated in Phase 2 below (task-like → explicit publish, artifact-like → implicit commit) can be promoted to a short authoring doc. Not worth shipping preemptively.

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

#### Audit table

Scope: all eight workflows in `workflows/*.yaml` as of commit `e146210`. SMP workflow files are not audited here — flagged for SMP follow-up.

| Workflow | Applies to | Terminal merging outcomes | Merge trigger | Intermediate commit metadata | Non-merging end outcomes | Runner dispatch for merge? |
|---|---|---|---|---|---|---|
| `task-default` | Task | `publish.published` | **Explicit publish step** (type: internal, handler: merge — added in TASK-015) | `review.accepted` → commit:{status: Completed} flows through to publish | none | **No** (engine-handled) |
| `artifact-creation` | Initiative, Epic, Task | `review.approved` (→ end, commit:{status: Pending}) | Implicit | none | none | No (no merge step existed) |
| `adr-creation` | ADR | `review.accepted` (→ end, commit:{status: Accepted}), `review.rejected` (→ end, commit:{status: Deprecated}) | Implicit (two distinct terminal merges) | none | none | No |
| `adr` | ADR | `evaluate.accepted` (→ end, commit:{status: Accepted}), `evaluate.deprecated` (→ end, commit:{status: Deprecated}) | Implicit (two distinct terminal merges) | none | none | No |
| `document-creation` | Governance, Architecture, Product | `review.approved` (→ end, commit:{status: Living Document}) | Implicit | none | none | No |
| `epic-lifecycle` | Epic | `review.completed` (→ end, commit:{status: Completed}) | Implicit | `plan.planned` → commit:{status: In Progress} attaches to a non-terminal outcome (next_step: execute). Sets run.CommitMeta without triggering a merge; the value is later overwritten by review.completed's Completed before the terminal merge. | none | No |
| `task-spike` | Task | `investigate.inconclusive` (→ end, commit:{status: Completed}), `review.accepted` (→ end, commit:{status: Completed}) | Implicit (two distinct terminal merges) | none | `needs_more_investigation` (loopback to investigate) | No |
| `workflow-lifecycle` | Workflow | `review.approved` (→ end, commit:{merge: "true"}) | Implicit (uses non-status `merge: "true"` metadata because workflow YAML has no frontmatter status) | none | none | No |

#### Answering the four audit questions

1. **Is there a governed merge to the authoritative branch?** Every workflow in `workflows/*.yaml` has at least one terminal outcome that triggers the engine's commit pipeline (either via explicit publish step or implicit `commit:` on terminal outcome).
2. **Explicit vs implicit?** 1/8 explicit (`task-default`, post-TASK-015). 7/8 implicit.
3. **Non-merging end outcomes?** None found. Every `next_step: end` outcome in the codebase carries `commit:` metadata. No workflow currently has an outcome that routes to end without triggering a merge.
4. **Workflows assuming runner execution for engine-handleable steps?** Only `task-default`'s old `commit` step (already removed in TASK-015). No other workflow dispatches a runner for a merge. `artifact-creation`'s `validate` step (type: automated, automated_only) IS engine-handleable in spirit but currently runs as an automated actor-dispatched step — not in scope here since it performs validation, not merge.

#### Additional observations

- **Two-status workflows.** `adr-creation`, `adr`, and `task-spike` each have **multiple terminal merging outcomes** with different target statuses. A hypothetical single `publish` step per workflow would need to preserve per-outcome status — the commit metadata would still travel on the review-step outcome (matching the task-default pattern: `review.accepted` carries `commit:{status: Completed}` and routes to `publish`).
- **Mid-flow commit metadata.** `epic-lifecycle.plan.planned` is the only outcome that attaches `commit:` metadata to a non-terminal next_step. It sets run.CommitMeta early; by the time the terminal `review.completed` merges, that slot has been overwritten by `commit:{status: Completed}`. This is working as designed (the early value is a "mid-flow status cascade" for display/projection purposes — though the cascade only runs at merge time, so it's effectively dead metadata). Not a publish-step concern.
- **No runner dispatch for any existing merge.** After TASK-015, runner dispatch for the authoritative merge is zero across the codebase.
- **`workflow-lifecycle` edge case.** Uses `commit: {merge: "true"}` because workflow YAML files have no frontmatter `status:` to rewrite. The engine's `applyCommitStatus` only acts when `commit.status` is set; the `merge` key is a signal-only "yes, merge this run" marker. Any standardization must handle this.

### Phase 2 — Decision

Pick ONE of:

- **(A) Standardize on explicit `publish` step.** Every workflow that does a governed merge gets an explicit `publish` internal step. The `commit:` metadata stays on the outcome (it's still how the engine knows the post-merge artifact status), but the merge is a distinct step rather than a hidden side effect. Workflows whose terminal outcomes don't merge (e.g., `needs_revision` loopbacks) are unaffected.
- **(B) Standardize on implicit engine behavior.** Remove the `publish` step from `task-default.yaml` (undoing part of TASK-015) and put `commit: { status: Completed }` on `review.accepted` with `next_step: end`, matching the seven other workflows. Internal execution mode stays in the platform (future-proofing), but no workflows use it yet.
- **(C) Hybrid, deliberate.** Keep both patterns but document which applies when (e.g., explicit for task-like workflows that have a dedicated execute step; implicit for creation workflows). Write validation that enforces the chosen rule per workflow category.

Strong priors from the design review: **(A) is the most consistent with Spine's explicit-governance philosophy.** (B) is the least code. (C) is the most realistic if audit findings reveal categories of workflows that genuinely behave differently.

#### Decision framework (post-audit)

The audit surfaced one important fact that reframes the decision: **runner dispatch for authoritative merge is already zero across all 8 workflows after TASK-015**. The implicit pattern (commit: metadata on terminal outcome → end) was never a "runner is involved in the merge" problem — the engine's `CompleteRun(hasCommit=true)` → `MergeRunBranch` path has always been engine-driven. The runner-dispatch problem was specific to `task-default`'s old dedicated `commit` step.

So the residual question TASK-016 has to answer is **not** "is publication engine-owned?" (answer: always was, still is). It is: **how visible and auditable should the moment of publication be?**

Two sub-questions, both answerable from the audit:

1. **Does the implicit pattern make the merge invisible?** Partly. A reader of a workflow YAML sees `commit: {status: X}` on a terminal outcome and can infer "this status change is committed when this outcome fires." That is already a visible, declarative governance signal — it's only the _mechanical_ merge step that's implicit. The run record shows `EventRunCompleted` at the moment of completion; it does not show a distinct "publish" step execution for implicit workflows. That loss of audit granularity is the actual cost.
2. **Is the explicit publish step carrying its weight?** In `task-default`, yes — the step execution record names the engine actor, the outcome, and separates the merge event from the review acceptance event in the audit trail. For workflows where the review outcome IS the governance decision and nothing else of substance happens between review and merge (artifact-creation, adr-creation, adr, document-creation, workflow-lifecycle, epic-lifecycle), a publish step would emit one extra audit event per run with no incremental information — the engine actor is already implicit in `EventRunCompleted`, and the outcome's status is on the review outcome that preceded it.

**Recommendation: (C) Hybrid, with the rule articulated as audit-driven.**

Rule: **A workflow MUST have an explicit `publish` internal step when its review-stage outcome does NOT already fully capture the governance decision**, i.e. when the merge represents a state change that the review outcome does not name. Today that matches `task-default` only — the review accepts "the deliverable is good"; the publish step commits "and here is the engine publishing it to main." For the other seven workflows the review outcome literally IS the governance decision (approve/reject/accept/deprecate/complete); the merge is mechanical.

Formally:
- If the workflow has a step of type `manual` or `hybrid` that produces deliverable files on the run branch (beyond the artifact itself) whose acceptance is distinct from their publication, use an explicit `publish` step.
- Otherwise, use implicit `commit:` metadata on the terminal review outcome.

In practice, this is "task-like" vs "artifact-like":
- Task-like: `task-default` (and future task-* variants that land a deliverable). Explicit publish.
- Artifact-like: `artifact-creation`, `adr-creation`, `adr`, `document-creation`, `workflow-lifecycle`, `epic-lifecycle`. Implicit.
- Hybrid / open question: `task-spike`. Produces findings + summary as deliverables (like task-default), but they are part of the task artifact rather than separate deliverable files. Leaning implicit because the terminal outcome _is_ the governance decision, but this is the boundary case.

Why not A: would add 10–15 lines of YAML to each of 7 workflows with no incremental audit value, for a merge path the engine already owns uniformly.

Why not B: would throw away TASK-015's engine-actor audit-trail property for `task-default` with no benefit. The hybrid rule keeps the valuable part (explicit visibility when the merge is a distinct governance event) and pays the cost (an extra step) only where it earns its keep.

Why C over a vague "both are fine": Spine's whole premise is explicit governed execution. "It depends what feels right" invites drift. Writing down the rule now — even if not enforced by validation in this task — gives future workflow authors a principle to follow.

#### Decision: **C (Hybrid, audit-driven rule)** — pending user confirmation.

Rationale: see above. The audit found no workflow whose current shape is wrong — only task-default's old commit step, which TASK-015 already fixed. The 7 implicit workflows are clean. TASK-016's job is to ratify that, write down the rule, and ensure nothing drifts.

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
