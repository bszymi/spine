---
type: Architecture
title: Task-to-Workflow Binding Model
status: Living Document
version: "0.1"
---
# Task-to-Workflow Binding Model

---

## 1. Purpose

This document defines how artifacts are bound to workflows — how the Workflow Engine determines which workflow definition governs a given artifact, when that binding is established, and under what rules it may change.

The [Workflow Definition Format](/architecture/workflow-definition-format.md) defines the `applies_to` clause, which declares which artifact types a workflow governs. The [Data Model](/architecture/data-model.md) records `workflow_version` on each Run. This document fills the gap between those two: the resolution logic that connects an artifact to a specific workflow version at execution time.

---

## 2. Binding Model Overview

Workflow binding in Spine follows a **type-based resolution model with optional classification refinement**:

1. Every workflow declares which artifact types it governs (`applies_to`)
2. Artifacts may optionally declare a `work_type` to select among multiple workflows for the same artifact type
3. The Workflow Engine resolves the binding when a Run is created
4. The resolved workflow version is pinned to the Run for its entire lifetime

Artifacts do not carry an explicit workflow reference in their front matter. The binding is resolved at runtime, not declared at creation time. This keeps artifacts decoupled from workflow implementation details and allows workflow evolution without touching every governed artifact.

---

## 3. Decisions

### 3.1 Does every Task require an explicit workflow reference?

**No.** Tasks do not store a `workflow_id` or `workflow_path` in their front matter.

The Workflow Engine resolves the governing workflow at Run creation time using the artifact's `type` field and optional `work_type` field. This means:

- Existing artifacts are automatically governed by new or updated workflows without migration
- Workflow changes do not require touching artifact files
- The binding is a runtime resolution, not a durable declaration

The Run records the resolved workflow reference and pinned version, providing full traceability without polluting artifact metadata.

### 3.2 Do we introduce `work_type`?

**Yes.** An optional `work_type` field is added to the artifact schema for types that require workflow differentiation.

`work_type` is a free-form classification string that allows multiple workflows to govern the same artifact type for different kinds of work. Examples:

```yaml
# A standard implementation task
---
type: Task
work_type: implementation
---

# A time-boxed investigation
---
type: Task
work_type: spike
---

# A bug fix with different review requirements
---
type: Task
work_type: bugfix
---
```

When `work_type` is present, the Workflow Engine uses the `(type, work_type)` pair for resolution. When absent, the engine falls back to type-only resolution.

**Constraints:**

- `work_type` values are not governed by a fixed enum — they are workflow-defined. Each workflow declares which `work_type` values it handles.
- A `work_type` value with no matching workflow is a resolution error (see §4).
- `work_type` is optional. Omitting it is valid when only one workflow exists for the artifact type.

### 3.3 Is workflow chosen manually, by template, or by rules?

**By rules, with template defaults.**

The Workflow Engine resolves the workflow using a deterministic rule chain:

1. Read the artifact's `type` and `work_type` (if present)
2. Find all Active workflow definitions where `applies_to` includes the artifact type
3. If `work_type` is specified, filter to workflows that declare a matching `work_type` selector
4. If exactly one workflow remains, use it
5. If zero workflows match, the resolution fails — no Run can be created
6. If multiple workflows match, the resolution fails — the conflict must be resolved before execution

Templates may pre-populate `work_type` to guide authors toward the correct workflow:

```yaml
# templates/spike-template.md
---
type: Task
work_type: spike
---
```

Manual workflow selection by the user is not supported. The binding is always resolved from artifact metadata, never specified as a user parameter on `run.start`.

### 3.4 When a Run starts, how is the workflow version pinned?

**By Git commit SHA.**

The Data Model already specifies `workflow_version` as a Git commit SHA. This is the correct approach:

- The semantic version (`version` field in the workflow YAML) is a human-readable label
- The Git SHA is the exact, immutable snapshot of the workflow file at Run creation
- The Run records both for different purposes:
  - `workflow_version` (Git SHA) — for exact reproducibility and auditability
  - `workflow_version_label` (semantic version string) — for human-readable display

This means if a workflow file is edited but the `version` field is not bumped (e.g., a typo fix), different Runs may reference different Git SHAs with the same semantic version. The SHA is authoritative; the label is informational.

### 3.5 Can workflow binding change after a task is created?

**The `work_type` field can change, but only under specific conditions.**

Since tasks do not carry an explicit workflow reference, "changing the binding" means changing the `work_type` field, which may cause a different workflow to resolve on the next Run.


| Task Status                  | Can`work_type` change? | Governance requirement                                       |
| ---------------------------- | ---------------------- | ------------------------------------------------------------ |
| Draft                        | Yes                    | None — task is still being defined                          |
| Pending                      | Yes                    | Requires rationale in commit message                         |
| In Progress (has active Run) | No                     | Active Run is pinned; changing`work_type` does not affect it |
| Terminal (Completed, etc.)   | No                     | Immutable — terminal artifacts are frozen                   |

