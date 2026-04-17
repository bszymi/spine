---
type: Architecture
title: API Design and Operation Semantics
status: Living Document
version: "0.1"
---

# API Design and Operation Semantics

---

## 1. Purpose

This document defines the API design philosophy, operation semantics, and domain-level rules for Spine's external interface.

The [Access Surface](/architecture/access-surface.md) defines operation categories and the internal operation model. The [OpenAPI specification](/api/spec.yaml) defines the concrete HTTP endpoints, request/response schemas, and error payloads. This document bridges the gap — it explains what the operations mean, when to use them, and what governance rules apply.

---

## 2. API Philosophy

### 2.1 Operations, Not Resources

The Spine API exposes **governed domain operations**, not a generic REST resource model. While HTTP is used as transport and read-heavy endpoints may resemble REST resources, state-changing actions are modeled as explicit operations with governance semantics.

This means:
- `artifact.create` is not `POST /artifacts` — it's a governed action that validates schemas, checks cross-artifact rules, and produces a Git commit
- `task.accept` is not `PATCH /tasks/:id` — it's a governance decision that requires reviewer authorization and records rationale
- Operations may have side effects beyond the obvious (emit events, trigger projection sync, advance workflow state)

### 2.2 Unified Operation Model

All access modes (CLI, API, GUI) converge on the same operations. The internal operation model (per [Access Surface](/architecture/access-surface.md) §5.3) is:

```
InternalRequest
├── operation     (string, e.g., "artifact.create", "run.start")
├── actor_id      (string, authenticated actor)
├── actor_role    (string, authorization role)
├── params        (map, operation-specific parameters)
└── trace_id      (string, observability correlation)
```

CLI commands, HTTP requests, and GUI actions all translate into this model. The API specification defines the HTTP mapping; this document defines the semantics that apply regardless of access mode.

### 2.3 Authoritative vs Proposed Writes

Not all writes are equal:

| Write Type | Where It Lands | Governance Status |
|------------|---------------|-------------------|
| Governed write | Authoritative branch (via merge) | Durable, governed truth |
| Operational write | Task/divergence branch | Proposed, revisable |
| Runtime write | Runtime Store only | Ephemeral execution state |

Callers should understand which category their operation falls into:

- `artifact.create` and `artifact.update` produce governed writes (direct to authoritative branch) when called outside a Run
- When called within a Run context (by providing `run_id`), artifact writes target the task branch instead — these are proposed writes that become governed only after workflow completion and merge
- `step.submit` may produce an operational write (to the task branch) that becomes governed only after workflow completion and merge
- `run.start`, `run.cancel`, `step.assign` produce runtime writes only

Write context is expressed explicitly in artifact write requests via `write_context` (see the OpenAPI specification for details). When `write_context` is omitted, the write targets the authoritative branch directly.

**Planning run writes:**

Planning runs (per [ADR-006](/architecture/adr/ADR-006-planning-runs.md)) produce proposed writes that include artifact creation on a branch. The `write_context` for planning runs accepts `run_id` without `task_path` validation, since the run owns a constrained creation scope on the branch for multi-artifact writes. Planning run writes are restricted to creating new artifacts only — they may not update, delete, or mutate pre-existing artifacts on the authoritative branch.

---

## 3. Operation Categories

### 3.1 Artifact Operations

Artifact operations create, read, and modify governed artifacts in Git.

Workflow definitions are **not** governed by this category. Per [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md), they are a separate resource with dedicated operations (see §3.2). Requests to `artifact.create`, `artifact.update`, or `artifact.add` that target a workflow path (prefix `/workflows/`) or declared type are rejected with `400 invalid_params` and an error payload pointing to the corresponding `workflow.*` operation. `artifact.read` against a workflow path returns the same summary projection as `query.artifacts` — not the executable body.

