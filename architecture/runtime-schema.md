---
type: Architecture
title: Runtime Store Schema
status: Living Document
version: "0.1"
---

# Runtime Store Schema

---

## 1. Purpose

This document defines the production-ready database schema for Spine's runtime and projection stores.

The [Data Model](/architecture/data-model.md) defines the conceptual schema for storage layers. This document converts those conceptual schemas into concrete table definitions with types, constraints, indexes, and operational guidance.

Runtime data is disposable as a source of truth, but only partially reconstructible. Durable outcomes remain reconstructible from Git, while in-progress operational state may be lost and require restart (per Constitution §8). The schema is designed for PostgreSQL, as recommended in [Data Model](/architecture/data-model.md) §7.2.

---

## 2. Database Organization

### 2.1 Schema Separation

Runtime and projection tables are stored in the same PostgreSQL database but separated into distinct schemas:

| Schema | Purpose | Disposable? |
|--------|---------|-------------|
| `projection` | Query-optimized views of Git artifact state | Yes — fully rebuildable from Git |
| `runtime` | Execution state (Runs, steps, queue) | Yes — operational only |

### 2.2 Naming Conventions

- Table names use `snake_case`
- Primary keys are named `<table>_id` or `id`
- Foreign keys are named `<referenced_table>_id`
- Timestamps use `timestamptz` (timezone-aware)
- All string identifiers are `text` (not `varchar` with length limits)

---

## 3. Projection Schema

### 3.1 `projection.artifacts`

The primary projection table. One row per artifact in the repository.

```sql
CREATE TABLE projection.artifacts (
    artifact_path       text        PRIMARY KEY,
    artifact_id         text,                       -- from front matter (e.g., TASK-001)
    artifact_type       text        NOT NULL,       -- from front matter (e.g., Task, Epic)
    title               text,                       -- from front matter
    status              text,                       -- from front matter
    metadata            jsonb       NOT NULL DEFAULT '{}', -- full parsed front matter
    content             text,                       -- markdown body
    links               jsonb       NOT NULL DEFAULT '[]', -- parsed link entries
    source_commit       text        NOT NULL,       -- Git commit SHA this projection reflects
    content_hash        text        NOT NULL,       -- hash of raw file content for change detection
    synced_at           timestamptz NOT NULL DEFAULT now()
);
```

**Indexes:**

```sql
CREATE INDEX idx_artifacts_type ON projection.artifacts (artifact_type);
CREATE INDEX idx_artifacts_status ON projection.artifacts (status);
CREATE INDEX idx_artifacts_type_status ON projection.artifacts (artifact_type, status);
CREATE INDEX idx_artifacts_id ON projection.artifacts (artifact_id);
CREATE INDEX idx_artifacts_source_commit ON projection.artifacts (source_commit);
CREATE INDEX idx_artifacts_links ON projection.artifacts USING gin (links);
CREATE INDEX idx_artifacts_metadata ON projection.artifacts USING gin (metadata);
```

### 3.2 `projection.artifact_links`

Denormalized link table for efficient graph queries. Derived from the `links` field in `projection.artifacts`.

```sql
CREATE TABLE projection.artifact_links (
    source_path         text        NOT NULL,       -- artifact containing the link
    target_path         text        NOT NULL,       -- artifact being linked to
    link_type           text        NOT NULL,       -- parent, blocks, supersedes, etc.
    source_commit       text        NOT NULL,       -- commit this link was derived from

    PRIMARY KEY (source_path, target_path, link_type)
);
```

-- NOTE: This table is intentionally minimal. Additional link validation
-- (e.g., inverse consistency, target existence) is handled by the validation service
-- and may be extended in future schema versions if required.

**Indexes:**

```sql
CREATE INDEX idx_links_target ON projection.artifact_links (target_path);
CREATE INDEX idx_links_type ON projection.artifact_links (link_type);
CREATE INDEX idx_links_source_target ON projection.artifact_links (source_path, target_path);
```

### 3.3 `projection.workflows`

Projection of workflow definition files for fast lookup during binding resolution.

