---
type: Architecture
title: Workflow Engine State Machine
status: Living Document
version: "0.1"
---

# Workflow Engine State Machine

---

## 1. Purpose

This document defines the formal state machine specifications for the Workflow Engine's core runtime entities: Run, StepExecution, DivergenceContext, and Branch.

The [Domain Model](/architecture/domain-model.md) §5 provides informal lifecycle diagrams. The [Error Handling](/architecture/error-handling-and-recovery.md) model defines failure semantics. The [Runtime Schema](/architecture/runtime-schema.md) defines the database constraints. This document formalizes the state transitions into explicit transition matrices with triggers, guards, and effects, enabling unambiguous implementation.

---

## 2. Run State Machine

### 2.1 States

| State | Description |
|-------|-------------|
| `pending` | Run created, workflow resolved and pinned, awaiting activation |
| `active` | At least one step is executing or ready for assignment |
| `paused` | Execution suspended — waiting for external dependency, human input, or operator action |
| `committing` | Terminal step completed; durable outcome is being committed to Git |
| `completed` | All steps reached terminal outcomes; durable results committed to Git |
| `failed` | A step failed permanently and the Run cannot proceed |
| `cancelled` | Explicitly cancelled by an operator |

### 2.1.1 Run Mode

Runs carry a `mode` field (per [ADR-006](/architecture/adr/ADR-006-planning-runs.md)) that determines their purpose:

| Mode | Description |
|------|-------------|
| `standard` | Default. Run executes against an existing artifact on `main`. Created via `StartRun()`. |
| `planning` | Run creates new artifacts on a branch. Artifacts are branch-local until merge. Created via `StartPlanningRun()`. |

Both modes follow the same state machine. The mode does not introduce new states or transitions — it affects only how the run is initialized (workflow resolution uses `mode: creation` bindings) and how write context is validated (planning runs allow `run_id` without `task_path`).

### 2.2 Transition Matrix

| From | To | Trigger | Guard | Effects |
|------|-----|---------|-------|---------|
| `pending` | `active` | `run.activate` | Entry step exists and is assignable | Set `started_at`; create first StepExecution in `waiting`; emit `run_started` |
| `active` | `active` | `step.completed` | Next step exists (not `end`) | Create next StepExecution; update `current_step_id` |
| `active` | `paused` | `step.blocked` | Step is waiting for external dependency or long-running human action | Record pause reason; emit `run_paused` |
| `active` | `committing` | `step.completed` | Next step is `end` and outcome has `commit` effect | Begin Git commit (durable outcome + merge) |
| `active` | `completed` | `step.completed` | Next step is `end` and outcome has no `commit` effect | Set `completed_at`; emit `run_completed` |
| `active` | `failed` | `step.failed_permanently` | Step exhausted retries or permanent error | Preserve runtime state; emit `run_failed` |
| `active` | `failed` | `divergence.failed` | Convergence failed (e.g., `require_all` with branch failure) | Preserve runtime state; emit `run_failed` |
| `active` | `cancelled` | `run.cancel` | Operator issues cancellation | Terminate in-progress steps; set `completed_at`; emit `run_cancelled` |
| `paused` | `active` | `run.resume` | Blocking condition resolved | Resume from current step; emit `run_resumed` |
| `paused` | `cancelled` | `run.cancel` | Operator issues cancellation | Set `completed_at`; emit `run_cancelled` |
| `committing` | `completed` | `git.commit_succeeded` | Git commit and merge confirmed | Set `completed_at`; emit `run_completed` |
| `committing` | `committing` | `git.commit_failed_transient` | Transient Git failure, retries remain | Retry commit |
| `committing` | `failed` | `git.commit_failed_permanent` | Git commit failed permanently after retries | Preserve runtime state; emit `run_failed` |

### 2.3 Invalid Transitions

| Attempted | From | Handling |
|-----------|------|---------|
| Any transition | `completed` | Reject — completed Runs are immutable |
| Any transition | `failed` | Reject — failed Runs are immutable (start a new Run instead) |
| Any transition | `cancelled` | Reject — cancelled Runs are immutable |
| `active` → `pending` | `active` | Reject — Runs do not revert to pending |
| `paused` → `pending` | `paused` | Reject — paused Runs resume to active, not pending |

### 2.4 Recovery Transitions

After Workflow Engine crash, recovery proceeds based on the last persisted state (per [Error Handling](/architecture/error-handling-and-recovery.md) §6.1):

