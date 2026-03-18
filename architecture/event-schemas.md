---
type: Architecture
title: Event Schema Specification
status: Living Document
version: "0.1"
---

# Event Schema Specification

---

## 1. Purpose

This document defines the concrete schemas for all event types in the Spine system.

[ADR-002](/architecture/adr/ADR-002-events.md) establishes the event model: domain events are derived signals reconstructible from Git, operational events are ephemeral runtime signals. The [Data Model](/architecture/data-model.md) (§2.4) defines the event envelope. The [Domain Model](/architecture/domain-model.md) (§3.7) defines the Event entity.

This document makes those abstractions concrete — specifying the payload structure for each event type so that producers and consumers can interoperate reliably.

---

## 2. Event Envelope

All events share a common envelope structure (per [Data Model](/architecture/data-model.md) §2.4):

```yaml
event_id: <string>              # Unique event identifier (UUID)
event_type: <string>            # Event type identifier (e.g., "artifact_created")
schema_version: <string>        # Schema version for this event type (e.g., "1.0")
timestamp: <string>             # ISO 8601 datetime (e.g., "2026-03-18T14:30:00Z")
source_component: <string>      # Component that produced the event
actor_id: <string|null>         # Actor that caused the event (null for system-generated)
run_id: <string|null>           # Associated Run (null if not execution-related)
artifact_path: <string|null>    # Associated artifact path (null if not artifact-related)
source_commit: <string|null>    # Git commit SHA that triggered the event (null for operational events)
payload: <object>               # Event-type-specific data (defined per schema below)
```

The `schema_version` field is added to support event versioning (see §5).

### Canonical Artifact Reference

`artifact_path` is the authoritative reference to an artifact within Spine. It is globally unique and stable across the system.

Fields such as `artifact_id` included in payloads are convenience identifiers only and must not be treated as globally unique or authoritative by consumers.

Consumers should always use `artifact_path` when correlating events with artifacts.

---

## 3. Domain Event Schemas

Domain events represent meaningful system state transitions. They are derived from Git artifact changes and must be reconstructible from Git commit history.

**Delivery guarantee:** at-least-once.

### Status and Lifecycle Consistency

Any status values or lifecycle transitions included in event payloads must align with the canonical lifecycle definitions defined in the Domain Model and Artifact Schema.

Event payloads must not introduce new status values. They are a reflection of artifact state, not a source of lifecycle truth.

### 3.1 `artifact_created`

Emitted when a new artifact is committed to Git.

**Source:** Artifact Service

```yaml
payload:
  artifact_id: <string>         # e.g., "TASK-001"
  artifact_type: <string>       # e.g., "Task", "Epic", "ADR"
  title: <string>               # Artifact title
  status: <string>              # Initial status
  parent_path: <string|null>    # Parent artifact path (e.g., epic path for tasks)
  created_by: <string>          # Actor who created the artifact
```

**Reconstruction:** diff the source commit to find new artifact files; parse front matter for payload fields.

### 3.2 `artifact_updated`

Emitted when an existing artifact's metadata or content changes in Git.

**Source:** Artifact Service

```yaml
payload:
  artifact_id: <string>
  artifact_type: <string>
  changed_fields: [<string>]    # Front matter fields that changed (e.g., ["status", "acceptance"])
  previous_status: <string|null> # Previous status value (null if status unchanged)
  new_status: <string|null>      # New status value (null if status unchanged)
  change_summary: <string>       # Human-readable description of the change
```

**Reconstruction:** diff the source commit to find modified artifact files; compare front matter before and after.

**Note on relationships:** If artifact relationships (links) change, they must be included in `changed_fields`. Consumers should treat relationship updates the same as other metadata changes.

### 3.3 `artifact_superseded`

Emitted when an artifact's status changes to `Superseded`.

**Source:** Artifact Service

```yaml
payload:
  artifact_id: <string>
  artifact_type: <string>
  successor_path: <string|null>  # Path to the superseding artifact (if linked)
  rationale: <string|null>       # Reason for supersession (from commit message or front matter)
```

**Reconstruction:** detect status change to `Superseded` in commit diff; extract successor link from front matter.

### 3.4 `run_started`