```sql
CREATE TABLE projection.workflows (
    workflow_path       text        PRIMARY KEY,
    workflow_id         text        NOT NULL,
    name                text        NOT NULL,
    version             text        NOT NULL,
    status              text        NOT NULL,       -- Active, Deprecated, Superseded
    applies_to          jsonb       NOT NULL,       -- parsed applies_to clause
    definition          jsonb       NOT NULL,       -- full parsed workflow YAML
    source_commit       text        NOT NULL,
    synced_at           timestamptz NOT NULL DEFAULT now()
);
```

**Indexes:**

```sql
CREATE INDEX idx_workflows_status ON projection.workflows (status);
CREATE INDEX idx_workflows_id ON projection.workflows (workflow_id);
CREATE INDEX idx_workflows_applies_to ON projection.workflows USING gin (applies_to);
```

### 3.4 `projection.sync_state`

Tracks the overall projection sync progress. Current design assumes a single repository and projection pipeline (v0.x).

```sql
CREATE TABLE projection.sync_state (
    id                  text        PRIMARY KEY DEFAULT 'global',
    last_synced_commit  text        NOT NULL,       -- last Git commit fully projected
    last_synced_at      timestamptz NOT NULL,
    status              text        NOT NULL DEFAULT 'idle', -- idle, syncing, rebuilding, error
    error_detail        text                         -- last error if status = error
);
```

---

## 4. Runtime Schema

### 4.0 Status Authority

Runtime tables represent execution state only and are not the source of truth for governed artifact lifecycle.

- Task lifecycle status (e.g., Completed, Cancelled, Rejected, Superseded, Abandoned) is defined and persisted in Git artifacts
- Run status (`runtime.runs.status`) represents execution lifecycle only
- Step execution status (`runtime.step_executions.status`) represents execution progress only

Runtime statuses MUST NOT be interpreted as artifact lifecycle state.

### 4.1 `runtime.runs`

One row per workflow Run.

```sql
CREATE TABLE runtime.runs (
    run_id              text        PRIMARY KEY,
    task_path           text        NOT NULL,       -- path to governed Task artifact
    workflow_path       text        NOT NULL,       -- path to governing Workflow Definition
    workflow_id         text        NOT NULL,       -- stable identifier of workflow
    workflow_version    text        NOT NULL,       -- Git commit SHA of pinned workflow
    workflow_version_label text,                     -- semantic version (informational)
    status              text        NOT NULL DEFAULT 'pending',
                                                    -- pending, active, paused, committing, completed, failed, cancelled
    current_step_id     text,                       -- active step; null during divergence
    trace_id            text        NOT NULL,       -- observability correlation ID
    started_at          timestamptz,
    completed_at        timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT runs_status_check CHECK (status IN ('pending', 'active', 'paused', 'committing', 'completed', 'failed', 'cancelled'))
);
```

**Indexes:**

```sql
CREATE INDEX idx_runs_task_path ON runtime.runs (task_path);
CREATE INDEX idx_runs_status ON runtime.runs (status);
CREATE INDEX idx_runs_trace_id ON runtime.runs (trace_id);
CREATE INDEX idx_runs_workflow_path ON runtime.runs (workflow_path);
CREATE INDEX idx_runs_created_at ON runtime.runs (created_at);
```

### 4.2 `runtime.step_executions`

One row per step execution attempt within a Run.

```sql
CREATE TABLE runtime.step_executions (
    execution_id        text        PRIMARY KEY,
    run_id              text        NOT NULL REFERENCES runtime.runs(run_id),
    step_id             text        NOT NULL,       -- step within workflow definition
    branch_id           text,                       -- null for main graph; set for divergence branches
    actor_id            text,                       -- assigned actor; null while waiting
    status              text        NOT NULL DEFAULT 'waiting',
                                                    -- waiting, assigned, in_progress, blocked, completed, failed, skipped
    attempt             integer     NOT NULL DEFAULT 1,  -- retry count
    outcome_id          text,                       -- workflow-defined outcome value
    started_at          timestamptz,
    completed_at        timestamptz,
    error_detail        jsonb,                      -- structured failure information
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT step_status_check CHECK (status IN ('waiting', 'assigned', 'in_progress', 'blocked', 'completed', 'failed', 'skipped'))
);
```

