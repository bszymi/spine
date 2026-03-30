---
id: ADR-006
type: ADR
title: "Planning Runs for Governed Artifact Creation"
status: Accepted
date: 2026-03-30
decision_makers: Spine Architecture
---

# ADR-006 — Planning Runs for Governed Artifact Creation

---

## Context

Spine's Constitution requires that all work occurs through defined workflows (§4) and that execution paths are reconstructible from artifact history (§7). However, artifact creation currently bypasses governance entirely — new initiatives, epics, tasks, and ADRs are committed directly to `main` without a governing workflow.

This creates a chicken-and-egg problem: a workflow run requires a governing artifact to already exist on the authoritative branch, but the artifact we want to create does not yet exist. The standard `StartRun()` method validates that the target task artifact is present and resolvable before creating a run. There is no mechanism for a run to create the artifact it will govern.

The consequence is that the most structurally significant act in the system — introducing new governed artifacts — is the one act that escapes governance. This contradicts the Constitution's explicit intent requirement (§3) and governed execution mandate (§4).

---

## Decision

### 1. Introduce `RunMode` Domain Concept

Add a `RunMode` type to the domain model with two variants:

- **`standard`** — the existing behavior. A run executes against an artifact that already exists on `main`. This is the default and preserves full backward compatibility.
- **`planning`** — a new mode where the run creates the artifact on a branch. The artifact does not need to exist on `main` before the run starts.

The `mode` field is stored on the `Run` entity and persisted in the database via a new migration.

### 2. Add `StartPlanningRun()` Engine Method

A new `StartPlanningRun()` method on the orchestrator handles planning run creation. This is a separate method from `StartRun()` — the existing `StartRun()` is not modified in any way.

`StartPlanningRun()` accepts:

- The artifact type to create (e.g., Initiative, Epic, Task, ADR)
- Initial artifact content (frontmatter and body)
- Optional parent artifact path (for child artifacts)

The method:

1. Resolves the governing workflow for the artifact type using the `mode: creation` binding (see §5 below)
2. Generates a run ID and branch name (`spine/run/{runID}`)
3. Creates the Run in `pending` status with `mode: planning`
4. Creates the Git branch
5. Writes the initial artifact to the branch using the `ArtifactWriter` interface
6. Creates the entry step execution
7. Transitions the run to `active`

### 3. Generic `artifact-creation.yaml` Workflow

A single `artifact-creation.yaml` workflow definition governs creation of artifact types that share the Draft → Pending lifecycle: Initiative, Epic, and Task. Product and ADR have different status models and require type-specific creation workflows if planning run support is added for them later. The workflow defines the following steps:

- **draft** — the actor elaborates the artifact content on the branch. For initiatives and epics, this includes creating child artifacts on the same branch.
- **validate** — an automated step that runs cross-artifact validation (Constitution §11) to verify the new artifact is consistent with the existing governed context.
- **review** — a human review step where the artifact (and any child artifacts on the branch) is approved, rejected, or sent back for revision.
- **merge** — on approval, the branch is merged to `main` following the existing commit path.

Rejection loops back to `draft` for rework. The artifact never appears on `main` until the review is approved and the merge succeeds.

### 4. Workflow `mode` Field

Workflow definitions gain a new optional `mode` field with two values:

- **`execution`** — the workflow governs execution against existing artifacts (current behavior)
- **`creation`** — the workflow governs the creation of new artifacts

When `mode` is absent, it defaults to `execution` for backward compatibility.

The workflow resolver uses the mode to select the correct workflow binding:

- `StartRun()` resolves to workflows with `mode: execution` (or absent mode)
- `StartPlanningRun()` resolves to workflows with `mode: creation`

This keeps the two concerns cleanly separated without requiring per-type creation workflows.

### 5. Automated Validation Step

The creation workflow includes a `validate` step of type `automated` that runs cross-artifact validation before human review. This step:

- Validates the new artifact's frontmatter against the artifact schema
- Checks cross-artifact consistency (Constitution §11) — for example, verifying that parent references are valid and required fields are populated
- Fails the step if validation errors are found, surfacing them for correction before review

This ensures reviewers see only structurally valid artifacts, reducing review burden and catching errors early.

### 6. Merge Trigger Model

Planning runs follow the existing merge path: when the review step completes with approval, the run transitions to `committing` status. `MergeRunBranch()` then merges the planning branch to `main` using merge-commit strategy. The scheduler handles merge retries for transient failures (e.g., concurrent merges).

This is not a new mechanism. Planning runs reuse the existing merge infrastructure identically to standard runs. The only difference is that the planning branch may contain multiple new artifacts (the primary artifact plus any child artifacts created during the draft step).

### 7. Write Context Relaxation

For standard runs, `resolveWriteContext()` requires both `run_id` and `task_path`, and validates that the task path matches the run's task. This ensures writes are scoped to the run's governing artifact.

