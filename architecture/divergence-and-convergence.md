---
type: Architecture
title: Divergence and Convergence Execution Model
status: Living Document
version: "0.1"
---

# Divergence and Convergence Execution Model

---

## 1. Purpose

This document defines how controlled divergence and convergence execute within the Spine runtime.

The [Constitution](/governance/Constitution.md) (§6) mandates that parallel execution must be explicit, all outcomes must be preserved, and convergence must occur through explicit evaluation steps. The [Domain Model](/architecture/domain-model.md) (§6) establishes that divergence creates parallel steps within a single Run. The [Workflow Definition Format](/architecture/workflow-definition-format.md) (§3.3) defines the declarative structure for divergence and convergence points.


This document specifies the runtime execution semantics — what happens when a Run reaches a divergence point, how parallel branches execute, and how convergence evaluates and commits results.

---

## 1.1 Parallel Execution vs Divergence

Parallel execution does not always imply divergence.

Two distinct forms of parallelism exist in the Spine execution model:

### Parallel Execution (Non-Divergent)

- Multiple steps execute concurrently
- All steps operate on the same artifact state
- Outputs are additive (e.g., validations, approvals, reports)
- No Git branching occurs
- No convergence step is required

This model is used for:
- approvals and validation workflows
- independent checks (e.g., QA, security, compliance)
- multi-role execution where outputs are not competing

### Divergence (Branching Execution)

- Multiple branches represent alternative approaches or outputs
- Each branch operates in isolation
- Git branches may be created for durable artifacts
- Convergence is required to evaluate outcomes

This model is used for:
- exploratory work (e.g., multiple design variants)
- competing implementations
- alternative solutions to the same problem

Workflows must explicitly choose between these models. Divergence must not be used for parallel validation or approval steps.

> **Note:** Non-divergent parallel execution is not yet supported by the Workflow Definition Format. The step-graph model routes through `next_step` sequentially. A future format extension (e.g., a `parallel` step type or `concurrent_steps` block) is needed to express non-divergent parallelism. Until then, workflows requiring concurrent non-competing steps should model them as sequential steps or use divergence with `require_all` as an approximation.

---

## 2. Foundational Constraints

These constraints are derived from the Constitution (§6) and cannot be relaxed:

1. **Explicit declaration** — divergence must be declared in the workflow definition; implicit parallelism is prohibited.
2. **Outcome preservation** — all branch outcomes, selected and non-selected, must be preserved as auditable artifacts.
3. **Explicit evaluation** — convergence must occur through a defined evaluation step; automatic selection without evaluation is prohibited.
4. **No silent overwriting** — a convergence result must not silently discard or overwrite alternative outputs.

---

## 3. Divergence

### 3.1 What Triggers Divergence

Divergence occurs when a step references a divergence point via the `diverge` field. The workflow definition declares this:

```yaml
steps:
  - id: plan
    name: Plan Approach
    type: manual
    outcomes:
      - id: approaches_identified
        name: Approaches Identified
        next_step: implement-a     # overridden by diverge
    diverge: diverge-implementations

divergence_points:
  - id: diverge-implementations
    name: Parallel Implementations
    branches:
      - id: branch-a
        name: Approach A
        start_step: implement-a
      - id: branch-b
        name: Approach B
        start_step: implement-b
```

When the `plan` step completes with the `approaches_identified` outcome, the Workflow Engine detects the `diverge` reference and creates parallel branches instead of following `next_step` routing.

### 3.2 Branch Creation

When divergence is triggered, the Workflow Engine:

1. Records the divergence event in the Run's runtime state
2. Creates a **branch execution context** for each declared branch (for structured divergence) or initializes a dynamic branch set (for exploratory divergence)
3. Each branch context tracks:
   - `branch_id` — unique identifier for the branch; references a declared branch in structured divergence, or a runtime-created branch in exploratory divergence
   - `status` — pending, in_progress, completed, failed
   - `current_step` — the step currently executing in this branch
   - `artifacts` — artifacts produced by this branch
   - `outcome` — the terminal result of this branch
4. Activates each branch's `start_step` for execution

All branches belong to the same Run. Divergence does not create new Runs.