**Indexes:**

```sql
CREATE INDEX idx_step_exec_run_id ON runtime.step_executions (run_id);
CREATE INDEX idx_step_exec_status ON runtime.step_executions (status);
CREATE INDEX idx_step_exec_actor_id ON runtime.step_executions (actor_id);
CREATE INDEX idx_step_exec_run_step ON runtime.step_executions (run_id, step_id);
CREATE INDEX idx_step_exec_branch ON runtime.step_executions (run_id, branch_id);
```

### 4.3 `runtime.divergence_contexts`

Tracks divergence and convergence state within a Run.

```sql
CREATE TABLE runtime.divergence_contexts (
    divergence_id       text        NOT NULL,
    run_id              text        NOT NULL REFERENCES runtime.runs(run_id),
    status              text        NOT NULL DEFAULT 'pending',
                                                    -- pending, active, converging, resolved, failed
    divergence_mode     text        NOT NULL,       -- structured, exploratory
    divergence_window   text        DEFAULT 'open', -- open, closed (exploratory only)
    convergence_id      text,                       -- associated convergence point
    triggered_at        timestamptz,
    resolved_at         timestamptz,

    PRIMARY KEY (run_id, divergence_id),
    CONSTRAINT div_status_check CHECK (status IN ('pending', 'active', 'converging', 'resolved', 'failed')),
    CONSTRAINT div_mode_check CHECK (divergence_mode IN ('structured', 'exploratory'))
);
```

### 4.4 `runtime.branches`

Tracks individual branch state within a divergence context.

```sql
CREATE TABLE runtime.branches (
    branch_id           text        NOT NULL,
    run_id              text        NOT NULL,
    divergence_id       text        NOT NULL,
    status              text        NOT NULL DEFAULT 'pending',
                                                    -- pending, in_progress, completed, failed
    current_step_id     text,
    outcome             jsonb,                      -- branch result summary (not a workflow outcome)
    artifacts_produced  jsonb       DEFAULT '[]',   -- paths of artifacts created by this branch
    created_at          timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz,

    PRIMARY KEY (run_id, divergence_id, branch_id),
    FOREIGN KEY (run_id, divergence_id) REFERENCES runtime.divergence_contexts(run_id, divergence_id),
    CONSTRAINT branch_status_check CHECK (status IN ('pending', 'in_progress', 'completed', 'failed'))
);
```

### 4.5 `runtime.convergence_results`

Records the outcome of convergence evaluation.

```sql
CREATE TABLE runtime.convergence_results (
    run_id              text        NOT NULL,
    divergence_id       text        NOT NULL,
    convergence_id      text,                       -- explicit convergence identifier (if defined)
    strategy_applied    text        NOT NULL,       -- select_one, select_subset, merge, require_all, experiment
    entry_policy_applied text       NOT NULL,       -- which entry policy triggered convergence
    selected_branch     text,                       -- for select_one
    selected_branches   jsonb       DEFAULT '[]',   -- for select_subset, experiment
    merged_artifact     text,                       -- for merge: path to merged artifact
    experiment_artifact text,                       -- for experiment: path to experiment artifact
    evaluator_actor_id  text,                       -- actor who performed evaluation
    rationale           text,                       -- evaluation rationale
    evaluated_at        timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (run_id, divergence_id),
    FOREIGN KEY (run_id, divergence_id) REFERENCES runtime.divergence_contexts(run_id, divergence_id)
);
```

### 4.6 `runtime.queue_entries`

Pending work items for step assignments and event delivery.

```sql
CREATE TABLE runtime.queue_entries (
    entry_id            text        PRIMARY KEY,
    entry_type          text        NOT NULL,       -- step_assignment, event_delivery, retry, recovery_check (extensible)
    payload             jsonb       NOT NULL,       -- entry-specific data
    status              text        NOT NULL DEFAULT 'pending',
                                                    -- pending, processing, completed, failed, dead_letter
    idempotency_key     text        UNIQUE,         -- prevents duplicate processing
    priority            integer     NOT NULL DEFAULT 0,  -- higher = more urgent
    max_attempts        integer     NOT NULL DEFAULT 3,
    attempt_count       integer     NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    scheduled_at        timestamptz NOT NULL DEFAULT now(), -- for delayed processing
    processing_at       timestamptz,                -- when a worker picked it up
    completed_at        timestamptz,
    error_detail        jsonb,                      -- last failure information

    CONSTRAINT queue_status_check CHECK (status IN ('pending', 'processing', 'completed', 'failed', 'dead_letter'))
);
```

