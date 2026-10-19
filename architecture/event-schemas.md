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

**Event ID:** `evt-<trace_id[:12]>-<execution_id>-completed` — one per step
*execution* (INIT-023 TASK-008), so every rework attempt is recorded (the per-step
ID used before made the event log's `ON CONFLICT (event_id) DO NOTHING` drop
attempt 2 onwards). A crash-recovery re-emission of the same execution
(`Orchestrator.RecoverCompletedStep`) carries the same ID, so the log keeps the
original. The envelope's `actor_id` is the execution's actor.

```yaml
payload:
  step_id: <string>
  step_name: <string>
  outcome: <string>              # Workflow-defined outcome value
  next_step: <string>            # Next step to execute (or "end")
  attempt: <integer>
  duration_ms: <integer>         # Step execution duration (absent when the step never started)
  has_commit: <boolean>          # Whether this outcome produced a Git commit
  branch_id: <string|null>
  execution_id: <string>         # INIT-023 TASK-008
  actor_id: <string>             # the execution's actor (absent when none)
  # Step-output evidence (INIT-023 TASK-008, audit P3-19). Absent on the
  # engine-owned publish step (type: internal, handler: merge), which carries
  # the fields above only.
  required_outputs: [<string>]   # the step's declared required_outputs (absent when none)
  artifacts_produced: [<string>] # the submitter's attested list: at most 32 entries,
                                 # each cut to at most 512 bytes (client-supplied)
  artifacts_produced_count: <integer>  # exact length of the attested list
  branch_change:
    repository_id: spine         # primary repository only (v1)
    status: unchanged | changed | unknown | not_applicable
    head_sha: <string>           # the run-branch head recorded for this submit (TASK-005's capture)
    previous_sha: <string>       # the run's previous trusted binding, else the branch baseline
    previous_source: <string>    # that binding's spine.bound_by verbatim
                                 # ("step:<step_id>:<execution_id>", "convergence:…",
                                 # "merge_recovery:…") or "baseline"
    changed_paths: <integer>     # 0 when unchanged, the diff's entry count when changed;
                                 # absent otherwise
    reason: <string>             # unknown / not_applicable: head_sha_unrecorded,
                                 # no_previous_sha, diff_failed:<kind>, divergence_branch,
                                 # no_run_branch, recovered; unchanged:
                                 # head_moved_tree_unchanged when the head moved but the
                                 # diff found no changed path (absent when head == previous)
  required_outputs_unchanged: <boolean>  # the step declared required_outputs and the
                                         # branch content is provably unchanged
```

`branch_change` is evidence, not enforcement: `required_outputs` are logical
labels the submitter attests (KNOWN-LIMITATIONS "Step Outputs Are Self-Attested
Labels"). `unchanged` needs no git call when the recorded head equals the
previous one; otherwise the engine diffs the two under a 3 s timeout. A diff with
entries is `changed`; a diff with none is `unchanged` too, with `reason:
head_moved_tree_unchanged` — the commits in between (an empty commit, a change and
its revert) left the tree as it was, so the step is flagged: the flag is judged on
the content, not the SHA. A failure or a timeout gives `unknown` with
`diff_failed:<kind>` (`kind` is a git error kind, `timeout`, `canceled` or
`error`; the git message is logged, never put in the event). With merge binding
off and no `expected_head_sha` nothing is captured: `status: unknown`, `reason:
head_sha_unrecorded`, no git call. A crash-recovery re-emission carries
`unknown`/`recovered`. When the step declared outputs and the branch is unchanged,
the engine also logs `WARN required outputs declared but run branch unchanged`,
once the submission's terminal write has applied (never for a submission that was
refused or lost its race). The same `branch_change` and
`required_outputs_unchanged` are persisted for run detail (`GET /runs/{run_id}` →
`step_output_evidence`) as the reserved `runs.commit_meta` key
`spine.evidence.<execution_id>`, with merge binding on only.

