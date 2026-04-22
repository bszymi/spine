---
id: TASK-015
type: Task
title: "Engine-owned publish step: internal execution mode + rename from commit"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-22
last_updated: 2026-04-22
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/tasks/TASK-016-workflow-publish-step-audit.md
  - type: related_to
    target: smp:INIT-001-build-spine-management-platform/epics/epic-051-runner-execution-improvements/tasks/TASK-014-remove-runner-commit-step.md
  - type: related_to
    target: smp:architecture/adrs/ADR-010-single-merge-path-spine-driven-commit.md
---

# TASK-015 — Engine-owned publish step (internal execution mode + rename from `commit`)

---

## Purpose

`engine.MergeRunBranch` (in `internal/engine/merge.go`) already does the merge that matters: it advances Spine's authoritative branch in the internal git mirror, runs the artifact-status cascade (`applyCommitStatus`), and pushes when `autoPushEnabled()`. Despite this, the `commit` step in `workflows/task-default.yaml` is still dispatched to the runner, which clones from Spine's git HTTP, re-runs `git merge --no-ff`, optionally pushes, and reports `committed` / `merge_failed` back to Spine.

That second path is redundant in the happy case and a footgun in the failure case: the rehearsal in the runner's throwaway container can fail (and gate the workflow) while the real engine merge succeeds. SMP hit this concretely on TASK-010 — the runner script's `git checkout main` aborted with `pathspec 'main' did not match any file(s) known to git` because of an assumption about clone shape, even though `engine.MergeRunBranch` would have merged cleanly.

ADR-010 (in the SMP repo) decides that there is **one merge path, and it lives in Spine**. This task is the Spine-side change that makes that possible — but done properly, not by smuggling "engine handles this" into a missing execution profile.

### Why the obvious fix is not enough

The narrow fix would be: treat `automated_only` + no execution profile as the signal that the engine, not a runner, advances the step. That works but is bad design:

- **Implicit contract.** Presence/absence of a profile quietly changes who executes the step. Adding or removing a profile by accident flips the semantics silently.
- **Wrong name.** `commit` conflates three different things: a git commit on a task branch during execution, completion of a workflow step, and the authoritative merge to main. The step is not a commit; it's a governed publication of an accepted outcome to authoritative project truth.
- **Wrong actor shape.** `mode: automated_only` + `eligible_actor_types: [automated_system]` says "any automated actor can do this," which is exactly wrong for a Spine-owned governed transition. The engine is not just another automated actor; it is *the* authority that performs the transition.
- **Cross-workflow inconsistency.** Seven of the eight workflows in `workflows/*.yaml` already model publication as engine behavior triggered by `commit:` metadata on a terminal outcome (see `adr-creation.yaml`, `adr.yaml`, `artifact-creation.yaml`, `document-creation.yaml`, `epic-lifecycle.yaml`, `task-spike.yaml`, `workflow-lifecycle.yaml`). Only `task-default.yaml` has an explicit dispatched `commit` step. The inconsistency is what TASK-016 audits; this task sets the pattern the audit will standardize on.

## Deliverable

Four coordinated changes: introduce a first-class internal execution mode, rename the step, wire the engine handler, and migrate `task-default.yaml`.

### 1. Internal execution mode (workflow schema + engine)

Introduce a dedicated execution mode for steps the Spine engine performs directly. Concrete proposal (final shape to be locked during review):

```yaml
- id: publish
  name: Publish Accepted Outcome
  type: internal                  # new step type
  execution:
    mode: spine_only              # new mode
    handler: merge                # which engine handler owns the step
```

- `type: internal` — distinct from `automated`, `manual`, `review`. Signals the step is not actor-dispatched.
- `execution.mode: spine_only` — validator rejects `eligible_actor_types`, `required_skills`, and any execution profile for this mode.
- `execution.handler: merge` — names which engine subsystem owns the step. For now, the only registered handler is `merge` (wired to `engine.MergeRunBranch`). A handler registry in `internal/engine` maps name → handler func; unknown handlers are a load-time validation error.
- Schema validation: `internal/workflows/validation` (or current equivalent) enforces that `mode: spine_only` appears only on `type: internal` steps, has a known `handler`, and has no actor/profile fields.
- Runtime: the dispatcher MUST NOT emit a runner dispatch event for `spine_only` steps. The scheduler's eligibility path skips them entirely.

### 2. Rename `commit` → `publish` in `task-default.yaml`