| Persisted State | Recovery Action |
|----------------|----------------|
| `pending` | Re-activate (transition to `active`) |
| `active` | Inspect current step; resume, retry, or timeout as appropriate |
| `paused` | Remain paused; operator may resume or cancel |
| `committing` | Re-attempt Git commit (idempotent) |
| `completed` | No action (terminal) |
| `failed` | No action (terminal) |
| `cancelled` | No action (terminal) |

---

## 3. StepExecution State Machine

### 3.1 States

| State | Description |
|-------|-------------|
| `waiting` | Step is ready but no actor has been assigned |
| `assigned` | An actor has been assigned; awaiting the actor to begin work |
| `in_progress` | Actor has acknowledged the assignment and is actively working |
| `blocked` | Execution paused — waiting for external dependency, precondition, or resource |
| `completed` | Actor submitted a result with a valid outcome |
| `failed` | Step failed (invalid result, timeout, actor failure) |
| `skipped` | Step was bypassed per workflow rules |

### 3.2 Transition Matrix

| From | To | Trigger | Guard | Effects |
|------|-----|---------|-------|---------|
| `waiting` | `assigned` | `step.assign` | Eligible actor found; skill eligibility validated | Create ActorAssignment; set `actor_id`; emit `step_assigned` |
| `waiting` | `failed` | `step.timeout` | Step timeout reached while waiting | Set `outcome` to `timeout_outcome`; emit `step_timeout` |
| `waiting` | `skipped` | `step.skip` | Workflow definition permits skipping | Emit `step_completed` with skip reason |
| `assigned` | `in_progress` | `actor.acknowledged` | Actor confirmed receipt | Set `started_at`; emit `step_started` |
| `assigned` | `failed` | `step.timeout` | Assignment timeout reached | Mark assignment as `timed_out`; emit `step_timeout` |
| `assigned` | `waiting` | `actor.unavailable` | Actor became unavailable before starting | Cancel assignment; return to pool for reassignment |
| `in_progress` | `completed` | `step.submit` | Outcome is valid, artifacts pass validation | Set `outcome_id`, `completed_at`; update assignment to `completed`; emit `step_completed` |
| `in_progress` | `failed` | `step.submit_invalid` | Outcome invalid or artifacts fail validation | Classify error; emit `step_failed` |
| `in_progress` | `failed` | `step.timeout` | Step timeout reached during execution | Apply `timeout_outcome`; emit `step_timeout` |
| `in_progress` | `failed` | `actor.unavailable` | Actor became unavailable during execution | Classify as `actor_unavailable`; emit `step_failed` |
| `in_progress` | `blocked` | `step.blocked` | Step waiting for external dependency or precondition | Record block reason; may trigger Run `paused` |
| `blocked` | `in_progress` | `step.unblocked` | Blocking condition resolved | Resume execution |
| `blocked` | `failed` | `step.timeout` | Step timeout reached while blocked | Apply `timeout_outcome`; emit `step_timeout` |

### 3.3 Retry Behavior

When a StepExecution transitions to `failed`:

| Condition | Action |
|-----------|--------|
| `attempt < retry.limit` and error is transient | Create a new StepExecution for the same step with `attempt + 1`; emit `retry_attempted` |
| `attempt >= retry.limit` or error is permanent | Escalate to Run failure (`step.failed_permanently` trigger on Run) |

Each retry creates a **new StepExecution record** — the failed record is preserved for diagnosis. The new execution starts in `waiting` state.

### 3.3.1 Failure Classification

Every `failed` StepExecution records a `failure_classification` to distinguish failure types:

| Classification | Meaning | Retryable |
|---------------|---------|-----------|
| `transient_error` | Temporary failure (network, timeout, actor busy) | Yes |
| `permanent_error` | Unrecoverable failure (schema violation, logic error) | No |
| `actor_unavailable` | Actor became unreachable or unresponsive | Yes (with different actor) |
| `invalid_result` | Actor returned invalid outcome or artifacts | Yes (if actor might correct) |
| `git_conflict` | Git commit conflict during durable outcome | No (operator intervention) |
| `timeout` | Step exceeded its configured timeout | Depends on `timeout_outcome` |

The classification determines whether retry is attempted and informs operator diagnostics. It is stored in the `error_detail` JSONB field alongside the error message.

### 3.4 Invalid Transitions

| Attempted | From | Handling |
|-----------|------|---------|
| Any transition | `completed` | Reject — completed steps are immutable |
| Any transition | `skipped` | Reject — skipped steps are immutable |
| `in_progress` → `waiting` | `in_progress` | Reject — cannot un-start a step |
| `completed` → `failed` | `completed` | Reject — cannot fail an already-completed step |

### 3.5 Recovery Transitions