| Operation | Effect | When to Use |
|-----------|--------|-------------|
| `artifact.create` | Creates a new artifact file and commits to Git | When creating a new artifact with known path and content (low-level). Does not accept workflow definitions. |
| `artifact.entry` | Allocates next ID, builds content, starts planning run | When creating a new artifact through governed workflow (high-level) |
| `artifact.add` | Adds an artifact to an existing planning run's branch | When incrementally adding child artifacts during a planning run's draft step. Does not accept workflow definitions. |
| `artifact.read` | Reads artifact content from Git (or projection) | When viewing artifact details; supports reading from non-default refs. For workflow paths, returns summary metadata only. |
| `artifact.update` | Updates artifact content and commits to Git | When modifying artifact metadata or content outside a Run. Does not accept workflow definitions. |
| `artifact.validate` | Validates without persisting | When checking an artifact before creation/update, or validating drafts |
| `artifact.list` | Queries projected artifacts | When browsing or filtering the artifact inventory |
| `artifact.links` | Queries artifact relationships | When exploring dependency graphs or parent/child hierarchies |

**Domain rules:**
- `artifact.create` rejects duplicates (same path or same ID within scope)
- `artifact.entry` allocates the next sequential ID within the parent scope (e.g., TASK-006 if TASK-001 through TASK-005 exist), generates a slug from the title, builds schema-valid front-matter content, and delegates to `run.start_planning`. The parent must exist and be the correct type (tasks require epic parent, epics require initiative parent). Supported types: Task, Epic, Initiative. ADR and document types use separate creation workflows.
- `artifact.add` writes to a planning run's branch, scanning the branch (not main) for the next ID. Only works while the run is in the `draft` step. The parent can exist on the branch or on main.
- `artifact.update` validates the full artifact (schema + cross-artifact) before committing
- Write operations target the authoritative branch by default; when a `write_context` with `run_id` is provided, they target the task branch instead
- Merge-time collision detection: if two planning runs allocate the same ID, the collision is detected at merge time. The artifact is renumbered and the merge retried (max 2 attempts).
- Write operations are designed to produce a single atomic Git commit with structured trailers (per [Git Integration](/architecture/git-integration.md) §5). This is a target architectural invariant — implementations must treat partial commits as bugs
- `artifact.create`, `artifact.update`, and `artifact.add` reject workflow targets with `400 invalid_params`; the error payload identifies the corresponding `workflow.*` operation to use instead

### 3.2 Workflow Definition Operations

Workflow Definition Operations create, read, and modify workflow definition artifacts. Per [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md), workflow definitions are a first-class resource with their own operations because their structural invariants (step-reference integrity, cycle detection, divergence/convergence balance, actor/skill resolution — see [Workflow Validation](/architecture/workflow-validation.md)) are materially stricter than those of any other artifact type and a malformed workflow blocks Runs at execution time.

| Operation | Effect | When to Use |
|-----------|--------|-------------|
| `workflow.create` | Creates a new workflow definition; runs the full workflow validation suite before commit | When introducing a new workflow |
| `workflow.update` | Updates an existing workflow definition; version bump required | When evolving an existing workflow |
| `workflow.read` | Returns the executable workflow definition body by `id` (optionally by version) | When a caller needs the full workflow document |
| `workflow.list` | Lists workflow definition summaries, filterable by `applies_to`, `status`, `mode` | When browsing available workflows |
| `workflow.validate` | Validates a candidate workflow body without persisting | When authoring or CI-checking a workflow before commit |

**Domain rules:**
- Write operations (`workflow.create`, `workflow.update`) invoke the workflow validation suite directly; validation failures produce structured `validation_failed` responses and do not persist any state.
- `workflow.update` enforces a version bump relative to the prior definition on **every** call — including repeated updates within the same planning run. A reviewer stacking edits on a single `run_id` must bump `version` on each body; the prior definition is read from the branch tip, not from `main`. Bodies that keep the version unchanged are rejected with `validation_failed`.
- Workflow writes produce a single atomic Git commit with structured trailers, consistent with the artifact write invariant in [Git Integration](/architecture/git-integration.md) §5.
- Reviewer role is required for `workflow.create` and `workflow.update`. `workflow.read`, `workflow.list`, and `workflow.validate` require reader.
- `workflow.read` is the **only** way to retrieve an executable workflow body. `artifact.read`, `query.artifacts`, and `query.graph` return summary metadata only (id, path, version, status, applies_to).
- Run binding resolution (see §3.3) uses `workflow.read` internally to pin a definition version to a Run; callers do not need to pre-resolve.

