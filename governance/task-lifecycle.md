---
type: Governance
title: Task Lifecycle and Terminal Outcomes
status: Living Document
version: "0.1"
---

# Task Lifecycle and Terminal Outcomes

---

## 1. Purpose

This document defines the task lifecycle model for Spine, with emphasis on the boundary between operational execution state and durable governance outcomes.

The key rule is:

**Starting work on a task must not modify the main branch. Only terminal governance outcomes modify durable artifact state.**

This distinction ensures that Git history reflects governed decisions, not operational noise.

---

## 2. Foundational Principle

A task artifact in Git represents governed intent — what work should be done, why, and under what acceptance criteria.

Execution of that work happens through Runs in the runtime system. Runs are operational constructs that live in the [Runtime Store](/architecture/data-model.md), not in Git.

Only when execution reaches a terminal governance outcome does the task artifact in Git get updated. This keeps the main branch clean, meaningful, and reconstructible.

Work attempts that are abandoned without a governance decision do not require durable recording.

---

## 3. Lifecycle States

### 3.1 State Overview

```
Draft → Pending → [Runtime Execution] → Terminal Outcome
```

The task artifact in Git tracks only the governed states. Runtime execution states exist in the Workflow Engine's Runtime Store.

### 3.2 Governed States (in Git)

These states are recorded in the task artifact's front matter and committed to the main branch.

| State | Meaning |
|-------|---------|
| `Draft` | Task is being defined; not yet ready for execution |
| `Pending` | Task is fully defined and ready to be picked up |
| `Completed` | Terminal: deliverable accepted, work finished |
| `Cancelled` | Terminal: task withdrawn before completion |
| `Rejected` | Terminal: deliverable evaluated and not accepted |
| `Superseded` | Terminal: task replaced by successor work |
| `Abandoned` | Terminal: task stopped by governance decision |

### 3.3 Runtime States (in Runtime Store)

These states exist only in the Workflow Engine's operational state. They are not committed to Git.

| State | Meaning |
|-------|---------|
| `Assigned` | An actor has been assigned to work on the task |
| `In Progress` | Active execution is underway within a Run |
| `In Review` | Deliverable submitted for evaluation |
| `Blocked` | Execution paused due to dependency or external constraint |
| `Retrying` | A failed step is being retried within the Run |

Runtime states are tracked per Run, not per task artifact. A task may have multiple Runs over its lifetime.

---

## 4. Transition Rules

### 4.1 Governed Transitions (modify main branch)

These transitions update the task artifact in Git:

```
Draft → Pending
```
Task definition is complete and ready for execution. This is the only non-terminal transition that modifies Git — it represents a governance decision that the task is ready.

```
Pending → Completed
Pending → Cancelled
Pending → Superseded
```
Terminal outcomes from a task that was never started or whose execution was decided without running.

```
[After Runtime Execution] → Completed
[After Runtime Execution] → Rejected
[After Runtime Execution] → Cancelled
[After Runtime Execution] → Superseded
[After Runtime Execution] → Abandoned
```
Terminal outcomes after one or more Runs have executed.

### 4.2 Operational Transitions (runtime only)

These transitions happen within the Workflow Engine and do not modify Git:

```
Pending → Assigned → In Progress → In Review → [back to runtime states or forward to terminal]
```

Starting a Run, assigning actors, progressing through steps, submitting for review — none of these modify the task artifact in Git.

### 4.3 Prohibited Transitions

- `Complete → any` — completed tasks are final (create follow-up work instead)
- `Cancelled → any` — cancelled tasks are final
- `Abandoned → any` — abandoned tasks are final
- Skipping from `Draft` directly to a terminal state without passing through `Pending` (except `Cancelled` — a draft may be cancelled before it becomes pending)

---

## 5. Terminal Outcomes

Terminal outcomes are the governed decisions that end a task's lifecycle and are committed to the main branch.

### 5.1 Completed

The task deliverable has been accepted and the work is finished.

**Effect on artifact:**
- Status updated to `Completed`
- Acceptance recorded (per [ADR-004](/architecture/adr/ADR-004-evaluation-and-acceptance-model.md))

**When used:**
- Deliverable meets acceptance criteria
- Review/evaluation step passes

---

### 5.2 Cancelled

The task has been withdrawn before completion. No deliverable was produced or evaluated.

**Effect on artifact:**
- Status updated to `Cancelled`
- Cancellation rationale recorded in the artifact

