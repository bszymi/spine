---
type: Architecture
title: Workflow Authoring and Validation
status: Living Document
version: "0.1"
---

# Workflow Authoring and Validation

---

## 1. Purpose

This document defines how workflow definitions are validated before activation and what correctness guarantees the system provides.

The [Workflow Definition Format](/architecture/workflow-definition-format.md) defines the structure of workflows. The [Task-to-Workflow Binding Model](/architecture/task-workflow-binding.md) defines how workflows are resolved at runtime. This document specifies the rules that ensure workflow definitions are well-formed, structurally sound, and semantically consistent before they govern execution.

---

## 2. Validation Categories

Workflow validation is organized into three categories, applied in order:

| Category | What It Checks | When Failure Is Detected |
|----------|---------------|--------------------------|
| Schema validation | Required fields, types, value constraints | Immediately on parse |
| Structural validation | Graph properties (reachability, cycles, dead steps) | After schema is valid |
| Semantic validation | Domain-level consistency (outcome coverage, binding uniqueness) | After structure is valid |

All three categories must pass before a workflow may become `Active`.

---

## 3. Schema Validation

Schema validation ensures the workflow YAML conforms to the expected structure.

### 3.1 Top-Level Required Fields

| Field | Type | Constraint |
|-------|------|-----------|
| `id` | string | Non-empty, unique across all workflow files |
| `name` | string | Non-empty |
| `version` | string | Valid semantic version (major.minor) |
| `status` | enum | One of: `Active`, `Deprecated`, `Superseded` |
| `description` | string | Non-empty |
| `applies_to` | list | Non-empty; each entry is a valid artifact type string or structured object with `type` and optional `work_type` |
| `entry_step` | string | Must reference a valid step `id` |
| `steps` | list | Non-empty; at least one step |

### 3.2 Step Required Fields

| Field | Type | Constraint |
|-------|------|-----------|
| `id` | string | Unique within the workflow |
| `name` | string | Non-empty |
| `type` | enum | One of: `manual`, `automated`, `review`, `convergence` |
| `outcomes` | list | Non-empty; at least one outcome |

### 3.3 Step Conditional Requirements

| Condition | Requirement |
|-----------|------------|
| `type: automated` | `retry` block must be present with `limit >= 1` |
| `timeout` is present | `timeout_outcome` must reference a valid outcome `id` |
| `diverge` is present | Must reference a valid divergence point `id` |
| `converge` is present | Must reference a valid convergence point `id` |

### 3.4 Outcome Required Fields

| Field | Type | Constraint |
|-------|------|-----------|
| `id` | string | Unique within the step |
| `name` | string | Non-empty |
| `next_step` | string | Must be a valid step `id` or the literal `end` |

### 3.5 Execution Block Validation

| Field | Type | Constraint |
|-------|------|-----------|
| `mode` | enum | One of: `automated_only`, `ai_only`, `human_only`, `hybrid` |
| `eligible_actor_types` | list | Each entry one of: `human`, `ai_agent`, `automated_system` |
| `required_capabilities` | list (optional) | Each entry is a non-empty string |

### 3.6 Divergence Point Validation

| Field | Type | Constraint |
|-------|------|-----------|
| `id` | string | Unique across divergence and convergence points |
| `name` | string | Non-empty |
| `mode` | enum | One of: `structured`, `exploratory` |

**Structured mode additional requirements:**

| Field | Constraint |
|-------|-----------|
| `branches` | Non-empty list; each branch has `id`, `name`, `start_step` |
| `start_step` | Must reference a valid step `id` |

**Exploratory mode additional requirements:**

| Field | Constraint |
|-------|-----------|
| `branch_step` | Must reference a valid step `id` |
| `min_branches` | Integer >= 1 |
| `max_branches` | Integer >= `min_branches` (if present) |

### 3.7 Convergence Point Validation

| Field | Type | Constraint |
|-------|------|-----------|
| `id` | string | Unique across divergence and convergence points |
| `name` | string | Non-empty |
| `strategy` | enum | One of: `select_one`, `select_subset`, `merge`, `require_all`, `experiment` |
| `entry_policy` | string (optional) | One of: `all_branches_terminal`, `minimum_completed_branches`, `deadline_reached`, `manual_trigger` (default: `all_branches_terminal`) |
| `evaluation_step` | string | Must reference a valid step `id` with `type: convergence` |

---

## 4. Structural Validation

Structural validation treats the workflow as a directed graph where steps are nodes and outcomes are edges. It verifies graph-level properties that ensure the workflow can execute correctly.

### 4.1 Reachability

Every step must be reachable from `entry_step` through at least one path of outcome transitions.

**Rule:** Starting from `entry_step`, follow all `next_step` references. Any step not visited is unreachable.

**Failure:** Unreachable steps indicate dead code in the workflow — they can never execute.