**Lifecycle governance (per [ADR-008](/architecture/adr/ADR-008-workflow-lifecycle-governance.md)):**
- `workflow.create` and `workflow.update` dispatch based on caller role and `write_context`:
  - **Reviewer, no `write_context`** — opens a planning-mode Run under the seeded `workflow-lifecycle` workflow; the response returns `run_id`, `branch_name`, `workflow_path`, and `write_mode: planning`. Approval on the `review` step merges the branch; `needs_rework` loops back to `draft` with the branch kept alive.
  - **`write_context { run_id }` present** — commits to the run's task branch via worktree. Response includes `write_mode: proposed`.
  - **Operator (or admin), no `write_context`** — commits directly to the authoritative branch as a recovery escape hatch. The commit carries a `Workflow-Bypass: true` trailer and the response includes `write_mode: bypass` plus `workflow_bypass: true`. The bypass exists for bootstrap/recovery when the lifecycle flow itself is unavailable; it is not a routine operator path.
- Existing Runs are unaffected by subsequent workflow edits — they stay pinned to the workflow commit SHA captured at `run.start` (per [ADR-001](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md)).

### 3.3 Workflow Execution Operations

Workflow Execution Operations control Run execution and task governance decisions.

**Run lifecycle:**

| Operation | Effect | When to Use |
|-----------|--------|-------------|
| `run.start` | Creates a Run, resolves workflow binding, pins version | When a task is ready for execution |
| `run.start_planning` | Creates a planning Run for artifact creation (per [ADR-006](/architecture/adr/ADR-006-planning-runs.md)) | When a new artifact needs governed creation |
| `run.status` | Queries Run state and step history | When monitoring execution progress |
| `run.cancel` | Cancels an active Run | When execution should be abandoned (operator decision) |

**Step execution:**

| Operation | Effect | When to Use |
|-----------|--------|-------------|
| `step.submit` | Submits a step result, may produce a Git commit | When an actor completes assigned work |
| `step.assign` | Assigns an actor to a step | When overriding automatic actor selection |

**Task governance:**

| Operation | Effect | When to Use |
|-----------|--------|-------------|
| `task.accept` | Records `Approved` acceptance on the task artifact | When deliverable meets acceptance criteria |
| `task.reject` | Records rejection with classification and rationale | When deliverable does not meet criteria |
| `task.cancel` | Sets task status to `Cancelled` | When the task is no longer needed |
| `task.abandon` | Sets task status to `Abandoned` | When the task is abandoned by governance decision |
| `task.supersede` | Sets task status to `Superseded` with successor link | When the task is replaced by new work |

**Domain rules:**
- `run.start` fails if an active Run already exists for the task, or if no active workflow matches the task's `(type, work_type)` pair
- `run.start_planning` resolves to workflows with `mode: creation` and accepts artifact type, initial content, and optional parent path — the target artifact does not need to exist on the authoritative branch
- `step.submit` validates the outcome against the workflow definition and checks actor assignment
- Task governance operations (`accept`, `reject`, `cancel`, `abandon`, `supersede`) are Git writes — they produce durable commits that change the task artifact's front matter
- `task.reject` requires a rationale; the `acceptance` field must be one of `rejected_with_followup` or `rejected_closed` (per [Task Lifecycle](/governance/task-lifecycle.md))

### 3.4 Query Operations

Query operations read from the Projection Store for fast access.