Emitted when a new Run is created for a task.

**Source:** Workflow Engine

```yaml
payload:
  task_path: <string>            # Path to the governed task artifact
  workflow_id: <string>          # Workflow definition identifier
  workflow_version: <string>     # Git SHA of the pinned workflow definition
  workflow_version_label: <string> # Semantic version label
  entry_step: <string>           # First step to execute
```

### 3.5 `run_completed`

Emitted when a Run reaches a successful terminal state.

**Source:** Workflow Engine

```yaml
payload:
  task_path: <string>
  workflow_id: <string>
  final_step: <string>           # Last step that executed
  final_outcome: <string>        # Outcome of the final step
  duration_ms: <integer>         # Total Run duration in milliseconds
  steps_executed: <integer>      # Number of step executions (including retries)
  artifacts_produced: [<string>] # Paths of artifacts produced during the Run
```

### 3.6 `run_failed`

Emitted when a Run fails (per [Error Handling](/architecture/error-handling-and-recovery.md) §4.2).

**Source:** Workflow Engine

```yaml
payload:
  task_path: <string>
  workflow_id: <string>
  failed_step: <string>          # Step where failure occurred
  failure_reason: <string>       # Error classification (transient_exhausted, permanent, convergence_failed)
  error_detail: <string|null>    # Human-readable error description
  attempts: <integer>            # Number of retry attempts on the failed step
  duration_ms: <integer>         # Total Run duration before failure
```

### 3.7 `run_cancelled`

Emitted when a Run is explicitly cancelled (per [Error Handling](/architecture/error-handling-and-recovery.md) §4.4).

**Source:** Workflow Engine

```yaml
payload:
  task_path: <string>
  workflow_id: <string>
  cancelled_by: <string>         # Actor who initiated cancellation
  cancellation_reason: <string|null> # Optional rationale
  current_step: <string|null>    # Step that was active when cancellation occurred
```

### 3.8 `workflow_definition_changed`

Emitted when a workflow definition file is created, modified, or its status changes.

**Source:** Artifact Service

```yaml
payload:
  workflow_id: <string>
  workflow_path: <string>        # Repository path to the workflow file
  previous_version: <string|null> # Previous semantic version (null if new)
  new_version: <string>          # New semantic version
  previous_status: <string|null> # Previous lifecycle status
  new_status: <string>           # New lifecycle status (Active, Deprecated, Superseded)
  applies_to: [<string>]         # Artifact types this workflow governs
```

**Reconstruction:** diff the source commit to find modified workflow files; compare YAML content before and after.

---

## 4. Operational Event Schemas

Operational events describe runtime execution behavior. They support observability and debugging but are not durable system state.

**Delivery guarantee:** best-effort.

### 4.1 `step_started`

Emitted when a step execution begins.

**Source:** Workflow Engine

```yaml
payload:
  step_id: <string>             # Step identifier within the workflow
  step_name: <string>           # Human-readable step name
  step_type: <string>           # manual, automated, review, convergence
  attempt: <integer>            # Execution attempt number (1 for first attempt)
  actor_id: <string|null>       # Assigned actor (null if not yet assigned)
  branch_id: <string|null>      # Divergence branch ID (null if not in divergence)
```

### 4.2 `step_completed`

Emitted when a step execution completes successfully.

**Source:** Workflow Engine

```yaml
payload:
  step_id: <string>
  step_name: <string>
  outcome: <string>              # Workflow-defined outcome value
  next_step: <string>            # Next step to execute (or "end")
  attempt: <integer>
  duration_ms: <integer>         # Step execution duration
  has_commit: <boolean>          # Whether this outcome produced a Git commit
  branch_id: <string|null>
```

### 4.3 `step_failed`

Emitted when a step execution fails.

**Source:** Workflow Engine

```yaml
payload:
  step_id: <string>
  step_name: <string>
  attempt: <integer>
  error_classification: <string> # transient, permanent
  error_detail: <string>         # Human-readable error description
  will_retry: <boolean>          # Whether the step will be retried
  remaining_retries: <integer>   # Retries remaining (0 if exhausted)
  branch_id: <string|null>
```

### 4.4 `step_assigned`

