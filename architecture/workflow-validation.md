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

This document validates workflow definitions as step graphs and outcome routing rules. It does not define a separate declared workflow state machine. Runtime execution state lives in the runtime store, while durable artifact lifecycle changes occur only through explicit outcome commit effects such as \`commit.status\`.

---

## 2. Validation Categories

Workflow validation is organized into five categories, applied in order:

| Category | What It Checks | When Failure Is Detected |
|----------|---------------|--------------------------|
| Schema validation | Required fields, types, value constraints | Immediately on parse |
| Structural validation | Graph properties (reachability, cycles, dead steps) | After schema is valid |
| Semantic validation | Domain-level consistency (outcome coverage, binding uniqueness) | After structure is valid |
| Runtime safety validation | Execution risk (deadlocks, unbounded retries, unsatisfiable convergence) | After semantics are valid |
| Cross-artifact validation | Alignment with task lifecycle, actor model, skill model | After all internal checks pass |

All five categories must pass before a workflow may become `Active`.

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
| `required_skills` | list | Required for actor-assigned steps (non `automated_only`); at least one entry. Each entry is a non-empty string. |

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
| `entry_policy` | string or object (optional) | Either a simple policy name or a structured policy object (default: `all_branches_terminal`) |
| `evaluation_step` | string | Must reference a valid step `id` with `type: convergence` |

**Entry policy forms:**

- Simple string form: `all_branches_terminal`, `manual_trigger`
- Structured object form:
  - `type: minimum_completed_branches` requires `min >= 1`
  - `type: deadline_reached` requires a non-empty `deadline` value
  - Additional policy-specific fields must match the policy type

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

### 4.5.1 Branch Isolation

- Steps within a divergence branch must not have outcomes that route to steps in other branches or in the main step graph before convergence
- A branch may only exit through the convergence point or through `end` (if the branch terminates independently)
- No step outside a divergence branch may route into a branch step (except via the divergence point itself)

### 4.5.2 Convergence Completeness

- For structured divergence, all declared branches must have a path to the convergence point (unless the convergence entry policy allows partial completion)
- For `require_all` strategy, every branch must have at least one path that reaches the convergence point
- The convergence evaluation step must have outcomes that handle both successful and failed convergence

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
- Review steps must have at least an accept and reject outcome
- Convergence steps must have outcomes that handle both successful and failed convergence
- Steps in cycles must include at least one outcome that progresses toward termination

### 5.3 Convergence Strategy Consistency

- `select_one` convergence must lead to an evaluation step that can select a single branch
- `require_all` convergence should consider what happens when branches fail (the evaluation step should handle partial failure)
- `minimum_completed_branches` entry policy requires a structured `entry_policy` object with `type: minimum_completed_branches` and `min >= 1`
- `deadline_reached` entry policy requires a structured `entry_policy` object with `type: deadline_reached` and a non-empty `deadline`

### 5.4 Execution Mode Consistency

- Steps with `mode: human_only` should not have `type: automated`
- Steps with `mode: automated_only` should have `eligible_actor_types` limited to `ai_agent` and/or `automated_system`
- Steps with `type: convergence` may use `mode: automated_only`, `ai_only`, `human_only`, or `hybrid`, depending on how convergence evaluation is performed
- A convergence step using `mode: human_only` must include `human` in `eligible_actor_types`
- A convergence step using `mode: automated_only` must limit `eligible_actor_types` to `ai_agent` and/or `automated_system`

- Steps with `mode: ai_only` should have `eligible_actor_types` including `ai_agent` and must not include `human` actors
- Steps with `mode: hybrid` must define `eligible_actor_types` including at least two actor types

### 5.5 Status Transition Validity

Outcomes with `commit` effects produce durable artifact status changes. These must align with the governed lifecycle:

- Every `commit.status` value must be a valid status for the artifact type governed by `applies_to` (per [Artifact Schema](/governance/artifact-schema.md) §6)
- The workflow must not produce durable status transitions that skip required intermediate governed states defined for the artifact type; runtime-only execution states are not considered durable intermediate states for this rule
- Terminal statuses (`Completed`, `Superseded`) should only appear on outcomes that route to `end`

### 5.6 Version Consistency

- A workflow transitioning from `Deprecated` to `Active` should have a higher version than the previously active version
- A workflow transitioning to `Superseded` should reference or be accompanied by a successor workflow

---

## 6. Runtime Safety Validation

Runtime safety validation detects workflow patterns that are structurally valid but could cause execution failures at runtime.

### 6.1 Retry-Cycle Interaction

When a step with retry configuration exists inside a cycle (rework loop), the total retry budget across cycle iterations is unbounded. The Workflow Engine must track cumulative retries.

- Steps with `retry.limit` inside a cycle should declare a maximum cycle iteration count (warning if missing)
- Alternatively, the workflow should use a timeout on the cycle's enclosing step to bound total execution time

### 6.2 Timeout Exit Path Validity

- Every step with a `timeout` must have a `timeout_outcome` that leads to a valid exit (another step or `end`)
- The timeout exit path must not re-enter the same step (which would create a timeout loop)
- Steps without timeouts in cycles must be bounded by an enclosing timeout or a cycle iteration limit

### 6.3 Convergence Satisfiability

The convergence entry policy must be satisfiable given the divergence configuration:

- `minimum_completed_branches` policy: `min_branches` must be <= the number of declared branches (structured) or `max_branches` (exploratory)
- `all_branches_terminal` policy: all branches must have at least one path to terminal status
- `deadline_reached` policy: a deadline duration must be specified

### 6.4 Deadlock Detection

Detect patterns where execution cannot proceed:

- A step that waits for convergence of branches that cannot complete (e.g., all branches lead to failed states with no retry)
- Circular dependencies between divergence and convergence points (divergence A waits on convergence B, which waits on divergence A)

---

## 7. Cross-Artifact Validation

Cross-artifact validation checks that the workflow definition is consistent with the broader system context.

### 7.1 Artifact Type Alignment

- Every `applies_to` type must be a recognized artifact type in the system (per [Artifact Schema](/governance/artifact-schema.md) §5)
- Every `commit.status` value must be valid for the governed artifact type

### 7.2 Task Lifecycle Alignment

- Workflows governing Tasks must produce outcomes consistent with the [Task Lifecycle](/governance/task-lifecycle.md)
- Execution workflows for Tasks should cover the terminal states they are responsible for producing during normal execution
- Administrative terminal states such as `Superseded` or `Abandoned` may be governed outside the execution workflow and therefore do not need to appear as workflow step outcomes, but this must be documented explicitly

### 7.3 Skill Alignment

- `required_skills` referenced in step execution blocks are validated against the skill registry (per [Actor Model](/architecture/actor-model.md) §3.1 Skill Registry)
- When skills are registered in the workspace, each `required_skills` value is checked against active skill names
- Unregistered skill names produce a **warning** (not error) because skills may be registered after workflow creation
- Deprecated skills are not matched — referencing a deprecated skill produces a warning
- Implementation: `workflow.ValidateSkills()` in `internal/workflow/skill_validation.go`

### 7.4 Actor Type Alignment

- `eligible_actor_types` values must be valid actor types (`human`, `ai_agent`, `automated_system`)
- If the system has no registered actors matching a step's requirements, a warning is emitted

---

## 8. Validation Lifecycle

### 8.1 When Validation Runs

| Trigger | Validation Scope | Failure Behavior |
|---------|-----------------|-----------------|
| Workflow file committed to Git | Schema + structural | Warning in commit checks; does not block commit |
| Status change to `Active` | Schema + structural + semantic | Blocks activation; workflow remains in previous status |
| Run creation | Lightweight schema check (already validated) | Fails Run creation with error |

### 8.2 Validation at Commit Time

When a workflow file is committed to Git, validation runs as a best-effort check:

- Schema validation catches syntax errors and missing fields
- Structural validation catches graph issues
- Results are surfaced through operational events or CI integration
- **Commits are not blocked** — workflow files may be committed in `Draft` or `Deprecated` status without passing all checks

This allows workflow authors to iterate on definitions without being blocked by validation during development.

Validation tooling may provide fast feedback but is not considered authoritative until activation validation completes.

### 8.3 Validation at Activation

When a workflow's status changes to `Active`, full validation is mandatory:

- All schema, structural, and semantic rules must pass
- `applies_to` uniqueness is checked against all other Active workflows
- If validation fails, the status change is rejected and the workflow remains in its previous status
- Validation failures produce an operational event with details

### 8.4 Validation at Run Creation

When a Run is created, the Workflow Engine performs a lightweight validation:

- Confirms the workflow is in `Active` status
- Confirms the workflow version matches the resolved binding
- Does not re-run full structural or semantic validation (this was done at activation)

This is a safeguard against race conditions (e.g., a workflow deactivated between resolution and Run creation).

---

## 9. Error Reporting

### 9.1 Validation Result Format

Validation produces a structured result:

```yaml
validation_result:
  workflow_id: <string>
  workflow_version: <string>
  status: <enum>                 # passed, failed, warnings
  errors:                        # Blocking issues (must fix before activation)
    - category: <string>         # schema, structural, semantic, runtime_safety, cross_artifact
      rule: <string>             # e.g., "reachability", "applies_to_uniqueness"
      message: <string>          # Human-readable description
      location: <string|null>    # Step ID or field path where the issue was found
  warnings:                      # Non-blocking issues (should fix)
    - category: <string>
      rule: <string>
      message: <string>
      location: <string|null>
  advisories:                    # Best practice recommendations
    - category: <string>
      rule: <string>
      message: <string>
      location: <string|null>