| Persisted State | Recovery Action |
|----------------|----------------|
| `waiting` | Re-attempt assignment |
| `assigned` | Check actor availability; if still reachable, wait; if not, transition to `waiting` for reassignment |
| `in_progress` | Check for pending result; if none, apply timeout logic |
| `blocked` | Remain blocked; check if blocking condition resolved |
| `completed` | No action (terminal) |
| `failed` | Check retry eligibility; if eligible, create new execution |
| `skipped` | No action (terminal) |

---

## 4. DivergenceContext State Machine

### 4.1 States

| State | Description |
|-------|-------------|
| `pending` | Divergence point reached in workflow; branches not yet created |
| `active` | Branches are executing in parallel |
| `converging` | Entry policy satisfied; evaluation step in progress |
| `resolved` | Convergence completed; result committed |
| `failed` | Convergence failed |

### 4.2 Transition Matrix

| From | To | Trigger | Guard | Effects |
|------|-----|---------|-------|---------|
| `pending` | `active` | `divergence.start` | Branch definitions resolved | Create Branch records; create Git branches; set `triggered_at`; set Run's `current_step_id` to null; emit `divergence_started` |
| `active` | `active` | `branch.completed` | Entry policy not yet satisfied | Update branch status |
| `active` | `active` | `branch.failed` | Entry policy not yet satisfied and strategy tolerates failure | Update branch status |
| `active` | `converging` | `branch.completed` or `branch.failed` | Entry policy satisfied | Create StepExecution for evaluation step |
| `active` | `failed` | `branch.failed` | Strategy is `require_all` and a branch failed | Emit failure; escalate to Run |
| `converging` | `resolved` | `evaluation.completed` | Evaluation step produced a valid outcome | Record convergence result; commit selected/merged artifacts; set `resolved_at`; restore Run's `current_step_id`; emit `convergence_completed` |
| `converging` | `failed` | `evaluation.failed` | Evaluation step failed or rejected all results | Escalate to Run failure |

### 4.3 Entry Policy Evaluation

The transition from `active` to `converging` is governed by the convergence entry policy:

| Policy | Condition for Transition |
|--------|------------------------|
| `all_branches_terminal` | All branches are `completed` or `failed` |
| `minimum_completed_branches` | At least `min` branches are `completed` |
| `deadline_reached` | Deadline duration has elapsed since `triggered_at` |
| `manual_trigger` | Operator explicitly triggers convergence |

### 4.4 Invalid Transitions

| Attempted | From | Handling |
|-----------|------|---------|
| Any transition | `resolved` | Reject — resolved divergence is immutable |
| Any transition | `failed` | Reject — failed divergence is immutable |
| `active` → `pending` | `active` | Reject — cannot un-start divergence |
| `converging` → `active` | `converging` | Reject — cannot return to active execution after convergence began |

### 4.5 Exploratory Divergence Extensions

In exploratory mode, the `active` state has additional behavior:

| Trigger | Guard | Effect |
|---------|-------|--------|
| `branch.create` | `divergence_window` is `open` and branch count < `max_branches` | Create new Branch record and Git branch |
| `divergence.close_window` | `divergence_window` is `open` | Set `divergence_window` to `closed`; no new branches may be created |

The `divergence_window` must be `closed` before transitioning to `converging` (either explicitly or by the entry policy).

---

## 5. Branch State Machine

### 5.1 States

| State | Description |
|-------|-------------|
| `pending` | Branch created but execution has not started |
| `in_progress` | Branch steps are executing |
| `completed` | Branch reached its terminal step |
| `failed` | A step within the branch failed permanently |

### 5.2 Transition Matrix

| From | To | Trigger | Guard | Effects |
|------|-----|---------|-------|---------|
| `pending` | `in_progress` | `branch.start` | Start step exists and is assignable | Create StepExecution for branch start step; set `current_step_id` |
| `in_progress` | `in_progress` | `branch_step.completed` | Next step exists within branch | Create next StepExecution; update `current_step_id` |
| `in_progress` | `completed` | `branch_step.completed` | Branch step routes to convergence or `end` | Set `completed_at`; record branch outcome; notify DivergenceContext |
| `in_progress` | `failed` | `branch_step.failed_permanently` | Step exhausted retries within branch | Set branch outcome; notify DivergenceContext |

### 5.3 Invalid Transitions

| Attempted | From | Handling |
|-----------|------|---------|
| Any transition | `completed` | Reject — completed branches are immutable |
| Any transition | `failed` | Reject — failed branches are immutable |

---

## 6. Engine Responsibility

### 6.1 Who Drives Transitions

The Workflow Engine is the sole authority for state transitions. No other component may directly modify runtime state.