Emitted when an actor is assigned to a step.

**Source:** Workflow Engine / Actor Gateway

```yaml
payload:
  step_id: <string>
  step_name: <string>
  actor_id: <string>             # Assigned actor
  actor_type: <string>           # human, ai_agent, automated_system
  attempt: <integer>
  branch_id: <string|null>
```

### 4.5 `step_timeout`

Emitted when a step exceeds its declared timeout.

**Source:** Workflow Engine

```yaml
payload:
  step_id: <string>
  step_name: <string>
  timeout_duration: <string>     # Configured timeout value (e.g., "7d")
  timeout_outcome: <string|null> # Outcome applied (null if treated as failure)
  attempt: <integer>
  branch_id: <string|null>
```

### 4.6 `retry_attempted`

Emitted when a step retry is scheduled.

**Source:** Workflow Engine

```yaml
payload:
  step_id: <string>
  step_name: <string>
  attempt: <integer>             # New attempt number
  backoff_strategy: <string>     # fixed, linear, exponential
  delay_ms: <integer>            # Delay before retry in milliseconds
  previous_error: <string>       # Error that triggered the retry
  branch_id: <string|null>
```

### 4.7 `engine_recovered`

Emitted when the Workflow Engine restarts after a crash and resumes operations (per [Error Handling](/architecture/error-handling-and-recovery.md) §6.1).

**Source:** Workflow Engine

```yaml
payload:
  active_runs_found: <integer>   # Number of active Runs detected
  runs_resumed: <integer>        # Number of Runs successfully resumed
  runs_flagged: <integer>        # Number of Runs flagged as potentially orphaned
  downtime_ms: <integer|null>    # Estimated downtime (null if unknown)
```

### 4.8 `divergence_started`

Emitted when a divergence point is triggered within a Run.

**Source:** Workflow Engine

```yaml
payload:
  divergence_id: <string>        # Divergence point identifier
  divergence_mode: <string>      # structured, exploratory
  branch_count: <integer>        # Number of branches created
  branch_ids: [<string>]         # List of branch identifiers
```

### 4.9 `convergence_completed`

Emitted when a convergence point resolves.

**Source:** Workflow Engine

```yaml
payload:
  convergence_id: <string>       # Convergence point identifier
  strategy: <string>             # select_one, select_subset, merge, require_all, experiment
  entry_policy_applied: <string> # Entry policy that triggered convergence
  branches_evaluated: <integer>  # Number of branches evaluated
  branches_completed: <integer>  # Number of branches that completed successfully
  branches_failed: <integer>     # Number of branches that failed
  selected_branch: <string|null> # Selected branch (for select_one)
  selected_branches: [<string>]  # Selected branches (for select_subset/experiment; empty otherwise)
```

---

## 5. Event Versioning

### 5.1 Schema Version Field

Every event includes a `schema_version` field in the envelope. Versions follow a major.minor scheme:

- **Major version change** — breaking payload change (field removed, type changed, required field added)
- **Minor version change** — backward-compatible addition (new optional field)

### 5.2 Versioning Rules

- Producers must include `schema_version` on every event
- Consumers must tolerate unknown fields (forward compatibility)
- Consumers should check `schema_version` and handle unknown major versions gracefully (log and skip, or route to dead letter)
- The Event Router does not validate payload schemas — it routes events by `event_type`. Schema validation is the consumer's responsibility.

### 5.3 Schema Evolution Strategy

When a schema changes:

1. **Minor change** — add the new field with a default or nullable value. Increment minor version. No consumer changes required.
2. **Major change** — introduce a new event type (e.g., `artifact_updated_v2`) or increment the major version. Producers emit both old and new versions during a transition period. Consumers migrate at their own pace.

Old schema versions are never removed from this document — they are marked as deprecated with a pointer to the successor.

### Event Ordering and Causality

Spine does not guarantee global ordering of events.

The following assumptions apply:
- Ordering is only reliable within the context of a single artifact (via Git commit history)
- Ordering is not guaranteed across artifacts
- Operational events may be out of order or delayed

Consumers must not rely on strict ordering across event streams and should use timestamps and artifact context for reconciliation where necessary.

---

## 6. Reconstruction Path