**Indexes:**

```sql
CREATE INDEX idx_queue_status_scheduled ON runtime.queue_entries (status, scheduled_at)
    WHERE status = 'pending';
CREATE INDEX idx_queue_idempotency ON runtime.queue_entries (idempotency_key);
CREATE INDEX idx_queue_type ON runtime.queue_entries (entry_type);
```

### 4.7 `runtime.actor_assignments`

Tracks active step assignments to actors.

```sql
CREATE TABLE runtime.actor_assignments (
    assignment_id       text        PRIMARY KEY,
    run_id              text        NOT NULL REFERENCES runtime.runs(run_id),
    execution_id        text        NOT NULL REFERENCES runtime.step_executions(execution_id),
    actor_id            text        NOT NULL,
    status              text        NOT NULL DEFAULT 'active',
                                                    -- active, completed, cancelled, timed_out
    assigned_at         timestamptz NOT NULL DEFAULT now(),
    responded_at        timestamptz,
    timeout_at          timestamptz,                -- when this assignment expires

    CONSTRAINT assignment_status_check CHECK (status IN ('active', 'completed', 'cancelled', 'timed_out'))
);
```

-- Ensure only one active assignment per execution
CREATE UNIQUE INDEX idx_assignments_active_execution
    ON runtime.actor_assignments (execution_id)
    WHERE status = 'active';

**Indexes:**

```sql
CREATE INDEX idx_assignments_actor ON runtime.actor_assignments (actor_id, status);
CREATE INDEX idx_assignments_run ON runtime.actor_assignments (run_id);
CREATE INDEX idx_assignments_timeout ON runtime.actor_assignments (timeout_at)
    WHERE status = 'active';
```

### 4.8 `auth.skills`

Workspace-scoped skill entities that formalize the skill system. Skills are registered in the `auth` schema alongside actors and tokens.

```sql
CREATE TABLE auth.skills (
    skill_id    text        PRIMARY KEY,
    name        text        NOT NULL UNIQUE,       -- unique within workspace
    description text        NOT NULL DEFAULT '',
    category    text        NOT NULL DEFAULT '',    -- e.g. "development", "review", "operations"
    status      text        NOT NULL DEFAULT 'active',
                                                    -- active, deprecated
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT skill_status_check CHECK (status IN ('active', 'deprecated'))
);
```

**Indexes:**

```sql
CREATE INDEX idx_skills_name ON auth.skills (name);
CREATE INDEX idx_skills_category ON auth.skills (category);
CREATE INDEX idx_skills_status ON auth.skills (status);
```

### 4.8.1 `auth.actor_skills`

Junction table for the many-to-many relationship between actors and skills.

```sql
CREATE TABLE auth.actor_skills (
    actor_id    text        NOT NULL REFERENCES auth.actors(actor_id),
    skill_id    text        NOT NULL REFERENCES auth.skills(skill_id),
    assigned_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (actor_id, skill_id)
);
```

**Indexes:**

```sql
CREATE INDEX idx_actor_skills_skill ON auth.actor_skills (skill_id);
```

### 4.9 JSONB Field Semantics

The following JSONB fields are used for flexible or evolving data structures:

- `projection.artifacts.metadata` — schema-governed front matter
- `projection.artifacts.links` — parsed link structures
- `projection.workflows.definition` — canonical parsed workflow definition
- `runtime.step_executions.error_detail` — structured failure data (implementation-defined)
- `runtime.queue_entries.payload` — entry-specific data (type-dependent)
- `runtime.queue_entries.error_detail` — structured failure data
- `runtime.branches.outcome` — branch result summary
- `runtime.branches.artifacts_produced` — list of artifact paths

Fields marked as implementation-defined may evolve without requiring SQL schema changes but must remain backward-compatible at the application level.