### 4.2 Termination

Every path through the workflow must eventually reach `end`.

**Rule:** From every step, follow all outcome paths. Every path must terminate at `end` within a finite number of transitions.

**Note:** Cycles are permitted (see §4.3), so termination analysis must check that every cycle has at least one exit path that leads to `end`. A workflow where all paths from a step lead only back to itself (with no exit) is invalid.

### 4.3 Cycle Detection

Cycles in the step graph are permitted — they represent rework loops (e.g., review → execute → review). However, certain cycle properties must be validated:

- **Every cycle must have an exit** — at least one outcome in the cycle must lead to a step outside the cycle or to `end`
- **Cycles must not be unreachable** — all steps in the cycle must be reachable from `entry_step`

A cycle with no exit creates an infinite loop where execution can never terminate.

### 4.4 Entry Step Validity

- `entry_step` must reference a step that exists in the `steps` list
- `entry_step` must not reference a convergence step (convergence steps are reached through divergence, not directly)

### 4.5 Divergence-Convergence Pairing

- Every divergence point referenced by a step must have a corresponding convergence point that collects its branches
- The convergence point's `evaluation_step` must exist and have `type: convergence`
- For structured divergence, every branch's `start_step` must be reachable only through the divergence (not from the main step graph)

### 4.6 Outcome Reference Integrity

- Every `next_step` value must reference either a valid step `id` or the literal `end`
- Every `timeout_outcome` must reference a valid outcome `id` within the same step
- Convergence point `evaluation_step` must reference a step with `type: convergence`

### 4.7 Deterministic Entry Guarantees

The workflow must ensure that execution from `entry_step` is deterministic in terms of initial step activation:

- `entry_step` must not depend on conditional routing
- No divergence may occur before the first step execution begins

This ensures predictable Run initialization.

---

## 5. Semantic Validation

Semantic validation checks domain-level consistency that requires understanding the workflow's meaning, not just its structure.

### 5.1 Applies-To Uniqueness

At most one `Active` workflow may govern a given `(type, work_type)` pair at any time (per [Task-to-Workflow Binding](/architecture/task-workflow-binding.md) §3.3).

**Rule:** When a workflow's status changes to `Active`, check all other `Active` workflows for overlapping `applies_to` entries. Overlap means the same `(type, work_type)` combination would match two workflows.

**Cases:**

- Two workflows with `applies_to: [Task]` (no `work_type`) — conflict
- Workflow A with `applies_to: [{type: Task, work_type: spike}]` and Workflow B with `applies_to: [{type: Task, work_type: spike}]` — conflict
- Workflow A with `applies_to: [Task]` (general) and Workflow B with `applies_to: [{type: Task, work_type: spike}]` (specific) — allowed (specific takes precedence)

### 5.2 Outcome Coverage

Every step should cover its expected execution paths:

- Steps with `timeout` must have a `timeout_outcome` that maps to a declared outcome
- Review steps should have at least an accept and reject outcome (warning, not error)
- Convergence steps should have outcomes that handle both successful and failed convergence

- Steps with loops (cycles) should include at least one outcome that progresses toward termination (warning if missing)

### 5.3 Convergence Strategy Consistency

- `select_one` convergence must lead to an evaluation step that can select a single branch
- `require_all` convergence should consider what happens when branches fail (the evaluation step should handle partial failure)
- `minimum_completed_branches` entry policy requires a `min_branches` parameter on the convergence point

### 5.4 Execution Mode Consistency

- Steps with `mode: human_only` should not have `type: automated`
- Steps with `mode: automated_only` should have `eligible_actor_types` limited to `ai_agent` and/or `automated_system`
- Steps with `type: convergence` should use `mode: automated_only` (convergence evaluation is a system operation)

- Steps with `mode: ai_only` should have `eligible_actor_types` including `ai_agent` and must not include `human` actors
- Steps with `mode: hybrid` must define `eligible_actor_types` including at least two actor types

### 5.5 Version Consistency

- A workflow transitioning from `Deprecated` to `Active` should have a higher version than the previously active version
- A workflow transitioning to `Superseded` should reference or be accompanied by a successor workflow

---

## 6. Validation Lifecycle

### 6.1 When Validation Runs

| Trigger | Validation Scope | Failure Behavior |
|---------|-----------------|-----------------|
| Workflow file committed to Git | Schema + structural | Warning in commit checks; does not block commit |
| Status change to `Active` | Schema + structural + semantic | Blocks activation; workflow remains in previous status |
| Run creation | Lightweight schema check (already validated) | Fails Run creation with error |

### 6.2 Validation at Commit Time

When a workflow file is committed to Git, validation runs as a best-effort check:

- Schema validation catches syntax errors and missing fields
- Structural validation catches graph issues
- Results are surfaced through operational events or CI integration
- **Commits are not blocked** — workflow files may be committed in `Draft` or `Deprecated` status without passing all checks