### 3.2.1 Static vs Dynamic Divergence

Two forms of divergence are supported:

#### Structured Divergence (Static)

- Branches are explicitly declared in the workflow definition
- Each branch represents a distinct role, responsibility, or predefined alternative
- The number and identity of branches are fixed before execution
- Used when structure, guarantees, or governance constraints are required

Examples:
- A/B testing with fixed variants
- fixed competing alternatives with known identities
- parallel implementations where each branch is an alternative solution

#### Exploratory Divergence (Dynamic)

- Branches may be created at runtime
- The number of branches is not fixed in advance
- Branches represent interchangeable variants of the same task
- Additional branches may be added during execution

This mode is declared in the workflow definition with `mode: exploratory` on the divergence point:

```yaml
divergence_points:
  - id: diverge-explorations
    name: Design Explorations
    mode: exploratory
    branch_step: explore          # step template each new branch starts at
    min_branches: 1               # minimum branches before convergence is allowed
    max_branches: 10              # optional cap on branch count
```

Unlike static divergence, exploratory divergence does not list branches upfront. Instead, it declares a `branch_step` that each dynamically created branch will start at, and optional constraints on branch count.

This mode is used for:
- design exploration (e.g., generating multiple UI concepts)
- AI-driven variant generation
- open-ended problem solving where the number of approaches is unknown

> **Note:** `min_branches` constrains branch creation in exploratory divergence, while `minimum_completed_branches` (see §4.1.1) controls when convergence may begin.

### 3.2.2 Divergence Window (Dynamic Only)

In exploratory divergence, the system enters a divergence window:

- The branch set remains open for expansion
- Authorized actors may create additional branches
- Each new branch is recorded with:
  - creator
  - timestamp
  - rationale (optional but recommended)

The divergence window must be explicitly closed before convergence begins, unless the convergence policy allows partial evaluation.

Branch expansion must be governed:
- Only actors permitted by the workflow may add branches
- Branch creation is an explicit action, not automatic or implicit

### 3.3 Branch Execution

Each branch executes independently within the Run:

- Branches follow their own step sequences as defined by `next_step` routing
- Each branch may traverse multiple steps before reaching its terminal point
- Branch steps are assigned to actors through the normal Actor Gateway flow
- Branch steps produce artifacts scoped to their branch context

**Actor assignment per branch:**

- Different branches may be assigned to different actors
- The same actor may be assigned to multiple branches
- Actor eligibility is determined by each step's `execution` block, not by the branch declaration
- The Workflow Engine must not assign the same human actor to competing branches unless the workflow definition explicitly permits it (to avoid bias in convergence evaluation)

### 3.4 Branch Isolation

Branches operate in isolation:

- A branch cannot read artifacts produced by a sibling branch during execution
- A branch cannot modify artifacts owned by a sibling branch
- Each branch works against the same baseline state that existed at the divergence point
- Branch isolation prevents one branch's work from influencing another's outcome

If a branch needs to produce durable artifacts (e.g., code, documents), these are created on separate Git branches. The naming convention is:

```
<run-branch>/<divergence-id>/<branch-id>
```

### 3.5 Branch Completion and Failure

A branch completes when its step sequence reaches `next_step: end` or a step designated as the branch's terminal step.

A branch fails when:
- A step within the branch fails and exhausts its retry limit
- A step within the branch times out without a recoverable timeout_outcome
- An actor assigned to the branch becomes permanently unavailable

**Branch failure does not automatically fail the Run.** The convergence point determines how to handle partial branch completion (see §4.3).

---

## 4. Convergence

### 4.1 When Convergence Occurs

Convergence begins based on the convergence entry policy defined by the workflow.

By default, convergence begins when all branches listed in the convergence point have reached a terminal state (completed or failed). However, alternative entry policies may be defined.

```yaml
convergence_points:
  - id: converge-implementations
    name: Evaluate Implementations
    branches: [branch-a, branch-b]      # for static; omit for exploratory
    strategy: select_one
    entry_policy: all_branches_terminal  # default
    evaluation_step: evaluate
```

The Workflow Engine activates the convergence point's evaluation step according to the entry policy. Convergence does not begin while any branch is still in progress, unless the policy allows it.

