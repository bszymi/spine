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

The [Constitution](/governance/constitution.md) (§6) mandates that parallel execution must be explicit, all outcomes must be preserved, and convergence must occur through explicit evaluation steps. The [Domain Model](/architecture/domain-model.md) (§6) establishes that divergence creates parallel steps within a single Run. The [Workflow Definition Format](/architecture/workflow-definition-format.md) (§3.3) defines the declarative structure for divergence and convergence points.

This document specifies the runtime execution semantics — what happens when a Run reaches a divergence point, how parallel branches execute, and how convergence evaluates and commits results.

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
2. Creates a **branch execution context** for each declared branch
3. Each branch context tracks:
   - `branch_id` — reference to the declared branch
   - `status` — pending, in_progress, completed, failed
   - `current_step` — the step currently executing in this branch
   - `artifacts` — artifacts produced by this branch
   - `outcome` — the terminal result of this branch
4. Activates each branch's `start_step` for execution

All branches belong to the same Run. Divergence does not create new Runs.

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

Convergence begins when all branches listed in the convergence point have reached a terminal state (completed or failed). The convergence point declares which branches it expects:

```yaml
convergence_points:
  - id: converge-implementations
    name: Evaluate Implementations
    branches: [branch-a, branch-b]
    strategy: select_one
    evaluation_step: evaluate
```

The Workflow Engine activates the convergence point's evaluation step when all listed branches have terminated. Convergence does not begin while any branch is still in progress.

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

### 4.3 Handling Partial Branch Completion

When some branches complete but others fail:

| Strategy | Behavior |
|----------|----------|
| `select_one` | Convergence may proceed if at least one branch completed successfully. The evaluation step can only select from completed branches. |
| `merge` | Convergence may proceed at the evaluation step's discretion. The evaluator decides whether a partial merge is acceptable. |
| `require_all` | Convergence fails. The Run follows the evaluation step's failure outcome. |

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

1. **Selected branch artifacts** — the winning branch's artifacts (or merged result) are committed to the main artifact path
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
│   ├── triggered_at
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
│   │   ├── merged_artifact (for merge)
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

- [Constitution](/governance/constitution.md) §6 — Controlled Divergence mandate
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
- Dynamic branching — whether the number of branches can be determined at runtime rather than statically declared

Changes should be captured as ADRs when they alter the foundational constraints from §2.