- Step `id: commit` → `id: publish`, `name: Commit Outcomes` → `name: Publish Accepted Outcome`.
- Outcome `id: committed` → `id: published`. Outcome `id: merge_failed` keeps that id (no `commit_failed` naming).
- `review.accepted.next_step: commit` → `next_step: publish`.
- Semantic meaning documented in the step `description`: "Spine promotes the reviewed/accepted outcome into authoritative project truth."

### 3. Engine handler for `publish`

`internal/engine/merge.go`:

- Extend `MergeRunBranch` (and its `failRunOnMergeError` / `retryMerge` siblings) to call the workflow advance API at the end of their flow:
  - On success: advance the `publish` step with outcome `published`.
  - On permanent failure: advance with outcome `merge_failed`.
  - On transient failure (`git.GitError.IsRetryable`): do NOT advance — the scheduler retry path keeps the run in `committing` and re-tries; the step status stays `assigned` to the engine "actor" until success.
- Register the merge handler under the name `merge` in the engine handler registry so workflow validation resolves `execution.handler: merge`.
- Define a stable engine actor identity for the audit trail (e.g., `actor-engine-merge`). The step execution record must make it obvious that the engine, not a runner, produced the outcome.

`internal/scheduler/`:

- The commit retry loop (`recovery.go:261` / `:291`) keeps working unchanged for transient failures. Its only adjustment is that `spine_only` steps are never dispatched to a runner — the loop just re-invokes the engine handler.

### 4. Close the implicit-contract loophole

An `automated_only` step with no execution profile is a **validation error** going forward — not silent engine handling. This prevents anyone from re-introducing the "no profile means engine handles it" pattern. Workflows that need engine ownership must use `type: internal` + `mode: spine_only` explicitly.

## Acceptance Criteria

- A run on `task-default.yaml` completes end-to-end with zero runner dispatch events for the `publish` step.
- Workflow schema validation accepts `type: internal` + `execution.mode: spine_only` + `execution.handler: merge`, and rejects:
  - `mode: spine_only` on non-internal step types;
  - unknown `handler` values;
  - `eligible_actor_types` / `required_skills` on internal steps;
  - `automated_only` + no execution profile (now a validation error, not engine-handled by omission).
- Step execution audit shows the engine actor (`actor-engine-merge` or chosen id) as producer of the `published` outcome.
- Permanent merge failure produces `merge_failed`, sends the run back to `execute`, and is observable via existing failure surfaces (no regression vs today).
- Transient merge failure (per `git.GitError.IsRetryable`) keeps the run in `committing` and retries via the scheduler — same behaviour as today.

## Tests

- **Unit:** `engine/merge_test.go` covers the new "advance workflow step on success/failure" call paths, handler registry lookup, and engine-actor identity.
- **Schema validation:** table-driven tests in `internal/workflows/validation` (or equivalent) cover the accept/reject cases listed above.
- **Scenario:** `scenariotest/scenarios/standard_run_test.go` exercises a full task-default run end-to-end, asserting the `publish` step has exactly one execution and is advanced by the engine actor (no runner dispatch event for the step id).

## Dependencies / sequencing

- Ship this task FIRST. It establishes the internal execution mode and the engine handler contract.
- TASK-016 (`Workflow publish-step audit`) runs in parallel or immediately after: audits the seven other workflows, standardizes on the explicit `publish` pattern where appropriate, and decides whether the implicit `commit:`-on-terminal-outcome convention is kept, deprecated, or removed.
- SMP TASK-014 (`Remove runner commit step`) follows: deletes `commit.yaml` and the runner branch in SMP. Note: that task may need its own rename to reflect `publish`/`published` terminology — flag to SMP maintainers.

## Out of scope

- Changing how Spine pushes (push code stays where it is in `o.git.Push`). The credential supply path (ADR-009 §Long-term HTTP resolver) is a separate concern; this task uses whichever credential mechanism Spine is on at the time.
- Generalising `spine_only` to steps other than publish/merge. The handler registry leaves room for future internal steps, but no others are introduced here.
- Auditing and migrating the other seven workflows — that is TASK-016.

## Open questions (resolve during implementation)

- **Naming: `publish` vs `merge`.** `publish` is Spine-language (promotes to authoritative truth); `merge` is git-language. Current draft uses `publish`. Lock in before writing the schema.
- **Handler syntax: `execution.handler: merge` vs `execution.internal_executor: spine`.** Current draft uses `handler` because it's shorter and leaves room for multiple named handlers. Revisit if a second internal handler appears on the horizon.