This allows workflow authors to iterate on definitions without being blocked by validation during development.

Validation tooling may provide fast feedback but is not considered authoritative until activation validation completes.

### 6.3 Validation at Activation

When a workflow's status changes to `Active`, full validation is mandatory:

- All schema, structural, and semantic rules must pass
- `applies_to` uniqueness is checked against all other Active workflows
- If validation fails, the status change is rejected and the workflow remains in its previous status
- Validation failures produce an operational event with details

### 6.4 Validation at Run Creation

When a Run is created, the Workflow Engine performs a lightweight validation:

- Confirms the workflow is in `Active` status
- Confirms the workflow version matches the resolved binding
- Does not re-run full structural or semantic validation (this was done at activation)

This is a safeguard against race conditions (e.g., a workflow deactivated between resolution and Run creation).

---

## 7. Error Reporting

### 7.1 Validation Result Format

Validation produces a structured result:

```yaml
validation_result:
  workflow_id: <string>
  workflow_version: <string>
  status: <enum>                 # passed, failed, warnings
  errors:                        # Blocking issues (must fix before activation)
    - category: <string>         # schema, structural, semantic
      rule: <string>             # e.g., "reachability", "applies_to_uniqueness"
      message: <string>          # Human-readable description
      location: <string|null>    # Step ID or field path where the issue was found
  warnings:                      # Non-blocking issues (should fix)
    - category: <string>
      rule: <string>
      message: <string>
      location: <string|null>
```

### 7.2 Error vs Warning

| Severity | Meaning | Blocks Activation |
|----------|---------|-------------------|
| Error | The workflow cannot execute correctly | Yes |
| Warning | The workflow may execute but has potential issues | No |

**Examples of errors:**
- Unreachable step
- Missing required field
- Cycle with no exit
- `applies_to` conflict with another Active workflow

**Examples of warnings:**
- Review step with only one outcome (no reject path)
- Step with no timeout configured
- Unused divergence or convergence point definition

### 7.3 Surfacing Results

Validation results are surfaced through:

- **Operational event** (`workflow_validation_completed`) emitted after validation runs
- **CLI output** when validating via the CLI
- **API response** when activating via the API
- **CI integration** when validating on commit (if configured)
- **Access Gateway responses** when validation is triggered through external requests

---

## 8. Relationship to Workflow Lifecycle

### 8.1 Draft Workflows

Workflow files committed to Git without `Active` status are effectively drafts. They may fail validation without consequence — the system does not attempt to use them for execution.

Authors may use the CLI or API to validate draft workflows on demand without changing their status.

### 8.2 Activation Gate

The transition to `Active` status is the primary validation gate:

```
Draft/Deprecated → (validation passes) → Active
Draft/Deprecated → (validation fails) → remains unchanged
```

This means:
- Authors can iterate freely on workflow definitions in Git
- Only validated workflows can govern execution
- The system never executes against a workflow that hasn't passed full validation

### 8.3 Deprecation and Supersession

When a workflow is `Deprecated` or `Superseded`:

- In-progress Runs continue using the pinned version
- No new Runs are created against it
- The workflow does not need to pass validation in these states (it was validated when it was Active)

---

## 9. Constitutional Alignment

| Principle | How Validation Supports It |
|-----------|---------------------------|
| Governed Execution (§4) | Validation ensures workflow definitions are well-formed before they govern execution |
| Explicit Intent (§3) | Validation catches ambiguous or incomplete workflow definitions |
| Reproducibility (§7) | Validated workflows produce predictable execution paths |
| Source of Truth (§2) | Workflow files in Git are the authoritative definitions; validation operates on Git content |

---

## 10. Cross-References

- [Workflow Definition Format](/architecture/workflow-definition-format.md) — Schema being validated
- [Task-to-Workflow Binding](/architecture/task-workflow-binding.md) — `applies_to` resolution and uniqueness rules
- [Divergence and Convergence](/architecture/divergence-and-convergence.md) — Divergence/convergence structural rules
- [Error Handling](/architecture/error-handling-and-recovery.md) — Run creation failure handling
- [System Components](/architecture/components.md) §4.3 — Workflow Engine, §4.8 — Validation Service
- [Constitution](/governance/constitution.md) §4 — Governed Execution

---

## 11. Evolution Policy

This validation specification is expected to evolve as workflows are implemented and authoring patterns emerge.

Areas expected to require refinement:

- Custom validation rules defined within workflow files
- Validation plugins for domain-specific checks
- Migration validation (detecting breaking changes between workflow versions)
- Performance validation (detecting workflows with excessive step counts or deep nesting)
- Automated fix suggestions for common validation failures

New validation rules should be added to this document with clear category, severity, and error message guidance before being implemented.