Upgrade note: an execution completed by a binary before INIT-023 TASK-008 was
logged under the per-step id `evt-<trace>-<step_id>-completed`. If a crash
interrupted its routing and the process restarts on a newer binary, recovery
re-emits it under the per-execution id, so that execution's `step_completed` is
logged and delivered twice (once per id). This is a one-off, standalone only.

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

### 4.9 `step_assignment_failed`

Emitted when no eligible actor can be found for a step (per [Actor Model](/architecture/actor-model.md) §4.4).

**Source:** Workflow Engine

```yaml
payload:
  step_id: <string>             # Step that could not be assigned
  step_name: <string>
  reason: <string>              # Why assignment failed (no_eligible_actors, actor_unreachable, skill_mismatch)
  required_skills: [<string>]      # Skills the step requires
  eligible_actor_types: [<string>]  # Actor types the step accepts
  attempt: <integer>            # Assignment attempt number
  branch_id: <string|null>
```

### 4.10 `task_unblocked`

Emitted when a task transitions from blocked to ready because all `blocked_by` targets have reached terminal status.

**Source:** Workflow Engine

```yaml
payload:
  task_path: <string>           # Task that became unblocked
  resolved_by: <string>         # The blocker task that just completed, triggering the transition
```

### 4.11 `task_released`

Emitted when an actor releases a step assignment back to the pool.

**Source:** Workflow Engine

```yaml
payload:
  assignment_id: <string>       # The released assignment
  actor_id: <string>            # Actor who released
  execution_id: <string>        # Step execution returned to waiting
  reason: <string>              # Why the actor released
```

### 4.12 `convergence_completed`

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

### 4.11 `workflow_validation_completed`

Emitted when workflow validation completes (per [Workflow Validation](/architecture/workflow-validation.md) §7.3).

**Source:** Workflow Engine

```yaml
payload:
  workflow_id: <string>          # Workflow that was validated
  workflow_version: <string>     # Version that was validated
  trigger: <string>              # What triggered validation (commit, activation, on_demand)
  status: <string>               # passed, failed, warnings
  error_count: <integer>         # Number of blocking errors
  warning_count: <integer>       # Number of non-blocking warnings
  errors: [<string>]             # Summary of error messages (truncated if excessive)
```

### 4.13 `projection_synced`

Emitted by `IncrementalSync` after a sync that did work (not on the up-to-date early return).

**Source:** Projection Service

```yaml
payload:
  from_commit: <string>          # Short SHA of the previous last_synced_commit
  to_commit: <string>            # Short SHA of the sync head
  created: <integer>             # Artifacts created
  modified: <integer>            # Artifacts modified
  deleted: <integer>             # Artifacts deleted
  errors: <integer>              # Counted sync errors (state stays at from_commit when > 0)
  workflows_rejected: <integer>  # Workflow files refused for their content (never counted in errors)
  read_skipped: <integer>        # Changed artifact files git could not read because the commit's tree holds no file at the path (a gitlink to an object not in the repository, a directory); skipped, logged at Warn
  refused: <integer>             # Changed files not projected because they are oversized or at an unsafe path (one projection_file_skipped each; never counted in errors)
```

### 4.14 `projection_workflow_rejected`

Emitted when the projection refuses a workflow file because of its content (INIT-023 TASK-011). A rejection is never a sync error: the sync advances and this event plus one Warn log line (`projection: workflow file rejected`) are the durable signals. `artifact_path` on the envelope is the workflow path. The event id is deterministic (`wfrej-` + 16 hex characters over path, commit and action), so a repeated report de-duplicates.

**Source:** Projection Service (`FullRebuild`, `IncrementalSync`, and the once-per-instance workflow-row sweep)

```yaml
payload:
  workflow_path: <string>        # Repository-relative workflow path
  commit: <string>               # Commit the file was read at (the sync head)
  check: <string>                # parse | round_trip | validate | read_bounds | not_a_file | scope | absent_at_head | unsafe_path
  action: <string>               # kept | not_projected | removed
  kept_source_commit: <string>   # source_commit of the kept row ("" unless action is kept)
  quarantined: <boolean>         # true when the kept row's status column is Quarantined
  error: <string>                # One line, control characters replaced, at most 512 bytes
```