For planning runs, `resolveWriteContext()` accepts `run_id` alone — `task_path` is not required. The run owns a constrained creation scope on the branch, and multiple new artifacts may be created on it during the draft step. The branch name is still resolved from the run, preserving the branch-scoped isolation guarantee.

The relaxation applies only when the run's mode is `planning`. Standard runs retain the existing strict validation.

### 8. Planning Mode Write Constraints

Planning runs are strictly bounded to artifact creation. A planning run may only:

- **Create new artifacts** — it may not update, delete, or mutate pre-existing artifacts on `main`
- **Create artifacts under allowed repository root paths** for the target artifact type (e.g., initiatives under `/initiatives/`, workflows under `/workflows/`)
- **Create only artifact types explicitly permitted** when the planning run is started — the permitted types are declared at run creation and enforced on every write
- **Optionally create child artifacts** under the declared parent/root artifact path (e.g., epics and tasks under a new initiative)

A planning run may **not**:

- Update, delete, or mutate unrelated pre-existing artifacts
- Write to paths outside the allowed root paths for the declared artifact types
- Create artifact types not declared at run start

These constraints are enforced by the write context validation layer, not by convention. If a future need arises for planning runs that modify existing artifacts, a separate ADR must explicitly introduce that capability.

### 9. Lifecycle Boundary

Planning runs exist solely for artifact introduction. Once a planning run completes and its branch is merged to `main`, the created artifacts are fully governed artifacts like any other. All subsequent modifications — status transitions, content updates, child artifact additions — must occur through standard runs (`StartRun()`) governed by the appropriate `mode: execution` workflows.

This separation is intentional and must be preserved:

- **`StartRun()`** = execute against an already-governed existing artifact
- **`StartPlanningRun()`** = create new governed artifact(s) through a dedicated creation workflow

Planning mode is not for edits, maintenance, or general work. It is a bounded creation capability, not a branch-wide write exemption.

---

## Consequences

### Positive

- Artifact creation is governed by workflows, fulfilling Constitution §4
- Creation history is fully traceable through run artifacts and branch history, fulfilling Constitution §7
- A single generic workflow covers all artifact types, avoiding per-type workflow proliferation
- Existing `StartRun()` behavior is completely unchanged — zero risk of regression
- Automated validation catches structural errors before human review
- The design reuses existing infrastructure (branching, merging, write context, state machine) rather than introducing parallel mechanisms
- Write constraints make planning mode a bounded creation capability — it cannot be stretched into general-purpose branch editing

### Negative

- Adds complexity to the domain model (`RunMode`, `mode` field on workflows, relaxed write context rules)
- Planning runs require understanding a new concept — operators must learn when to use `StartPlanningRun()` vs `StartRun()`
- The `ArtifactWriter` interface adds a new dependency to the orchestrator

### Neutral

- Existing runs are unaffected — the `standard` mode is the default and all current behavior is preserved
- The workflow `mode` field is optional and backward compatible
- Planning runs use the same state machine, the same merge path, and the same branch lifecycle as standard runs
- Once artifacts are merged to `main`, they are indistinguishable from artifacts that were created before planning runs existed — the creation mode is not a permanent property of the artifact

---

## Alternatives Considered

### A. Raw Branch Writes Without a Governing Run

Create artifacts by committing directly to a feature branch, then merging via pull request — bypassing the engine entirely.

Rejected because this circumvents all workflow governance. There would be no run entity, no step tracking, no automated validation, and no audit trail. This is essentially the current state, formalized as a process.

### B. Modified `StartRun()` With Optional Task Path

Make the `task_path` parameter optional in `StartRun()`. When omitted, the run creates the artifact instead of executing against it.

Rejected because it overloads `StartRun()` with two fundamentally different semantics, increasing the risk of regressions in existing behavior. The conditional logic would make the method harder to reason about and test. A separate method with a clear name communicates intent better.

### C. Per-Type Creation Workflows

Create separate workflow definitions for each artifact type: `create-initiative.yaml`, `create-epic.yaml`, `create-task.yaml`, etc.

Rejected because the creation lifecycle is the same for all artifact types (draft, validate, review, merge). Per-type workflows would duplicate step definitions and require maintaining multiple nearly identical files. The generic `artifact-creation.yaml` with the `mode: creation` binding achieves the same outcome with a single definition.

### D. New `ArtifactRequest` Entity

Introduce a separate entity type that represents a request to create an artifact, with its own lifecycle and approval flow outside the run system.

Rejected because it would create a parallel governance mechanism alongside runs. This adds significant complexity — a new entity, new state machine, new storage, new API — while runs already provide the exact infrastructure needed: branches, steps, review, and merge.

---

## Links

- Constitution: `/governance/constitution.md` (§3 Explicit Intent, §4 Governed Execution, §7 Reproducibility, §11 Cross-Artifact Validation)
- Initiative: `/initiatives/INIT-006-governed-artifact-creation/initiative.md`
- Workflow Definitions: `/workflows/`
- Engine State Machine: `/architecture/engine-state-machine.md`
- ADR-001: `/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md`