**When used:**
- Task is no longer relevant due to scope change
- Parent epic or initiative is closed
- Task was created in error

---

### 5.3 Rejected

The task deliverable has been evaluated and not accepted.

**Effect on artifact:**
- Status updated to `Rejected`
- Rejection rationale recorded
- If follow-up is required: successor task created and linked (per [ADR-004](/architecture/adr/ADR-004-evaluation-and-acceptance-model.md))
- If no follow-up: task is closed with rationale

**When used:**
- UAT or review step fails
- Deliverable does not meet acceptance criteria
- Governed evaluation determines the outcome is not acceptable

**Two rejection types (per ADR-004):**
- `Rejected with follow-up` — successor task created
- `Rejected and closed` — no further work

---

### 5.4 Superseded

The task has been replaced by successor work that redefines the scope.

**Effect on artifact:**
- Status updated to `Superseded`
- Link to successor task recorded in front matter

**When used:**
- Requirements changed significantly enough to warrant a new task
- Architectural decisions invalidated the original task scope
- Task is being split into multiple successor tasks

---

### 5.5 Abandoned

The task has been stopped by an explicit governance decision. Unlike cancellation, abandonment typically applies to tasks where work was started but a decision was made to stop.

**Effect on artifact:**
- Status updated to `Abandoned`
- Abandonment rationale recorded

**When used:**
- Work was started but deprioritized by governance decision
- External constraints make completion infeasible
- The approach was found to be unviable after investigation

**Note:** Abandonment requires an explicit governance decision. Work attempts that simply stop without a decision do not modify the task artifact — they remain as incomplete Runs in the Runtime Store until the task reaches a governed terminal outcome.

---

## 6. What Does NOT Modify the Main Branch

The following activities are runtime-only and do not produce Git commits:

- Starting work on a task (creating a Run)
- Assigning an actor to a step
- Progressing through workflow steps
- Submitting a deliverable for review (before the review outcome is decided)
- Retrying a failed step
- Pausing or blocking execution
- Abandoning a Run without a governance decision about the task itself
- Any step-level outcome that does not affect the task's terminal state

These activities are tracked in the [Runtime Store](/architecture/data-model.md) by the [Workflow Engine](/architecture/components.md).

---

## 7. Relationship to Runs

A task may have multiple Runs over its lifetime:

- A Run may fail and be restarted
- A Run may be abandoned without affecting the task's governed state
- Multiple Runs may execute in parallel (controlled divergence)

The task artifact in Git does not track individual Runs. It tracks only its governed lifecycle state and terminal outcome.

Run history exists in the Runtime Store for operational purposes. If the Runtime Store is lost, Run history may be lost, but the task's terminal outcome remains in Git.

---

## 8. Example Lifecycle

```
1. Task created as Draft (Git commit: task artifact created)
2. Task moved to Pending (Git commit: status updated)
3. Run started (Runtime Store only — no Git commit)
4. Actor assigned to step (Runtime Store only)
5. Work in progress (Runtime Store only)
6. Deliverable submitted for review (Runtime Store only)
7. Review outcome: accepted (Runtime Store → triggers Git commit)
8. Task marked Completed (Git commit: status updated, acceptance recorded)
```

Steps 3–6 do not touch the main branch. Only steps 1, 2, and 8 produce Git commits.

---

## 9. Cross-References

- [Domain Model](/architecture/domain-model.md) — Entity lifecycles (§5) and Run definition (§3.5)
- [Data Model](/architecture/data-model.md) — Git truth vs runtime state boundary (§2, §3)
- [System Components](/architecture/components.md) — Workflow Engine and Runtime Store behavior (§4.3)
- [ADR-004](/architecture/adr/ADR-004-evaluation-and-acceptance-model.md) — Evaluation and acceptance outcomes, rejection types
- [Constitution](/governance/Constitution.md) — Source of Truth (§2), Governed Execution (§4)
- [Artifact Schema](/governance/artifact-schema.md) — Task status enums and acceptance fields
- [Task-to-Workflow Binding](/architecture/task-workflow-binding.md) — How tasks are bound to workflows, `work_type` mutability rules

---

## 10. Evolution Policy

This document is expected to evolve as workflow definitions are implemented and operational experience is gained.

Changes must be versioned in Git and must not contradict the [Charter](/governance/Charter.md) or [Constitution](/governance/Constitution.md).

Changes that alter the boundary between runtime state and durable governance outcomes should be captured as ADRs.