---

## 5. Idempotency Strategy

### 5.1 Purpose

Idempotency prevents duplicate operations when messages are retried or delivered more than once.

### 5.2 Queue Idempotency

Queue entries use the `idempotency_key` column. Before inserting a new queue entry:

1. Generate a deterministic key from the operation context (e.g., `step_assignment:{run_id}:{step_id}:{attempt}`)
2. Attempt insert with the key
3. If a conflict occurs (key already exists), the operation is a duplicate — skip it

### 5.3 Step Execution Idempotency

Step result submissions are idempotent by checking:

1. The `assignment_id` must match an active assignment
2. The `execution_id` must be in `assigned` or `in_progress` status
3. If the execution is already `completed` or `failed`, the duplicate response is ignored

### 5.4 Run Creation Idempotency

Run creation uses a deterministic `run_id` derived from `task_path + timestamp + requestor`. If a Run already exists with the same ID, creation is rejected.

---

## 6. Data Archival

### 6.1 Archival Strategy

Runtime data accumulates over time. The following archival strategy keeps the operational tables performant:

| Data | Retention | Archival |
|------|-----------|---------|
| Active Runs and their step executions | Indefinite (while active) | — |
| Completed/failed Runs | Configurable (default: 90 days) | Move to `runtime.archived_runs` |
| Queue entries (completed) | 7 days | Delete |
| Queue entries (dead_letter) | 30 days | Delete |

### 6.2 Archival Tables

Archived Runs are moved to partitioned archival tables with the same schema:

```sql
CREATE TABLE runtime.archived_runs (LIKE runtime.runs INCLUDING ALL);
CREATE TABLE runtime.archived_step_executions (LIKE runtime.step_executions INCLUDING ALL);
```

Archival is operator-configured. Spine does not enforce retention — operators choose when and what to archive.

---

## 7. Migration Policy

### 7.1 Projection Schema Migrations

Projection tables require no formal migration. If the schema changes:

1. Drop the projection schema
2. Recreate tables with the new schema
3. Trigger a full projection rebuild from Git

This is safe because projection data is fully derived from Git.

### 7.2 Runtime Schema Migrations

Runtime tables require standard database migration practices:

- Migrations are versioned and applied sequentially
- Backward-compatible changes (add column, add index) are preferred
- Breaking changes (remove column, change type) require a migration plan
- Migrations must handle in-progress Runs gracefully — do not break active execution state

### 7.3 Migration Tooling

Migration files are stored in the repository under:

```
/migrations/<sequence>_<description>.sql
```

Example:

```
/migrations/001_initial_schema.sql
/migrations/002_add_branch_metadata.sql
```

---

## 8. Constitutional Alignment

| Principle | How the Schema Supports It |
|-----------|---------------------------|
| Source of Truth (§2) | Projection tables are derived from Git; runtime tables are operational only |
| Disposable Database (§8) | All tables are explicitly disposable — projections rebuild from Git, runtime state is operational |
| Reproducibility (§7) | `source_commit` on projections enables freshness verification; `trace_id` on Runs enables audit correlation |

---

## 9. Cross-References

- [Data Model](/architecture/data-model.md) — Conceptual schema this document makes concrete
- [Divergence and Convergence](/architecture/divergence-and-convergence.md) §5 — Runtime state model for branches
- [Observability](/architecture/observability.md) §3 — Trace ID on Runs
- [Error Handling](/architecture/error-handling-and-recovery.md) §6 — Orphaned Run detection
- [Actor Model](/architecture/actor-model.md) §5 — Step assignment protocol
- [Event Schemas](/architecture/event-schemas.md) — Event envelope stored in queue entries

---

## 10. Evolution Policy

This schema is expected to evolve as the system is implemented.

Areas expected to require refinement:

- Table partitioning for high-volume step executions
- Read replica configuration for projection queries
- Full-text search indexes on artifact content
- Materialized views for common aggregate queries (epic progress, initiative status)
- Audit-specific indexes for compliance queries

Schema changes should follow the migration policy defined in §7. Changes that alter the boundary between projection and runtime schemas should be captured as ADRs.