- `parse`: strict `workflow.Parse` refused the file (unknown, duplicate or case-variant keys, `path` / `commit_sha`, bounds).
- `round_trip`: `Parse` accepted the file but its stored JSON would decode differently (for example an unquoted `version: 2026-10-06`), or the row could not be stored: U+0000 anywhere, or a workflow path, `id` or `status` over 1024 bytes (an indexed column).
- `read_bounds`: the blob is refused by a content-size bound on read.
- `not_a_file`: the commit's tree holds no file (blob) at the path — a gitlink or a directory — so there is no content to project. The entry is typed before the file is read, so a gitlink is refused even when the id it records names a blob in the repository (which `git show` would print, though diffs show only a submodule pointer).
- `validate`: `workflow.Validate` failed and strict projection validation is enabled (off by default).
- `scope` / `absent_at_head`: the sweep found a stored row outside the workflow definition locations, or whose file no longer exists at head and did not exist at the last synced commit either (a file deleted after the last synced commit is removed like the pending diff would remove it, without a rejection).
- `unsafe_path`: a stored row at a path INIT-023 TASK-003 refuses to project (a control, bidi or line-separator character in the file name). Such a row can only come from a projection built before TASK-003 by a git configured with `core.quotePath=false`, which listed those names raw. It is reported by the sweep when the file is still at head, and by an incremental sync that adds or modifies the file; the file itself is also reported as `projection_file_skipped` (`unsafe_path`) by the sync. A row whose file is deleted or renamed away is deleted without a rejection, as for any other path (INIT-023 wave 3 integration).
- `kept`: the previous row verified and is kept for in-flight runs, with its status set to `Quarantined` so no new run binds it. `removed`: the previous row could not be verified and was deleted. `not_projected`: there was no row.

`read_bounds` is where an oversized workflow file lands (INIT-023 TASK-003): a blob over the git client's per-blob read limit (`git.ErrBlobTooLarge`) is a rejection that advances the sync, not a sync error. Such a file is not also reported as `projection_file_skipped`.

### 4.15 `projection_file_skipped`

Emitted once per file the projection declined to project (INIT-023 TASK-003): a file over the git client's per-blob read limit (default 4 MiB, `git.WithMaxBlobBytes`), or a candidate at a path Spine refuses to store. A skip is never a sync error: `FullRebuild` and `IncrementalSync` advance past it. Each one is also logged at Warn (`projection skipped file`) and counted in `spine_projection_files_skipped_total`. It is reported as soon as discovery has decided it, before any later step can hold the sync: a sync held in `error` for another reason (an unreadable changed file, a failed store write or wipe) still reports it, and every retry reports it again under the same event id (the delivery queue de-duplicates; the counter counts each report). `artifact_path` on the envelope is the reported `path` (repository-relative, never stripped of `artifacts_dir`, since a skipped file has no projection key).

**Source:** Projection Service (`FullRebuild` and `IncrementalSync`)

**When it fires:**

- `oversized`: an artifact candidate (`*.md`) whose blob is over the read limit, or that git cannot produce within its allocation limit (`git_alloc_limit_bytes` set: a small file stored as a delta on a large base, or a gitlink rendered with its patch) — added, modified, renamed to, or present at the rebuild head. Its content is never read, so it is reported whether or not it is a governed artifact. A projected artifact modified past the limit also loses its row (as for a parse failure). An oversized *workflow* file is a `projection_workflow_rejected` with check `read_bounds` instead (§4.14); an oversized `.spine/branch-protection.yaml` is a held sync error with the rules unchanged.
- `unsafe_path`: an artifact or workflow-definition candidate whose path is not valid UTF-8 or holds a control character (C0, DEL, C1), U+2028/U+2029, or a bidi embedding, override or isolate control (U+202A–U+202E, U+2066–U+2069). The file is never read, and no store call carries its path. A deleted or renamed-away unsafe path is ignored (no such row can exist).

