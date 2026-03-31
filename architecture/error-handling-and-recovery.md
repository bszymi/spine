---
type: Architecture
title: Error Handling and Recovery Model
status: Living Document
version: "0.1"
---

# Error Handling and Recovery Model

---

## 1. Purpose

This document defines how the Spine runtime handles errors, failures, and recovery across all layers of execution.

The [Domain Model](/architecture/domain-model.md) defines retry limits and timeouts on Step Definitions (§3.3) and acknowledges that failed Runs preserve state (§5.2). The [Data Model](/architecture/data-model.md) establishes that runtime state is ephemeral and recoverable with limitations (§2.3, §5.2). The [Workflow Definition Format](/architecture/workflow-definition-format.md) provides retry and timeout configuration on steps (§3.2).

This document specifies the concrete behavior: what happens when things go wrong, how failures propagate, and how the system recovers.

---

## 2. Error Classification

All errors in Spine are classified into two categories that determine retry behavior:

### 2.1 Transient Errors

Errors that may resolve on their own or with a retry. The system should retry automatically within configured limits.

Examples:
- Network timeout reaching an external service
- Temporary Git operation failure (lock contention, transient I/O error)
- Actor temporarily unavailable (e.g., LLM rate limit, human actor offline)
- Queue delivery failure
- Database connection timeout

### 2.2 Permanent Errors

Errors that will not resolve with retries. The system should fail immediately and escalate.

Examples:
- Workflow definition not found or invalid
- Precondition violation (artifact in wrong state)
- Validation failure (step output does not meet requirements)
- Actor returns a structurally invalid result
- Git conflict that cannot be auto-resolved
- Authorization failure (actor lacks permissions)

### 2.3 Classification Responsibility

The component that encounters the error is responsible for classifying it:

| Component | Classifies |
|-----------|-----------|
| Workflow Engine | Step precondition failures, invalid state transitions |
| Artifact Service | Git operation failures, schema validation failures |
| Actor Gateway | Actor availability, response validity |
| Projection Service | Sync failures, parse errors |
| Event Router | Delivery failures |

When classification is ambiguous, the error should be treated as transient for the first occurrence and permanent if it persists after retries.

### 2.4 Failure Semantics (Core Guarantees)

The system provides the following guarantees:

- **No duplicate durable outcomes** — Git commits are the single boundary of durable state
- **Step execution may be repeated** — retries and recovery may re-run steps
- **External side effects must be idempotent or compensatable** — steps must tolerate re-execution
- **Runtime state loss may cause re-execution** — correctness is prioritized over continuity

---

## 3. Step Failure Handling

### 3.1 Step Execution Failure

When a step execution fails, the Workflow Engine follows this sequence:

```
Step fails
  → Is error transient?
    → Yes: Has retry limit been reached?
      → No: Schedule retry with backoff → Re-execute step
      → Yes: Mark step as failed → Escalate to Run
    → No (permanent): Mark step as failed → Escalate to Run
```

### 3.2 Retry Logic

Retry behavior is configured per step in the workflow definition:

```yaml
retry:
  limit: 3              # Maximum retry attempts
  backoff: exponential   # fixed, linear, exponential
```

**Backoff strategies:**

| Strategy | Behavior |
|----------|----------|
| `fixed` | Constant delay between retries (e.g., 30s, 30s, 30s) |
| `linear` | Linearly increasing delay (e.g., 30s, 60s, 90s) |
| `exponential` | Exponentially increasing delay (e.g., 30s, 60s, 120s) |

Base delay and maximum delay are determined by the Workflow Engine's configuration, not the workflow definition. The workflow definition controls the strategy and limit.

**Retry rules:**

- Each retry creates a new step execution record with an incremented `attempt` counter
- The same actor may be reassigned, or the Actor Gateway may select a different actor
- Retry attempts are tracked in the Runtime Store and visible to operators
- Manual steps may also be retried (e.g., reassigned to a different human actor after timeout)
- Retrying does not modify Git — retries are operational, not durable

### 3.2.1 Side-Effect Safety

Steps fall into two categories:

- **Pure steps** — no external side effects; safe to retry freely
- **Side-effect steps** — interact with external systems or produce non-idempotent outputs

Side-effect steps must ensure one of the following:

- Idempotent execution (same input produces the same result without duplication)
- Deduplication via identifiers (e.g., idempotency keys)
- Explicit compensation logic defined outside the workflow (operator or system-driven)

The Workflow Engine assumes steps are safe to retry. Responsibility for side-effect safety lies with step design and actor implementation.

### 3.3 Timeout Handling

When a step exceeds its declared `timeout`:

1. The Workflow Engine marks the step execution as timed out
2. If `timeout_outcome` is declared, the step completes with that outcome and normal routing continues
3. If no `timeout_outcome` is declared, the timeout is treated as a step failure and follows the retry/escalation path

Timeout outcomes allow workflows to handle timeouts gracefully. For example, a review step that times out might route to an automatic approval or to cancellation, rather than failing the entire Run.

**Guidance:**
Timeout outcomes should be used cautiously. A timeout does not imply successful completion of the step. Workflows should avoid treating timeouts as implicit success unless explicitly intended.

### 3.4 Actor Failure

When an actor becomes unresponsive or returns invalid results:

**Unresponsive actor:**
- Detected via heartbeat timeout or step timeout
- The step execution is marked as failed with error classification `actor_unavailable`
- If retries remain, the step is retried with a different actor (if available)
- If the same actor is the only eligible actor, the step fails permanently

**Invalid result:**
- Detected by the Workflow Engine during validation
- The step execution is marked as failed with error classification `invalid_result`
- Classified as transient if the actor might produce a valid result on retry (e.g., AI agent)
- Classified as permanent if the validation failure indicates a structural problem
- Classification is performed by the component detecting the failure, but may be influenced by workflow validation rules

---

## 4. Run Failure Handling

### 4.1 When a Run Fails

A Run transitions to `failed` status when:

- A step fails permanently (all retries exhausted, or permanent error)
- A step times out without a recoverable `timeout_outcome` and all retries are exhausted
- During divergence: convergence fails (e.g., `require_all` with a failed branch, or evaluation step rejects all results)

### 4.2 Run Failure Effects

When a Run fails:

1. **Run status** is updated to `failed` in the Runtime Store
2. **Runtime state is preserved** — all step execution records, actor assignments, and error details remain in the Runtime Store for diagnosis
3. **No Git commit is produced** — a failed Run does not modify the task artifact's status in Git. The task remains in its pre-Run governed state (typically `Pending`)
4. **A `run_failed` domain event is emitted** — for observability and operator notification
5. **Partial artifacts** — any artifacts produced by completed steps within the Run remain in their branch paths but are not committed to the canonical artifact path

### 4.3 Run Failure Does Not Affect Task Status

Per the [Task Lifecycle](/governance/task-lifecycle.md), Run failure is a runtime event that does not modify Git. The task artifact remains in its governed state. A new Run may be started for the same task.

The only exception is when an operator makes an explicit governance decision (e.g., marking the task as `Abandoned`) after repeated Run failures.

### 4.4 Run Cancellation

A Run may be cancelled through an explicit `run.cancel` operation. Cancellation is not a failure — it is a deliberate action.

When a Run is cancelled:

1. All in-progress step executions are terminated
2. Run status is updated to `cancelled`
3. No Git commit is produced (unless the cancellation triggers a task-level governance decision)
4. A `run_cancelled` domain event is emitted

---

## 5. Git Operation Failure

### 5.1 Durable Outcome Commit Failure

When the Artifact Service fails to commit a durable outcome to Git:

1. The Workflow Engine does not advance the Run past the step that produced the outcome
2. The commit is retried as a transient error (Git lock contention, network failure, etc.)
3. If retries are exhausted, the step is marked as failed and follows normal escalation
4. The step outcome and artifacts are preserved in the Runtime Store for recovery

**Key principle:** A durable outcome is not considered complete until the Git commit succeeds. The Workflow Engine must not advance to the next step until the commit is confirmed.

### 5.2 Git Conflict

If a durable outcome commit results in a Git conflict:

1. The conflict is classified as a permanent error — automatic conflict resolution is not attempted
2. The step execution is marked as failed with error classification `git_conflict`
3. Operator intervention is required to resolve the conflict
4. After resolution, the Run may be resumed or a new Run started

**Note (v0.x scope):**
Git conflicts are treated as permanent errors requiring operator intervention. Future versions may introduce assisted resolution (e.g., rebase-and-retry or workflow-driven merge steps), but these are explicitly out of scope for v0.x.

### 5.3 Partial Commit Failure

If a step produces multiple artifact changes that must be committed atomically:

- All changes are committed in a single Git commit
- If any change fails validation, the entire commit is rejected
- The step is marked as failed and follows normal retry/escalation

---

## 6. Workflow Engine Recovery

### 6.1 Crash Recovery

When the Workflow Engine restarts after a crash:

1. **Scan for active Runs** — query the Runtime Store for all Runs with status `active`
2. **Verify step execution state** — for each active Run, check the current step's execution status
3. **Resume or retry** — based on the step execution state:
   - `waiting` or `assigned` — re-issue the step assignment via Actor Gateway
   - `in_progress` — check if the actor has a result pending; if not, treat as timeout
   - `completed` — process the outcome and advance the Run
   - `failed` — follow normal retry/escalation logic
4. **Emit recovery events** — emit `engine_recovered` operational events for observability

### 6.2 Orphaned Run Detection

A Run is orphaned when no Workflow Engine instance is actively managing it. This can occur after:

- Workflow Engine crash with incomplete recovery
- Network partition between Workflow Engine and Runtime Store
- Runtime Store failover

**Detection:**

- The Workflow Engine periodically scans for Runs that have been `active` for longer than expected (based on step timeouts, workflow structure, and absence of recent execution activity or heartbeat signals)
- Runs without recent step execution activity are flagged as potentially orphaned
- Operators may also manually flag orphaned Runs
- The default orphan threshold is **30 days** — Spine workflows are human-paced and runs may be active for days or weeks
- Runs are only auto-failed after **3x the threshold** (90 days by default); single-threshold orphans are logged as warnings
- The threshold is configurable via `SPINE_ORPHAN_THRESHOLD` environment variable (e.g., `720h` for 30 days)

**Resolution:**

- An orphaned Run may be resumed (picked up by a Workflow Engine instance)
- An orphaned Run may be cancelled by an operator
- An orphaned Run must not be silently deleted — its state is preserved for diagnosis

### 6.3 Idempotency Requirements

All Workflow Engine operations must be idempotent to support crash recovery:

- Creating a step execution that already exists should be a no-op
- Committing a durable outcome that was already committed should be detected and skipped
- Emitting an event that was already emitted should not cause duplicate side effects
- Advancing a Run past a step that was already completed should be safe

The Runtime Store must support idempotent writes. The Artifact Service must detect already-committed artifacts (e.g., via content hash or commit SHA comparison).

---

## 7. Runtime State Loss

### 7.1 Full Runtime Store Loss

If the Runtime Store is entirely lost (per [Data Model](/architecture/data-model.md) §5.2):

**Completed Runs:**
- Durable outcomes exist in Git — the task artifacts reflect their terminal state
- Run records can be partially reconstructed from Git artifact history (which tasks were completed, what artifacts were produced)
- Full step execution history is lost but not authoritative — Git is the source of truth

**In-progress Runs:**
- Cannot be automatically resumed — the Workflow Engine does not know what step was active
- Operators must restart affected Runs or make governance decisions about the tasks
- Any step results not yet committed to Git are lost

**Queue entries:**
- Lost entries may result in missed step assignments or event deliveries
- The system supports replaying domain events from Git commit history to recover consumer state

### 7.2 Partial Runtime Store Loss

If specific Run or step execution records are corrupted:

- The Workflow Engine should detect inconsistent state during its normal execution loop
- Affected Runs should be flagged for operator review
- Automatic recovery should not be attempted for corrupted records — operator intervention is preferred

### 7.3 Recovery Priority

When recovering from state loss, the system prioritizes:

1. **Preserve durable outcomes** — anything already committed to Git is safe
2. **Identify affected Runs** — determine which Runs were in progress at the time of loss
3. **Notify operators** — emit events and surface affected Runs for manual triage
4. **Restart cleanly** — new Runs start from scratch rather than attempting to reconstruct mid-execution state

---

## 8. Error Propagation Rules

### 8.1 Step → Run

A step failure escalates to a Run failure when:
- All retry attempts are exhausted
- The error is classified as permanent
- The step has no `timeout_outcome` and times out after retries

A step failure does **not** escalate when:
- Retries are available and the error is transient
- The step declares a `timeout_outcome` that routes to another step
- The step is within a divergence branch (branch failure is handled by convergence, see §8.2)

### 8.2 Branch → Convergence

During divergence, branch failure follows the rules in [Divergence and Convergence](/architecture/divergence-and-convergence.md) §4.3:

- `select_one` / `select_subset` / `experiment` — convergence proceeds if at least one branch completed
- `merge` — convergence proceeds at the evaluator's discretion
- `require_all` — convergence fails, which escalates to Run failure

### 8.3 Run → Task

A Run failure does **not** automatically change the task's governed status. The task remains in its pre-Run state. Only explicit governance decisions (via `task.cancel`, `task.abandon`, etc.) modify the task artifact in Git.

### 8.4 Component → Component

Errors in supporting components (Projection Service, Event Router) do not fail Runs:

- **Projection Service failure** — queries return stale data; writes to Git succeed. The system operates in degraded mode.
- **Event Router failure** — events are not delivered. Domain events can be reconstructed from Git. Operational events are lost but non-authoritative.
- **Actor Gateway failure** — step assignments cannot be delivered. Steps time out and follow normal timeout handling.

### 8.5 Responsibility Boundaries

Error handling responsibilities are distributed as follows:

| Concern | Responsible Component |
|--------|----------------------|
| Error classification | Component detecting the error |
| Retry and escalation decisions | Workflow Engine |
| Validation of results | Workflow Engine / Validation Service |
| Side-effect safety | Step design and actor implementation |

The Workflow Engine orchestrates recovery, but correctness depends on proper step and actor design.

---

## 9. Operator Visibility

### 9.1 Error Information

All errors are recorded with:

- Error classification (transient/permanent)
- Error source (component, step, actor)
- Error detail (human-readable message, stack trace for automated steps)
- Timestamp
- Run and step context (run_id, step_id, attempt number)

This information is stored in the `error_detail` field on step execution records and emitted as operational events.

### 9.2 Alerting

The system should emit operational events for:

- Step failure (each attempt)
- Run failure
- Orphaned Run detection
- Runtime Store recovery initiated
- Git commit failure

These events are consumed by external observability systems. Spine does not define alerting rules — those are operator-configured.

---

## 10. Cross-References

- [Domain Model](/architecture/domain-model.md) — Run lifecycle (§5.2), Step lifecycle (§5.3)
- [Data Model](/architecture/data-model.md) — Runtime state reconciliation (§5.2), step_executions schema (§2.3)
- [Workflow Definition Format](/architecture/workflow-definition-format.md) — Retry and timeout configuration (§3.2)
- [Divergence and Convergence](/architecture/divergence-and-convergence.md) — Branch failure handling (§3.5), partial completion (§4.3)
- [System Components](/architecture/components.md) — Workflow Engine responsibilities (§4.3)
- [Task Lifecycle](/governance/task-lifecycle.md) — Run failure does not modify task status (§6, §7)
- [ADR-001](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md) — Durable outcomes vs operational details

---

## 11. Evolution Policy

This error handling model will evolve as the system is implemented and operational experience is gained.

Areas expected to require refinement:

- Concrete backoff timing parameters (base delay, max delay, jitter)
- Dead letter handling for repeatedly failing step assignments
- Circuit breaker patterns for actor and external service failures
- Automated Run restart policies (vs requiring operator intervention)
- Error budget and reliability SLO definitions

Changes that alter error propagation rules or recovery guarantees should be captured as ADRs.
