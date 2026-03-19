---
type: Architecture
title: API Operation Schemas
status: Living Document
version: "0.1"
---

# API Operation Schemas

---

## 1. Purpose

This document defines the detailed request/response schemas for all operations exposed through the Spine access surface.

The [Access Surface](/architecture/access-surface.md) defines operation categories and the internal operation model. This document specifies the concrete JSON schemas, error codes, and authorization requirements for each operation, enabling CLI and API implementation.

---

## 2. Common Conventions

### 2.1 Request Envelope

All API requests use JSON and follow this structure:

```json
{
  "operation": "<operation_name>",
  "params": { ... },
  "trace_id": "<uuid or null>"
}
```

- `operation` — required, identifies the operation
- `params` — required, operation-specific parameters
- `trace_id` — optional; if omitted, the Access Gateway generates one

CLI and GUI adapters translate their native input into this format before forwarding to the Access Gateway.

### 2.2 Response Envelope

All responses follow this structure:

```json
{
  "status": "ok | error",
  "data": { ... },
  "trace_id": "<uuid>",
  "errors": [ ... ]
}
```

- `status` — `ok` for success, `error` for failure
- `data` — operation-specific response (null on error)
- `trace_id` — always present (generated if not provided in request)
- `errors` — array of error objects (empty on success)

### 2.3 Error Format

```json
{
  "code": "<error_code>",
  "message": "<human-readable description>",
  "detail": { ... }
}
```

### 2.4 Pagination

List operations support cursor-based pagination:

```json
{
  "params": {
    "limit": 50,
    "cursor": "<opaque_cursor_string or null>"
  }
}
```

Paginated responses include:

```json
{
  "data": {
    "items": [ ... ],
    "next_cursor": "<cursor or null>",
    "has_more": true
  }
}
```

### 2.5 Authorization Shorthand

Each operation specifies the minimum required role using the hierarchy: `reader < contributor < reviewer < operator < admin` (per [Security Model](/architecture/security-model.md) §4.2).

---

## 3. Error Codes

| Code | HTTP Status | Meaning |
|------|-------------|---------|
| `not_found` | 404 | Artifact or resource does not exist |
| `already_exists` | 409 | Artifact with this ID or path already exists |
| `validation_failed` | 422 | Artifact or request failed schema or cross-artifact validation |
| `unauthorized` | 401 | Authentication required or invalid |
| `forbidden` | 403 | Actor lacks required role or capabilities |
| `conflict` | 409 | Operation conflicts with current state (e.g., Run already active) |
| `precondition_failed` | 412 | Workflow precondition not met |
| `invalid_params` | 400 | Request parameters are malformed or missing |
| `internal_error` | 500 | Unexpected system error |
| `service_unavailable` | 503 | System not ready (e.g., projection rebuild in progress) |
| `git_error` | 502 | Git operation failed |
| `workflow_not_found` | 404 | No active workflow matches the artifact for binding resolution |

---

## 4. Artifact Operations

### 4.1 `artifact.create`

Create a new artifact with validated front matter.

**Min role:** `contributor`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `path` | string | yes | Repository-relative path for the new artifact |
| `content` | string | yes | Full artifact content (front matter + markdown body) |

**Response data:**

```json
{
  "artifact_path": "/initiatives/INIT-002/initiative.md",
  "artifact_id": "INIT-002",
  "artifact_type": "Initiative",
  "commit_sha": "abc123..."
}
```

**Errors:** `already_exists`, `validation_failed`, `forbidden`, `git_error`

---

### 4.2 `artifact.read`

Read artifact content and metadata.

**Min role:** `reader`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `path` | string | yes | Repository-relative path of the artifact |
| `ref` | string | no | Git ref to read from (default: authoritative branch HEAD) |

**Response data:**

```json
{
  "artifact_path": "/initiatives/INIT-001/initiative.md",
  "artifact_id": "INIT-001",
  "artifact_type": "Initiative",
  "status": "In Progress",
  "title": "Foundations",
  "metadata": { ... },
  "content": "# INIT-001 — Foundations\n...",
  "source_commit": "def456..."
}
```

**Errors:** `not_found`

---

### 4.3 `artifact.update`

Update artifact content or metadata.

**Min role:** `contributor`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `path` | string | yes | Repository-relative path of the artifact |
| `content` | string | yes | Updated full artifact content |

**Response data:**