### 4.1.1 Convergence Entry Policies

The `entry_policy` field on a convergence point defines when convergence is allowed to begin:

```yaml
# Default — wait for all branches
entry_policy: all_branches_terminal

# Begin after at least N branches complete
entry_policy:
  type: minimum_completed_branches
  min: 3

# Begin after a time threshold
entry_policy:
  type: deadline_reached
  deadline: "7d"

# An authorized actor explicitly triggers convergence
entry_policy: manual_trigger
```

| Policy | Meaning |
|--------|---------|
| `all_branches_terminal` (default) | All branches must complete or fail |
| `minimum_completed_branches` | Convergence may begin after `min` branches complete; remaining branches continue but their results are optional |
| `deadline_reached` | Convergence begins after `deadline` elapses; completed branches are evaluated, and in-progress branches are handled according to workflow policy (e.g., cancelled, excluded, or allowed to continue asynchronously) |
| `manual_trigger` | An authorized actor explicitly starts convergence |

For exploratory divergence, convergence also requires that the divergence window is closed before evaluation begins, unless the policy explicitly allows partial evaluation.

### 4.2 Convergence Strategies

The `strategy` field determines how branch results are processed:

#### `select_one`

One branch outcome is selected as the result; all others are preserved but not applied.

- The evaluation step reviews all branch outcomes
- The evaluator selects exactly one branch as the winner
- The selected branch's artifacts become the convergence result
- Non-selected branch artifacts are preserved with status `not_selected`

This is the most common strategy — used when multiple approaches to the same problem are explored and the best one is chosen.

#### `merge`

Multiple branch outcomes are combined into a single result.

- The evaluation step reviews all branch outcomes
- The evaluator produces a merged artifact that combines elements from multiple branches
- The merged artifact is a new artifact, not a modification of any branch artifact
- All original branch artifacts are preserved alongside the merged result

This strategy is used when branches produce complementary work that should be combined.

#### `require_all`

All branches must complete successfully before proceeding.

- If any branch failed, the convergence point fails
- The evaluation step verifies that all branch outcomes meet the required conditions
- All branch artifacts are carried forward as the convergence result

This strategy is used when all parallel work streams must succeed — for example, parallel validation checks that must all pass.

#### `select_subset`

A subset of branch outcomes is selected for further processing.

- The evaluation step reviews all branch outcomes
- The evaluator selects one or more branches as candidates
- Selected branches continue to the next stage
- Non-selected branches are preserved with status `not_selected`

This strategy is used for:
- shortlisting candidates from a larger exploration set
- reducing the solution space before further evaluation
- preparing variants for downstream processes such as experimentation

#### `experiment`

Multiple branch outcomes are packaged into a controlled experiment.

- The evaluation step selects one or more eligible branches
- A new experiment artifact is created containing:
  - selected variants
  - feature flag or routing configuration
  - traffic allocation rules
  - evaluation metrics and success criteria
  - evaluation window and decision owner
- Selected variants are deployed under controlled conditions (e.g., feature flags)
- Original branch artifacts remain preserved

This strategy is used for:
- A/B or multi-variant testing in production
- data-driven evaluation using real user behavior
- staged rollout of competing alternatives

Experimentation introduces deferred convergence: a later evaluation step determines the final outcome based on collected evidence.

### 4.3 Handling Partial Branch Completion

When some branches complete but others fail:

| Strategy | Behavior |
|----------|----------|
| `select_one` | Convergence may proceed if at least one branch completed successfully. The evaluation step can only select from completed branches. |
| `select_subset` | Same as `select_one` — at least one completed branch is required. The evaluator selects from completed branches only. |
| `merge` | Convergence may proceed at the evaluation step's discretion. The evaluator decides whether a partial merge is acceptable. |
| `require_all` | Convergence fails. The Run follows the evaluation step's failure outcome. |
| `experiment` | Convergence may proceed if at least one branch completed successfully. The evaluator selects eligible branches for the experiment. |

The evaluation step always has visibility into which branches completed and which failed, along with any partial artifacts from failed branches.

### 4.4 The Evaluation Step

The evaluation step is a regular workflow step (type `review` or `convergence`) that receives all branch outcomes as input and produces a convergence decision.