| Operation | Effect | When to Use |
|-----------|--------|-------------|
| `query.artifacts` | Searches by type, status, metadata, full-text | When searching the artifact inventory |
| `query.graph` | Traverses relationship links with configurable depth | When exploring artifact relationships |
| `query.history` | Reads Git commit history for an artifact | When reviewing change history |
| `query.runs` | Lists Runs for a task | When reviewing execution history |
| `execution.tasks` | Lists all tasks with blocking and assignment status | Dashboard overview |
| `execution.tasks.ready` | Lists tasks not blocked and not assigned | Finding available work |
| `execution.tasks.blocked` | Lists tasks blocked by dependencies (includes blocker details) | Identifying bottlenecks |
| `execution.tasks.assigned` | Lists tasks assigned to a specific actor | Viewing actor workload |

**Domain rules:**
- Queries read from projections, which are eventually consistent with Git
- Consumers must tolerate staleness (per [Data Model](/architecture/data-model.md) §4.3)
- `query.history` reads from Git directly, not projections — it always returns authoritative history

### 3.5 System Operations

System operations are administrative and require elevated authorization.

| Operation | Effect | When to Use |
|-----------|--------|-------------|
| `system.health` | Returns component health status | Monitoring, readiness checks |
| `system.metrics` | Returns Prometheus-compatible metrics | Monitoring, alerting |
| `system.rebuild` | Triggers full projection rebuild from Git | After projection corruption or drift |
| `system.validate_all` | Runs validation across all artifacts | Periodic governance audits |

### 3.6 Skill Operations

| Operation | Effect | When to Use |
|-----------|--------|-------------|
| `actor.add_skill` | Assigns a skill to an actor | Configuring actor skills |
| `actor.remove_skill` | Removes a skill from an actor | Revoking skills |
| `actor.list_skills` | Lists skills assigned to an actor | Viewing actor skills |
| `actor.find_eligible` | Lists actors eligible for given skill requirements | Discovering who can execute a step |
| `execution.candidates` | Lists tasks ready for execution, filtered by actor type, skills, blocking status | AI agents and dashboards discovering available work |

**Endpoint:** `GET /api/v1/execution/candidates`

Query parameters:
- `actor_type` — filter by allowed actor type (e.g. `ai_agent`, `human`)
- `skills` — comma-separated skill names the actor has
- `include_blocked` — `true` to include blocked tasks (default: `false`)

Response includes `blocked`, `blocked_by`, and `resolved_blockers` fields on each candidate.

| `execution.claim` | Claims a waiting step execution for an actor | Actor pulling work from the execution pool |

**Endpoint:** `POST /api/v1/execution/claim`

Request body:
```json
{ "actor_id": "...", "execution_id": "..." }
```

Validates: step is in `waiting` state, actor type is eligible, step is not already assigned. Returns assignment details on success, conflict error on contention.

| `execution.release` | Releases a step assignment back to the pool | Actor unable to complete work, needs reassignment |

**Endpoint:** `POST /api/v1/execution/release`

Request body:
```json
{ "actor_id": "...", "assignment_id": "...", "reason": "..." }
```

Validates: actor is the current assignee, assignment is active, step is not in terminal state. Step transitions back to `waiting` for re-claiming.

### 3.7 Divergence Operations

| Operation | Effect | When to Use |
|-----------|--------|-------------|
| `divergence.create_branch` | Creates a new exploratory branch within an active divergence | Actor-driven branch creation during exploratory divergence |
| `divergence.close_window` | Closes the exploratory divergence window | When enough branches have been created |

**Endpoints:**
- `POST /runs/{run_id}/divergences/{divergence_id}/branches` — create branch (body: `branch_id`, `start_step`)
- `POST /runs/{run_id}/divergences/{divergence_id}/close-window` — close window

**Domain rules:**
- `system.rebuild` is asynchronous — the response confirms the rebuild was started, not completed
- `system.validate_all` may be slow on large repositories; it runs against the Projection Store

---

## 4. Error Model

### 4.1 Error Codes

| Code | Meaning |
|------|---------|
| `not_found` | Artifact or resource does not exist |
| `already_exists` | Artifact with this ID or path already exists |
| `validation_failed` | Failed schema or cross-artifact validation |
| `unauthorized` | Authentication required or invalid |
| `forbidden` | Actor lacks required role or skills |
| `conflict` | Operation conflicts with current state |
| `precondition_failed` | Workflow precondition not met |
| `invalid_params` | Request parameters malformed or missing |
| `internal_error` | Unexpected system error |
| `service_unavailable` | System not ready |
| `git_error` | Git operation failed |
| `workflow_not_found` | No active workflow matches for binding resolution |

