# ADR-004: Evaluation and Acceptance Model

**Status:** Accepted
**Date:** 2026-03-11
**Decision Makers:** Spine Architecture

---

## Context

Spine workflows include review, validation, and acceptance points.

During discovery, the term "decision" was found to describe multiple different things:

1. **Step-level evaluation**
   - whether a workflow step may proceed
   - whether rework is required
   - whether a step should be retried or routed backward

2. **Task-level acceptance or rejection**
   - whether the overall task outcome is accepted
   - whether the resulting deliverable may proceed to deployment or completion
   - whether follow-up work is required
   - whether the task should be closed with no further action

Treating all of these as a single generic "Decision" entity would blur important distinctions between workflow control and durable governed outcomes.

The architecture must define:

- what is evaluated
- where evaluation outcomes are stored
- what effect those outcomes have on workflow and artifact state

---

## Decision

### 1. Decision is not a standalone core domain entity

Spine does not introduce a generic `Decision` entity in the core domain model.

Instead, evaluation is modeled in two distinct forms:

- **Step-level outcomes** within runtime workflow execution
- **Task-level acceptance outcomes** recorded durably in the Task artifact

This keeps workflow control separate from governed artifact truth.

---

### 2. Step-level evaluation is part of Run execution

Evaluation of an individual step is a runtime execution concern.

Examples:

- step accepted to continue
- step needs rework
- step failed
- step retried

These outcomes determine workflow routing, for example:

- proceed to next step
- repeat current step
- return to previous step
- fail the run

Step-level outcomes are **not standalone durable domain entities**.
They belong to execution state within a Run.

They do not need to be stored as separate Git-backed records unless they produce a durable artifact or a durable change in governed state.

---

### 3. Task-level acceptance is stored in the Task artifact

Acceptance or rejection of the overall task outcome is a governed, durable result.

This must be recorded in the **Task artifact** in Git.

Examples:

- task approved
- task rejected with follow-up required
- task rejected and closed with no further action

Task-level acceptance records the governed outcome of the task, including rationale and any required follow-up.

This allows Git history to preserve durable acceptance state without introducing a separate generic Decision entity.

---

### 4. Task-level rejection has two distinct meanings

Task-level rejection must distinguish between:

#### Rejected with follow-up required

The task outcome is not accepted in its current form, and further work is required.

Typical consequences:

- close the current task in its current scope
- create a new successor task for the required changes
- link the successor task from the rejected task
- preserve rationale in the task artifact

This model avoids mutating the historical meaning of the original task.

---

#### Rejected and closed

The task outcome is not accepted, and no further work will be done.

Typical consequences:

- close the current task
- record the rejection rationale
- do not create successor work unless later requested separately

This represents a genuine stop, not a rework loop.

---

### 5. UAT and similar validation steps act as task-level acceptance gates

Some workflow steps, such as UAT, are not merely step-control checks.

They evaluate the deliverable at the level of the whole task.

For these cases:

- the step execution itself still has a runtime outcome
- but the meaningful governed result must also be recorded in the Task artifact as task-level acceptance or rejection

This distinguishes runtime workflow progression from durable acceptance of the deliverable.

---

### 6. Language should distinguish step outcomes from task outcomes

To reduce ambiguity, Spine should avoid using the same vocabulary for both layers.

Preferred examples:

#### Step-level outcomes
- `accepted_to_continue`
- `needs_rework`
- `failed`

#### Task-level outcomes
- `approved`
- `rejected_with_followup`
- `rejected_closed`

This avoids confusion between "redo this step" and "the overall task outcome was not accepted."

---

## Consequences

### Positive

- Workflow control remains separate from governed artifact truth
- The domain model avoids an overly broad and ambiguous Decision entity
- Task-level acceptance becomes durable and auditable in Git
- Rejection semantics become clearer and more actionable
- Successor work can be created without corrupting the historical meaning of earlier tasks

### Negative

- Evaluation logic exists at two distinct layers and must be explained clearly
- Task artifacts need structured acceptance information
- Some workflow steps (such as UAT) operate across both runtime and governed layers

---

## Architectural Implications

The architecture distinguishes between:

### Step Outcome
A runtime execution result that controls workflow progression.

Examples:
- accepted to continue
- needs rework
- failed

This is part of Run execution behavior.

### Task Acceptance Outcome
A durable governed result recorded in the Task artifact.

Examples:
- approved
- rejected with follow-up required
- rejected and closed

This is part of durable Git-backed system truth.

### Successor Task
A new task created when rejected work requires additional or corrected follow-up.

This preserves historical traceability while allowing work to continue under new scope.

---

## Out of Scope

This ADR does not define:

- the exact markdown structure for recording task acceptance in task files
- the exact schema for step execution state in runtime systems
- how successor task creation is automated
- release/deployment workflow design beyond task-level acceptance

These may be covered by later ADRs.

---

## Future Work

Future ADRs or model updates may define:

- task artifact structure for acceptance records
- successor task linkage conventions
- divergence and convergence execution model
- artifact taxonomy and status semantics