```yaml
steps:
  - id: evaluate
    name: Evaluate Implementations
    type: convergence
    execution:
      mode: human_only
      eligible_actor_types:
        - human
    required_inputs:
      - branch_outcomes
    outcomes:
      - id: branch_selected
        name: Branch Selected
        next_step: finalize
        commit:
          status: In Review
      - id: no_acceptable_result
        name: No Acceptable Result
        next_step: end
        commit:
          status: Rejected
    converge: converge-implementations
```

**Evaluator constraints:**

- The evaluator must not be an actor who participated in any of the competing branches (for `select_one` and `merge` strategies)
- The Actor Gateway enforces this constraint during assignment
- For `require_all`, this constraint does not apply since there is no selection decision

**Evaluation inputs:**

The evaluation step receives a structured summary of all branch outcomes:

- Branch ID and status (completed/failed)
- Artifacts produced by each branch
- Step execution history for each branch (for auditability)
- Any validation results from branch steps

### 4.5 Committing Convergence Results to Git

After the evaluation step completes, durable outcomes are committed to Git per ADR-001:

1. **Selected artifacts** — the chosen result (selected branch, subset, merged artifact, or experiment package) is committed to the main artifact path
2. **Non-selected branch artifacts** — preserved in their branch paths with metadata indicating they were evaluated and not selected
3. **Convergence record** — a record of the evaluation decision is committed, including:
   - Which branches were evaluated
   - Which branch was selected (for `select_one`) or how results were merged
   - The evaluator's rationale (if provided)
   - Timestamp and actor reference

Non-selected branch artifacts remain accessible at their branch paths indefinitely. They are never deleted — per Constitution §6, silent overwriting is prohibited.

---

## 5. Runtime State Model

### 5.1 Branch Execution Context

The Runtime Store tracks branch execution state as part of the Run. This extends the existing Run schema from the [Data Model](/architecture/data-model.md):

```
Run
├── run_id
├── current_step (null during divergence — execution is within branches)
├── divergence_contexts[]
│   ├── divergence_id
│   ├── status: pending | active | converging | resolved | failed
│   ├── divergence_mode: structured | exploratory
│   ├── triggered_at
│   ├── divergence_window: open | closed   # for exploratory divergence
│   ├── branches[]
│   │   ├── branch_id
│   │   ├── status: pending | in_progress | completed | failed
│   │   ├── current_step
│   │   ├── step_executions[]
│   │   ├── artifacts[]
│   │   └── outcome
│   ├── convergence_id
│   ├── convergence_result
│   │   ├── strategy_applied
│   │   ├── selected_branch (for select_one)
│   │   ├── selected_branches[] (for select_subset)
│   │   ├── merged_artifact (for merge)
│   │   ├── experiment_artifact (for experiment)
│   │   ├── entry_policy_applied
│   │   └── evaluation_record
│   └── resolved_at
└── ...
```

### 5.2 Divergence Context Lifecycle

```
pending → active → converging → resolved
                              → failed
```

- **pending** — divergence point reached, branches not yet created
- **active** — branches are executing in parallel
- **converging** — all branches terminal, evaluation step in progress
- **resolved** — convergence completed, result committed
- **failed** — convergence failed (e.g., `require_all` with branch failure, or evaluation step rejected all results)

---

## 6. Component Interactions

### 6.1 Workflow Engine

The Workflow Engine orchestrates the entire divergence-convergence lifecycle:

- Detects divergence triggers on step outcomes
- Creates branch execution contexts
- Tracks branch progress independently
- Detects when all branches reach terminal states
- Activates the convergence evaluation step
- Applies the convergence result and resumes normal step-graph execution

### 6.2 Actor Gateway

The Actor Gateway handles actor assignment with divergence-specific constraints:

- Assigns actors to branch steps based on each step's `execution` block
- Enforces branch-evaluator separation (evaluator must not have participated in competing branches)
- May assign different actors to different branches or the same actor to non-competing branches

### 6.3 Artifact Service

The Artifact Service manages branch-scoped artifact operations:

- Creates branch-scoped artifact paths during divergence
- Commits selected branch artifacts to canonical paths during convergence
- Preserves non-selected branch artifacts at their branch paths
- Records convergence metadata on preserved artifacts

---

## 7. Nested Divergence

A branch may itself contain a divergence point, creating nested parallel execution. This is permitted but must be explicitly declared in the workflow definition.

Nested divergence follows the same rules:

- Each nested divergence has its own convergence point
- Inner convergence must resolve before outer convergence can begin
- Branch isolation applies at each nesting level

The runtime state model supports nesting naturally — a branch's step execution may trigger a new divergence context within that branch.

Workflow authors should use nested divergence sparingly. Deep nesting increases complexity and makes evaluation more difficult.

---

## 8. Example: Parallel Implementation with Review

```yaml
id: parallel-task-execution
name: Task Execution with Parallel Approaches
version: "1.0"
status: Active
description: Task workflow where multiple approaches are explored in parallel.

applies_to:
  - Task

entry_step: plan

steps:
  - id: plan
    name: Plan Approaches
    type: manual
    execution:
      mode: human_only
      eligible_actor_types:
        - human
    required_outputs:
      - approach_descriptions
    outcomes:
      - id: approaches_ready
        name: Approaches Ready for Implementation
        next_step: implement-a
        commit:
          status: In Progress
    diverge: diverge-implementations

  - id: implement-a
    name: Implement Approach A
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
        - ai_agent
    required_outputs:
      - implementation
    outcomes:
      - id: done
        name: Implementation Complete
        next_step: end

  - id: implement-b
    name: Implement Approach B
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
        - ai_agent
    required_outputs:
      - implementation
    outcomes:
      - id: done
        name: Implementation Complete
        next_step: end

  - id: evaluate
    name: Evaluate Implementations
    type: convergence
    execution:
      mode: human_only
      eligible_actor_types:
        - human
    required_inputs:
      - branch_outcomes
    outcomes:
      - id: selected
        name: Approach Selected
        next_step: finalize
        commit:
          status: In Review
      - id: neither_acceptable
        name: Neither Approach Acceptable
        next_step: end
        commit:
          status: Rejected
    converge: converge-implementations

  - id: finalize
    name: Finalize Selected Approach
    type: review
    execution:
      mode: human_only
      eligible_actor_types:
        - human
    required_inputs:
      - selected_implementation
    outcomes:
      - id: accepted
        name: Final Review Passed
        next_step: end
        commit:
          status: Completed
      - id: needs_rework
        name: Needs Rework
        next_step: evaluate

divergence_points:
  - id: diverge-implementations
    name: Parallel Implementations
    branches:
      - id: branch-a
        name: Approach A
        start_step: implement-a
      - id: branch-b
        name: Approach B
        start_step: implement-b

convergence_points:
  - id: converge-implementations
    name: Evaluate Implementations
    branches: [branch-a, branch-b]
    strategy: select_one
    evaluation_step: evaluate
```

---

## 9. Cross-References

- [Constitution](/governance/Constitution.md) §6 — Controlled Divergence mandate
- [Domain Model](/architecture/domain-model.md) §6 — Divergence and Convergence entity model
- [Workflow Definition Format](/architecture/workflow-definition-format.md) §3.3 — Declarative format for divergence and convergence points
- [System Components](/architecture/components.md) §4.3 — Workflow Engine responsibilities
- [ADR-001](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md) — Durable outcomes committed to Git
- [Data Model](/architecture/data-model.md) — Runtime Store schema
- [Task-to-Workflow Binding](/architecture/task-workflow-binding.md) §3.8 — Branch governance inheritance

---

## 10. Evolution Policy

This execution model will evolve as divergence patterns are implemented and used in practice.

Areas expected to require refinement:

- Cancellation semantics — how to cancel individual branches or the entire divergence
- Timeout handling — what happens when a branch exceeds time limits during active divergence
- Dynamic branching — runtime creation and expansion of branches during exploratory divergence
- Multi-stage convergence — supporting workflows with multiple evaluation and convergence phases
- Experimentation support — integration of feature flags, traffic routing, and external metrics into the `experiment` convergence strategy

Changes should be captured as ADRs when they alter the foundational constraints from §2.