**Key principle:** Changing `work_type` does not affect any existing Run. It only affects the workflow resolved for the *next* Run. An active Run remains pinned to its original workflow version regardless of any changes to the task's metadata.

### 3.6 Governance rules for binding changes

When `work_type` changes on a Pending task:

1. The change must be committed to Git with a rationale in the commit message
2. If a Run was previously created and completed or failed under the old workflow, the history is preserved — the new Run uses the new binding
3. The Workflow Engine must validate that the new `(type, work_type)` pair resolves to a valid Active workflow before allowing a new Run

There is no approval gate for `work_type` changes. The governance control is auditability (Git history) rather than authorization (approval workflow). If stricter control is needed in the future, a dedicated workflow governing workflow-binding changes could be introduced.

### 3.7 How does the engine validate workflow-task compatibility?

The Workflow Engine performs validation at two points:

**At Run creation (`run.start`):**

1. Resolve the workflow using the `(type, work_type)` pair (per §3.3)
2. Verify the resolved workflow has status `Active`
3. Verify the artifact is in a state where a new Run is permitted (not terminal, no active Run already in progress)
4. Pin the workflow version (Git SHA) to the Run
5. Verify the artifact satisfies the workflow's entry step preconditions

If any check fails, Run creation is rejected with a specific error. No partial Runs are created.

**During execution (continuous):**

- Step preconditions are validated before each step begins
- Step validation rules are checked before accepting outcomes
- Outcome routing is enforced per the pinned workflow definition

The engine does not re-validate the workflow binding during execution. Once a Run is created, the binding is fixed.

### 3.8 How do branches inherit governance from the Run?

**Branches created during divergence inherit the Run's pinned workflow version unconditionally.**

Divergence creates parallel branch execution contexts within a single Run (per [Domain Model](/architecture/domain-model.md) §6). Since all branches belong to the same Run:

- All branches use the same pinned workflow definition
- All branches follow steps defined in that workflow
- Branch steps are governed by the same preconditions, validation rules, and outcome definitions
- If the workflow definition is updated in Git while branches are executing, the branches are unaffected — they use the pinned SHA

Branches do not independently resolve workflows. They are not separate Runs and do not have their own workflow bindings.

**Artifacts created within branches:**

If a branch step creates new artifacts (e.g., a sub-task), those new artifacts resolve their own workflow bindings independently when their own Runs are created. The parent Run's workflow binding does not cascade to child artifacts.

---

## 4. Workflow Resolution Algorithm

```
resolve_workflow(artifact):
  1. type = artifact.type                    # e.g., "Task"
  2. work_type = artifact.work_type          # e.g., "spike" or null

  3. candidates = all workflow definitions where:
       - status == "Active"
       - applies_to includes type

  4. if work_type is not null:
       candidates = candidates where work_type_selector includes work_type

  5. if candidates.count == 0:
       ERROR: no active workflow for (type, work_type)
  6. if candidates.count > 1:
       ERROR: ambiguous — multiple workflows match (type, work_type)

  7. workflow = candidates[0]
  8. sha = current Git SHA of workflow file
  9. return (workflow, sha)
```

---

## 5. Extended `applies_to` Clause

To support `work_type` resolution, the `applies_to` clause in workflow definitions accepts either a simple string or a structured object:

```yaml
# Governs all Tasks regardless of work_type
applies_to:
  - Task

# Governs only Tasks with work_type: spike
applies_to:
  - type: Task
    work_type: spike

# Governs Tasks with work_type: implementation or bugfix
applies_to:
  - type: Task
    work_type: [implementation, bugfix]
```

**Resolution precedence:**

When both a general workflow (`applies_to: [Task]`) and a specific workflow (`applies_to: [{type: Task, work_type: spike}]`) exist:

- If the artifact has `work_type: spike`, the specific workflow wins
- If the artifact has no `work_type`, the general workflow applies
- A general workflow and a specific workflow for the same type may coexist without conflict, as long as they do not overlap on the same `work_type` value

---

## 6. Cross-References

- [Workflow Definition Format](/architecture/workflow-definition-format.md) — `applies_to` clause (§3.4), version lifecycle (§4)
- [Domain Model](/architecture/domain-model.md) — Workflow Definition (§3.2), Run (§3.5), Divergence (§6)
- [Data Model](/architecture/data-model.md) — Run schema with `workflow_version` (§2.3)
- [Artifact Schema](/governance/artifact-schema.md) — Task front-matter fields (§5.3)
- [Task Lifecycle](/governance/task-lifecycle.md) — Governed states and transition rules
- [System Components](/architecture/components.md) — Workflow Engine responsibilities (§4.3)
- [Access Surface](/architecture/access-surface.md) — `run.start` operation

---

## 7. Evolution Policy

This binding model is expected to evolve as implementation reveals practical needs. Anticipated areas of change:

- Additional resolution criteria beyond `work_type` (e.g., project scope, team assignment)
- Override mechanisms for exceptional cases where rule-based resolution is insufficient
- Governance gates for `work_type` changes if auditability alone proves insufficient

Changes that alter the resolution algorithm or introduce new front-matter fields should be captured as ADRs.