Domain events must be reconstructible from Git commit history (per [Data Model](/architecture/data-model.md) §5.3):

```
Git commit log
  → For each commit, diff against parent
  → For each changed artifact file:
    → Parse front matter before and after
    → Determine event type:
      - New file → artifact_created
      - Modified file → artifact_updated
      - Status changed to Superseded → artifact_superseded
      - Workflow file changed → workflow_definition_changed
  → For each event:
    → Populate envelope from commit metadata (SHA, timestamp, author)
    → Populate payload from front matter diff
    → Emit reconstructed event
```

**Run lifecycle events** (`run_started`, `run_completed`, `run_failed`, `run_cancelled`) are only partially reconstructible from Git.

- Runs that produce durable outcomes (e.g., a task status change to `Completed`) may imply that a corresponding lifecycle event occurred
- Runs that fail or are cancelled without producing Git commits are not reconstructible from Git history
- Operational aspects of execution (step-level activity, retries, timing) are never reconstructible

These events should be treated as runtime signals with limited durability rather than a complete historical source of truth.

---

## 7. Delivery Guarantees

| Category | Guarantee | Rationale |
|----------|-----------|-----------|
| Domain events | At-least-once | Consumers may depend on these for projections and integrations. Missed events cause stale state. Reconstruction from Git provides a fallback. |
| Operational events | Best-effort | These support observability only. Loss is acceptable. Reconstruction is not guaranteed. |

**At-least-once implications:**

- Consumers must be idempotent — receiving the same event twice must not cause incorrect state
- The Event Router should support deduplication where feasible (e.g., by `event_id`)
- Domain events should include enough context for consumers to detect duplicates (e.g., `source_commit` + `artifact_path`)
  - For domain events derived from Git, a recommended idempotency key is the combination of `source_commit` and `artifact_path`

---

## 8. Event Producers and Consumers

### 8.1 Producers

| Component | Events Produced |
|-----------|----------------|
| Artifact Service | `artifact_created`, `artifact_updated`, `artifact_superseded`, `workflow_definition_changed` |
| Workflow Engine | `run_started`, `run_completed`, `run_failed`, `run_cancelled`, `step_started`, `step_completed`, `step_failed`, `step_assigned`, `step_timeout`, `retry_attempted`, `engine_recovered`, `divergence_started`, `convergence_completed` |

### 8.2 Consumers

| Consumer | Events Consumed | Purpose |
|----------|----------------|---------|
| Projection Service | `artifact_created`, `artifact_updated`, `artifact_superseded` | Update projection store |
| External integrations | All domain events | Webhooks, notifications |
| Observability systems | All events | Monitoring, alerting, dashboards |
| Workflow Engine | `artifact_updated` | Detect external artifact changes that may affect running Runs |

### Events vs Source of Truth

Events in Spine are signals, not the source of truth.

- Artifact state is defined exclusively by Git (artifact files and their history)
- Events reflect and notify about those changes
- Consumers must not treat events as authoritative state

In particular, decisions such as approval, rejection, or completion are only valid when reflected in artifact state, not when observed as events alone.

---

## 9. Cross-References

- [ADR-002](/architecture/adr/ADR-002-events.md) — Event model decisions (domain vs operational, derived signals)
- [Domain Model](/architecture/domain-model.md) §3.7 — Event entity definition
- [Data Model](/architecture/data-model.md) §2.4 — Event envelope schema, §5.3 — Event reconciliation
- [System Components](/architecture/components.md) §4.7 — Event Router
- [Error Handling](/architecture/error-handling-and-recovery.md) — Failure events (§4.2, §4.4, §6.1, §9.2)
- [Divergence and Convergence](/architecture/divergence-and-convergence.md) — Branch and convergence events

---

## 10. Evolution Policy

This specification will evolve as event types are implemented and operational experience is gained.

Areas expected to require refinement:

- Additional domain events for new artifact types or governance actions
- Structured error codes within failure event payloads
- Event filtering and subscription patterns for the Event Router
- Batch event emission for commits that change multiple artifacts

New event types should be added to this document with schemas before producers begin emitting them. Changes to existing schemas must follow the versioning rules in §5.