```json
{
  "artifact_path": "/initiatives/INIT-001/initiative.md",
  "commit_sha": "ghi789..."
}
```

**Errors:** `not_found`, `validation_failed`, `forbidden`, `git_error`

---

### 4.4 `artifact.validate`

Validate an artifact against schema and cross-artifact rules without persisting.

**Min role:** `reader`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `path` | string | yes | Repository-relative path of the artifact |
| `content` | string | no | Content to validate (if omitted, validates the current artifact in Git) |

**Response data:**

```json
{
  "artifact_path": "/tasks/TASK-001.md",
  "status": "passed | failed | warnings",
  "errors": [ ... ],
  "warnings": [ ... ]
}
```

**Errors:** `not_found` (if path given without content and artifact doesn't exist)

---

### 4.5 `artifact.list`

List artifacts by type, status, or parent.

**Min role:** `reader`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | no | Filter by artifact type |
| `status` | string | no | Filter by status |
| `parent_path` | string | no | Filter by parent artifact path |
| `limit` | integer | no | Max results (default: 50, max: 200) |
| `cursor` | string | no | Pagination cursor |

**Response data:**

```json
{
  "items": [
    {
      "artifact_path": "...",
      "artifact_id": "...",
      "artifact_type": "...",
      "status": "...",
      "title": "..."
    }
  ],
  "next_cursor": "...",
  "has_more": false
}
```

---

### 4.6 `artifact.links`

Query artifact relationships.

**Min role:** `reader`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `path` | string | yes | Artifact to query links for |
| `link_type` | string | no | Filter by link type (parent, blocks, etc.) |
| `direction` | string | no | `outgoing` (default), `incoming`, or `both` |

**Response data:**

```json
{
  "artifact_path": "...",
  "links": [
    {
      "direction": "outgoing",
      "link_type": "parent",
      "target_path": "..."
    }
  ]
}
```

---

## 5. Workflow Operations

### 5.1 `run.start`

Start a new Run for a task.

**Min role:** `contributor`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `task_path` | string | yes | Path to the Task artifact |

The Workflow Engine resolves the workflow binding, pins the version, and creates the Run.

**Response data:**

```json
{
  "run_id": "run-abc123",
  "task_path": "...",
  "workflow_id": "task-execution",
  "workflow_version": "abc123...",
  "trace_id": "...",
  "status": "pending"
}
```

**Errors:** `not_found`, `workflow_not_found`, `conflict` (active Run already exists), `validation_failed` (task not in valid state for Run)

---

### 5.2 `run.status`

Query Run execution state.

**Min role:** `reader`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `run_id` | string | yes | Run identifier |

**Response data:**

```json
{
  "run_id": "run-abc123",
  "task_path": "...",
  "workflow_id": "...",
  "status": "active",
  "current_step_id": "execute",
  "trace_id": "...",
  "started_at": "...",
  "step_executions": [
    {
      "execution_id": "...",
      "step_id": "assign",
      "status": "completed",
      "actor_id": "...",
      "outcome_id": "assigned",
      "attempt": 1
    }
  ]
}
```

**Errors:** `not_found`

---

### 5.3 `run.cancel`

Cancel an in-progress Run.

**Min role:** `operator`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `run_id` | string | yes | Run identifier |
| `reason` | string | no | Cancellation rationale |

**Response data:**

```json
{
  "run_id": "run-abc123",
  "status": "cancelled"
}
```

**Errors:** `not_found`, `conflict` (Run not in cancellable state)

---

### 5.4 `step.submit`

Submit step result for evaluation.

**Min role:** `contributor`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `assignment_id` | string | yes | Active assignment identifier |
| `outcome_id` | string | yes | One of the step's declared outcomes |
| `output` | object | no | Step-specific output data |
| `output.artifacts_produced` | string[] | no | Paths of artifacts created or modified |
| `output.data` | object | no | Structured data output |
| `output.summary` | string | no | Human-readable summary |

**Response data:**

```json
{
  "execution_id": "...",
  "step_id": "execute",
  "outcome_id": "submitted",
  "next_step": "review",
  "commit_sha": "abc123..."
}
```

`commit_sha` is present only if the outcome has a `commit` effect.

**Errors:** `not_found`, `invalid_params` (outcome_id not valid), `conflict` (assignment not active), `validation_failed` (artifacts don't conform), `git_error`

---

### 5.5 `step.assign`

Assign an actor to a step.

**Min role:** `operator`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `run_id` | string | yes | Run identifier |
| `step_id` | string | yes | Step to assign |
| `actor_id` | string | yes | Actor to assign |

**Response data:**

```json
{
  "assignment_id": "...",
  "run_id": "...",
  "step_id": "...",
  "actor_id": "...",
  "status": "active"
}
```

**Errors:** `not_found`, `forbidden` (actor not eligible), `conflict` (step not in assignable state)

---

### 5.6 `task.accept`

Record task-level acceptance.

**Min role:** `reviewer`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `task_path` | string | yes | Path to the Task artifact |
| `rationale` | string | no | Acceptance rationale |

**Response data:**

```json
{
  "task_path": "...",
  "acceptance": "Approved",
  "commit_sha": "..."
}
```

**Errors:** `not_found`, `forbidden`, `conflict` (task not in acceptable state), `git_error`

---

### 5.7 `task.reject`

Record task-level rejection.

**Min role:** `reviewer`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `task_path` | string | yes | Path to the Task artifact |
| `acceptance` | string | yes | `Rejected With Followup` or `Rejected Closed` |
| `rationale` | string | yes | Rejection rationale (required) |
| `followup_path` | string | no | Path to follow-up task (for `Rejected With Followup`) |

**Response data:**

```json
{
  "task_path": "...",
  "acceptance": "Rejected With Followup",
  "commit_sha": "..."
}
```

**Errors:** `not_found`, `forbidden`, `invalid_params`, `conflict`, `git_error`

---

### 5.8 `task.cancel`

Cancel a task with rationale.

**Min role:** `reviewer`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `task_path` | string | yes | Path to the Task artifact |
| `rationale` | string | yes | Cancellation rationale |

**Response data:**

```json
{
  "task_path": "...",
  "status": "Cancelled",
  "commit_sha": "..."
}
```

**Errors:** `not_found`, `forbidden`, `conflict`, `git_error`

---

### 5.9 `task.abandon`

Abandon a task by governance decision.

**Min role:** `reviewer`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `task_path` | string | yes | Path to the Task artifact |
| `rationale` | string | yes | Abandonment rationale |

**Response data:**

```json
{
  "task_path": "...",
  "status": "Abandoned",
  "commit_sha": "..."
}
```

**Errors:** `not_found`, `forbidden`, `conflict`, `git_error`

---

### 5.10 `task.supersede`

Supersede a task with successor work.

**Min role:** `reviewer`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `task_path` | string | yes | Path to the Task artifact |
| `successor_path` | string | yes | Path to the successor artifact |
| `rationale` | string | no | Supersession rationale |

**Response data:**

```json
{
  "task_path": "...",
  "status": "Superseded",
  "successor_path": "...",
  "commit_sha": "..."
}
```

**Errors:** `not_found`, `forbidden`, `invalid_params` (successor doesn't exist), `conflict`, `git_error`

---

## 6. Query Operations

### 6.1 `query.artifacts`

Search artifacts by type, status, and metadata fields.

**Min role:** `reader`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | no | Filter by artifact type |
| `status` | string | no | Filter by status |
| `metadata` | object | no | Filter by metadata field values (e.g., `{"work_type": "spike"}`) |
| `search` | string | no | Full-text search across title and content |
| `limit` | integer | no | Max results (default: 50, max: 200) |
| `cursor` | string | no | Pagination cursor |

**Response data:**

```json
{
  "items": [
    {
      "artifact_path": "...",
      "artifact_id": "...",
      "artifact_type": "...",
      "status": "...",
      "title": "...",
      "metadata": { ... }
    }
  ],
  "next_cursor": "...",
  "has_more": false
}
```

---

### 6.2 `query.graph`

Retrieve relationship graph for an artifact.

**Min role:** `reader`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `path` | string | yes | Root artifact path |
| `depth` | integer | no | Max traversal depth (default: 2, max: 5) |
| `link_types` | string[] | no | Filter to specific link types |

**Response data:**

```json
{
  "root": "...",
  "nodes": [
    {
      "artifact_path": "...",
      "artifact_type": "...",
      "status": "...",
      "title": "..."
    }
  ],
  "edges": [
    {
      "source": "...",
      "target": "...",
      "link_type": "parent"
    }
  ]
}
```

---

### 6.3 `query.history`

View artifact change history from Git.

**Min role:** `reader`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `path` | string | yes | Artifact path |
| `limit` | integer | no | Max commits (default: 20) |
| `cursor` | string | no | Pagination cursor |

**Response data:**

```json
{
  "items": [
    {
      "commit_sha": "...",
      "timestamp": "...",
      "author": "...",
      "message": "...",
      "trace_id": "...",
      "operation": "..."
    }
  ],
  "next_cursor": "...",
  "has_more": false
}
```

---

### 6.4 `query.runs`

List Runs for a task with execution state.

**Min role:** `reader`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `task_path` | string | yes | Path to the Task artifact |
| `status` | string | no | Filter by Run status |
| `limit` | integer | no | Max results (default: 20) |
| `cursor` | string | no | Pagination cursor |

**Response data:**

```json
{
  "items": [
    {
      "run_id": "...",
      "workflow_id": "...",
      "status": "completed",
      "trace_id": "...",
      "started_at": "...",
      "completed_at": "..."
    }
  ],
  "next_cursor": "...",
  "has_more": false
}
```

---

## 7. System Operations

### 7.1 `system.health`

Runtime health check.

**Min role:** `reader`

**Request params:** none

**Response data:**

```json
{
  "status": "healthy | degraded | unhealthy",
  "components": {
    "artifact_service": "healthy",
    "workflow_engine": "healthy",
    "projection_service": "healthy",
    "event_router": "healthy"
  },
  "projection_lag_ms": 150,
  "active_runs": 3
}
```

---

### 7.2 `system.rebuild`

Trigger full projection rebuild from Git.

**Min role:** `operator`

**Request params:** none

**Response data:**

```json
{
  "status": "started",
  "rebuild_id": "rebuild-xyz"
}
```

This is an asynchronous operation. Use `system.health` to monitor progress.

**Errors:** `conflict` (rebuild already in progress)

---

### 7.3 `system.validate_all`

Run schema and cross-artifact validation across all artifacts.

**Min role:** `operator`

**Request params:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `categories` | string[] | no | Restrict to specific rule categories |

**Response data:**

```json
{
  "status": "passed | failed | warnings",
  "total_artifacts": 42,
  "passed": 38,
  "warnings": 3,
  "failed": 1,
  "results": [
    {
      "artifact_path": "...",
      "status": "failed",
      "errors": [ ... ],
      "warnings": [ ... ]
    }
  ]
}
```

---

## 8. Authorization Summary

| Operation | Min Role |
|-----------|----------|
| `artifact.create` | `contributor` |
| `artifact.read` | `reader` |
| `artifact.update` | `contributor` |
| `artifact.validate` | `reader` |
| `artifact.list` | `reader` |
| `artifact.links` | `reader` |
| `run.start` | `contributor` |
| `run.status` | `reader` |
| `run.cancel` | `operator` |
| `step.submit` | `contributor` |
| `step.assign` | `operator` |
| `task.accept` | `reviewer` |
| `task.reject` | `reviewer` |
| `task.cancel` | `reviewer` |
| `task.abandon` | `reviewer` |
| `task.supersede` | `reviewer` |
| `query.artifacts` | `reader` |
| `query.graph` | `reader` |
| `query.history` | `reader` |
| `query.runs` | `reader` |
| `system.health` | `reader` |
| `system.rebuild` | `operator` |
| `system.validate_all` | `operator` |

---

## 9. Cross-References

- [Access Surface](/architecture/access-surface.md) — Operation categories and internal operation model
- [Security Model](/architecture/security-model.md) §4 — Role hierarchy and authorization enforcement
- [Artifact Schema](/governance/artifact-schema.md) — Front matter validation rules
- [Task Lifecycle](/governance/task-lifecycle.md) — Task terminal states and acceptance model
- [Validation Service](/architecture/validation-service.md) — Cross-artifact validation rules
- [Actor Model](/architecture/actor-model.md) §5 — Step assignment protocol
- [Git Integration](/architecture/git-integration.md) §5 — Commit format for operations that modify Git

---

## 10. Evolution Policy

This API specification is expected to evolve as the system is implemented.

Areas expected to require refinement:

- Webhook registration and management operations
- Batch operations for bulk artifact updates
- Streaming endpoints for real-time event consumption
- Workflow definition management operations (create, activate, deprecate)
- Actor management operations (register, suspend, deactivate)

New operations should be added to this document with request/response schemas and authorization requirements before implementation.