```

### 9.2 Severity Levels

| Severity | Meaning | Blocks Activation |
|----------|---------|-------------------|
| Error | The workflow cannot execute correctly or violates governance rules | Yes |
| Warning | The workflow may execute but has potential issues or risks | No |
| Advisory | Best practice recommendation; no execution risk | No |

**Examples of errors:**
- Unreachable step
- Missing required field
- Cycle with no exit
- `applies_to` conflict with another Active workflow
- Invalid `commit.status` for the governed artifact type
- Branch step routing outside its branch before convergence

**Examples of warnings:**
- Step with no timeout configured
- Unused divergence or convergence point definition
- `required_skills` not found in registered skills
- Retry inside a cycle without iteration limit
- No registered actors matching step requirements
- Review step without reject outcome
- Convergence step without failure outcome
- Step in cycle without an explicit progression path toward termination

**Examples of advisories:**
- Step name could be more descriptive
- Workflow has high step count (complexity warning)
- Step has many outcomes (readability concern)

### 9.3 Surfacing Results

Validation results are surfaced through:

- **Operational event** (`workflow_validation_completed`) emitted after validation runs
- **CLI output** when validating via the CLI
- **API response** when activating via the API
- **CI integration** when validating on commit (if configured)
- **Access Gateway responses** when validation is triggered through external requests

---

## 10. Relationship to Workflow Lifecycle

### 10.1 Draft Workflows

Workflow files committed to Git without `Active` status are effectively drafts. They may fail validation without consequence — the system does not attempt to use them for execution.

Authors may use the CLI or API to validate draft workflows on demand without changing their status.

### 10.2 Activation Gate

The transition to `Active` status is the primary validation gate:

```
Draft/Deprecated → (validation passes) → Active
Draft/Deprecated → (validation fails) → remains unchanged
```

This means:
- Authors can iterate freely on workflow definitions in Git
- Only validated workflows can govern execution
- The system never executes against a workflow that hasn't passed full validation

### 10.3 Deprecation and Supersession

When a workflow is `Deprecated` or `Superseded`:

- In-progress Runs continue using the pinned version
- No new Runs are created against it
- The workflow does not need to pass validation in these states (it was validated when it was Active)

---

## 11. Constitutional Alignment

| Principle | How Validation Supports It |
|-----------|---------------------------|
| Governed Execution (§4) | Validation ensures workflow definitions are well-formed before they govern execution |
| Explicit Intent (§3) | Validation catches ambiguous or incomplete workflow definitions |
| Reproducibility (§7) | Validated workflows produce predictable execution paths |
| Source of Truth (§2) | Workflow files in Git are the authoritative definitions; validation operates on Git content |

---

## 12. Cross-References

- [Workflow Definition Format](/architecture/workflow-definition-format.md) — Schema being validated
- [Task-to-Workflow Binding](/architecture/task-workflow-binding.md) — `applies_to` resolution and uniqueness rules
- [Divergence and Convergence](/architecture/divergence-and-convergence.md) — Divergence/convergence structural rules
- [Error Handling](/architecture/error-handling-and-recovery.md) — Run creation failure handling
- [System Components](/architecture/components.md) §4.3 — Workflow Engine, §4.8 — Validation Service
- [Constitution](/governance/constitution.md) §4 — Governed Execution, §11 — Cross-Artifact Validation
- [Artifact Schema](/governance/artifact-schema.md) §5, §6 — Artifact type schemas and status enums
- [Actor Model](/architecture/actor-model.md) §3 — Actor registration and skills
- [Task Lifecycle](/governance/task-lifecycle.md) — Governed terminal states
- [Validation Service](/architecture/validation-service.md) — Cross-artifact validation rules and extensibility

---

## 13. Evolution Policy

This validation specification is expected to evolve as workflows are implemented and authoring patterns emerge.

Areas expected to require refinement:

- Custom validation rules defined within workflow files
- Validation plugins for domain-specific checks
- Migration validation (detecting breaking changes between workflow versions)
- Performance validation (detecting workflows with excessive step counts or deep nesting)
- Automated fix suggestions for common validation failures

New validation rules should be added to this document with clear category, severity, and error message guidance before being implemented.