```yaml
payload:
  path: <string>          # Repository-relative path; for unsafe_path, Go-quoted (strconv.Quote: one line, valid UTF-8, every control and bidi character escaped)
  reason: <string>        # oversized | unsafe_path
  size_bytes: <integer>   # oversized: the blob's size, or -1 when the read overran the limit (git refused an allocation — see git_alloc_limit_bytes — or `git show` rendered more than the probed object); null for unsafe_path
  limit_bytes: <integer>  # oversized: the limit it exceeded (-1 if the git client did not report it); null for unsafe_path
  git_alloc_limit_bytes: <integer>  # oversized, when git itself refused an allocation over this GIT_ALLOC_LIMIT while producing the content (size_bytes is then -1): the file may be under limit_bytes, and what is too large is a delta base it is stored on or a gitlink's patch; null otherwise
  commit: <string>        # The sync head the file was found at (full SHA)
```

**Idempotency:** the event id is deterministic — `projskip-` + the first 12 characters of `commit` + `-` + the first 12 hex characters of SHA-256 over `reason`, a NUL byte and the raw path — so a rebuild or a retried sync at the same commit re-emits the same id, which the delivery queue de-duplicates. Existing subscriptions that receive every event type receive this one too.

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
| Workflow Engine | `run_started`, `run_completed`, `run_failed`, `run_cancelled`, `step_started`, `step_completed`, `step_failed`, `step_assigned`, `step_assignment_failed`, `step_timeout`, `retry_attempted`, `engine_recovered`, `divergence_started`, `convergence_completed`, `task_unblocked`, `workflow_validation_completed` |

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

---

## Appendix: Implementation Emission Map

All event types are emitted from the following code paths:

### Domain Events

| Event Type | Emitted By | File |
|-----------|-----------|------|
| `run_started` | `Orchestrator.StartRun` | engine/run.go |
| `run_completed` | `Orchestrator.CompleteRun` | engine/run.go |
| `run_failed` | `Orchestrator.FailRun` | engine/run.go |
| `run_cancelled` | `Orchestrator.CancelRun` | engine/run.go |
| `run_paused` | `Orchestrator.PauseRun` | engine/run.go |
| `run_resumed` | `Orchestrator.ResumeRun` | engine/run.go |
| `step_assigned` | `Orchestrator.ActivateStep` | engine/step.go |
| `step_started` | `Orchestrator.SubmitStepResult` (auto-acknowledge) | engine/step.go |
| `step_completed` | `Orchestrator.SubmitStepResult` (also `RecoverCompletedStep`, and `advancePublishStepIfAny` for the publish step) | engine/step.go, engine/handlers.go |
| `step_failed` | `Orchestrator.RetryStep` (non-retryable only) | engine/retry.go |
| `step_timeout` | `Scheduler.handleStepTimeout` | scheduler/timeout.go |
| `retry_attempted` | `Orchestrator.RetryStep` | engine/retry.go |
| `run_timeout` | `Scheduler.handleRunTimeout` | scheduler/run_timeout.go |

### Operational Events

| Event Type | Emitted By | File |
|-----------|-----------|------|
| `divergence_started` | `divergence.Service.StartDivergence` | divergence/service.go |
| `convergence_completed` | `divergence.Service.CommitConvergence` | divergence/convergence.go |
| `engine_recovered` | `Scheduler.RecoverOnStartup` | scheduler/recovery.go |
| `projection_synced` | `projection.Service.IncrementalSync` | projection/service.go |
| `projection_workflow_rejected` | `projection.Service` | projection/service.go |
| `projection_file_skipped` | `projection.Service.FullRebuild`, `IncrementalSync` | projection/refused.go |
| `validation_passed` | `gateway.handleSystemValidate` | gateway/handlers_system.go |
| `validation_failed` | `gateway.handleSystemValidate` | gateway/handlers_system.go |
| `step_assignment_failed` | `Orchestrator.ActivateStep` | engine/step.go |