| Responsibility | Owner | Description |
|---------------|-------|-------------|
| Transition evaluation | Workflow Engine | Evaluates triggers and guards; applies state changes |
| Timeout detection | Workflow Engine (scheduler) | Periodically scans for steps exceeding their timeout |
| Retry initiation | Workflow Engine | Creates new StepExecution after transient failure |
| Assignment orchestration | Workflow Engine → Actor Gateway | Engine decides when to assign; Actor Gateway delivers |
| Git commit execution | Workflow Engine → Artifact Service | Engine triggers commit; Artifact Service executes |
| Event emission | Workflow Engine → Event Router | Engine emits events as transition effects |
| Orphan detection | Workflow Engine (scheduler) | Scans for Runs without recent activity |

### 6.2 Actor vs Engine Boundaries

Actors do **not** drive state transitions directly. Actors submit results through the Actor Gateway, which delivers them to the Workflow Engine. The engine then evaluates the result and decides the transition.

- An actor submitting a step result does not automatically complete the step — the engine validates the result first
- An actor cannot cancel a Run or skip a step — those are engine or operator actions
- An actor cannot assign themselves to a step — the engine performs selection (per [Actor Model](/architecture/actor-model.md) §4)

### 6.3 Scheduler Functions

The Workflow Engine includes a scheduler component responsible for time-based triggers:

- **Timeout scanning** — periodically checks active steps against their timeout configuration
- **Orphan detection** — periodically scans for Runs without recent step activity (default threshold: 30 days, configurable via `SPINE_ORPHAN_THRESHOLD`)
- **Retry scheduling** — schedules retry attempts with appropriate backoff delays
- **Convergence deadline** — monitors `deadline_reached` entry policies

The scheduler operates on the persisted state in the Runtime Store. It does not maintain in-memory timers that would be lost on crash.

---

## 7. State Persistence and Atomicity

### 6.1 Persistence Rules

All state transitions must be persisted to the Runtime Store **before** their effects are executed:

1. Write the new state to the database
2. Then emit events, create assignments, or trigger Git commits
3. If the effect fails, the state is already persisted and recovery can re-attempt the effect

This ensures that a crash between state change and effect execution results in a recoverable state, not a corrupted one.

### 6.2 Atomicity

State transitions within a single entity are atomic:

- A Run status change and its associated StepExecution creation are written in a single database transaction
- A Branch status change and its notification to the DivergenceContext are written in a single transaction
- Git commits and their corresponding state updates are **not** atomic (Git is external) — the state machine accounts for this by treating uncommitted durable outcomes as retryable

### 6.3 Concurrency

- Multiple StepExecutions may be active concurrently within a Run (during divergence)
- Multiple Branches execute concurrently within a DivergenceContext
- The Workflow Engine must use optimistic concurrency control or row-level locking to prevent conflicting state transitions on the same entity

---

## 8. Constitutional Alignment

| Principle | How the State Machine Supports It |
|-----------|----------------------------------|
| Governed Execution (§4) | Every state transition has explicit triggers, guards, and effects — no implicit behavior |
| Reproducibility (§7) | Terminal states are immutable; recovery is deterministic from persisted state |
| Disposable Database (§8) | State machine operates on runtime state; durable outcomes are committed to Git independently |
| Source of Truth (§2) | State machine drives execution but Git commits are the durable record |

---

## 9. Cross-References

- [Domain Model](/architecture/domain-model.md) §5 — Informal lifecycle diagrams
- [Error Handling](/architecture/error-handling-and-recovery.md) §3–§6 — Failure, retry, timeout, and recovery semantics
- [Runtime Schema](/architecture/runtime-schema.md) §4 — Database tables and status constraints
- [Divergence and Convergence](/architecture/divergence-and-convergence.md) §5 — Runtime state model for branches
- [Workflow Definition Format](/architecture/workflow-definition-format.md) §3.2 — Step outcomes and routing
- [Actor Model](/architecture/actor-model.md) §4 — Actor selection and assignment
- [Git Integration](/architecture/git-integration.md) §6 — Branch strategy and merge operations
- [Event Schemas](/architecture/event-schemas.md) — Events emitted on state transitions

---

## 10. Evolution Policy

This state machine specification is expected to evolve as the system is implemented.

Areas expected to require refinement:

- Run resumption after failure (currently requires a new Run)
- Nested divergence state machine interactions
- State machine visualization tooling
- Performance optimization for high-concurrency divergence
- Machine-readable state machine DSL for code generation and test derivation

Changes that alter state transitions, add new states, or change recovery behavior should be captured as ADRs.