### 4.2 Error Semantics

- **Validation errors** include structured details (rule_id, artifact_path, field, severity) about which rules failed and where
- **Conflict errors** indicate the operation cannot proceed because of current state (e.g., active Run exists, task already completed) — the caller should inspect state and decide how to proceed
- **Git errors** indicate the durable write failed — the operation was not persisted and may be retried
- **Permission errors** include which role or skill was required vs what the actor has
- Errors are designed to never produce partial state — either the full operation succeeds or nothing changes. This is a target architectural invariant

---

## 5. Asynchronous Behavior

Most operations are synchronous — the response confirms the operation completed (including Git commit if applicable).

Exceptions:

| Operation | Async Behavior |
|-----------|---------------|
| `system.rebuild` | Returns immediately; rebuild runs in background |
| `run.start` | Synchronous (Run created), but execution proceeds asynchronously after |
| `step.submit` | Synchronous (result recorded), but next step assignment is asynchronous |

For `run.start`, the response confirms the Run was created and the first step is ready. The caller does not wait for the Run to complete — they poll `run.status` or consume events.

---

## 6. Authorization Model

Authorization is role-based (per [Security Model](/architecture/security-model.md) §4):

| Role | Can Do |
|------|--------|
| `reader` | All read and query operations |
| `contributor` | Reader + create/update artifacts, start Runs, submit step results |
| `reviewer` | Contributor + task governance decisions (accept, reject, cancel, abandon, supersede) |
| `operator` | Reviewer + system operations, Run cancellation, manual step assignment |
| `admin` | Full access including actor and token management |

Additionally, individual workflow steps may require specific **skills** — the Workflow Engine checks these at assignment time, not the Access Gateway.

---

## 7. Conventions

### 7.1 Pagination

List operations use cursor-based pagination. Callers provide `limit` and `cursor`; responses include `next_cursor` and `has_more`.

### 7.2 Trace ID and Idempotency

All requests may include an `X-Trace-Id` header. If omitted, the Access Gateway generates one. The trace ID appears in all responses (via `X-Trace-Id` response header), events, and Git commits produced by the operation.

Write operations additionally accept an `Idempotency-Key` header. When provided, retrying a request with the same key does not produce duplicate effects (per [Git Integration](/architecture/git-integration.md) §5.6). If `Idempotency-Key` is omitted, `X-Trace-Id` is used as the fallback idempotency key.

Both headers are defined as reusable components in the OpenAPI specification.

---

## 8. Cross-References

- [Access Surface](/architecture/access-surface.md) — Operation categories and internal operation model
- [OpenAPI Specification](/api/spec.yaml) — Concrete HTTP endpoints and JSON schemas
- [Security Model](/architecture/security-model.md) §4 — Role hierarchy and authorization
- [Git Integration](/architecture/git-integration.md) §5 — Commit format for write operations
- [Task Lifecycle](/governance/task-lifecycle.md) — Task terminal states and acceptance model
- [Validation Service](/architecture/validation-service.md) — Cross-artifact validation rules
- [Actor Model](/architecture/actor-model.md) §5 — Step assignment protocol
- [Data Model](/architecture/data-model.md) §4.3 — Projection consistency model
- [ADR-001](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md) — Workflow definition storage and execution recording
- [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md) — Workflow definitions as a separate API resource
- [Workflow Definition Format](/architecture/workflow-definition-format.md) — Schema and file format for workflow definitions
- [Workflow Validation](/architecture/workflow-validation.md) — Workflow-specific validation suite

---

## 9. Evolution Policy

This document evolves alongside the API specification. When new operations are added:

1. Define the semantics and domain rules in this document
2. Define the concrete endpoint in the OpenAPI specification
3. Specify the authorization requirement

Changes that alter operation semantics or the error model should be captured as ADRs.
