-- Spine v0.x Initial Schema
-- Per runtime-schema.md §3 (Projection) and §4 (Runtime)

-- ── Schemas ──

CREATE SCHEMA IF NOT EXISTS projection;
CREATE SCHEMA IF NOT EXISTS runtime;

-- ── Migration Tracking ──

CREATE TABLE IF NOT EXISTS public.schema_migrations (
    version     text        PRIMARY KEY,
    applied_at  timestamptz NOT NULL DEFAULT now()
);

-- ════════════════════════════════════════════════════════════════════════════
-- PROJECTION SCHEMA
-- ════════════════════════════════════════════════════════════════════════════

-- §3.1 projection.artifacts
CREATE TABLE projection.artifacts (
    artifact_path       text        PRIMARY KEY,
    artifact_id         text,
    artifact_type       text        NOT NULL,
    title               text,
    status              text,
    metadata            jsonb       NOT NULL DEFAULT '{}',
    content             text,
    links               jsonb       NOT NULL DEFAULT '[]',
    source_commit       text        NOT NULL,
    content_hash        text        NOT NULL,
    synced_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_artifacts_type ON projection.artifacts (artifact_type);
CREATE INDEX idx_artifacts_status ON projection.artifacts (status);
CREATE INDEX idx_artifacts_type_status ON projection.artifacts (artifact_type, status);
CREATE INDEX idx_artifacts_id ON projection.artifacts (artifact_id);
CREATE INDEX idx_artifacts_source_commit ON projection.artifacts (source_commit);
CREATE INDEX idx_artifacts_links ON projection.artifacts USING gin (links);
CREATE INDEX idx_artifacts_metadata ON projection.artifacts USING gin (metadata);

-- §3.2 projection.artifact_links
CREATE TABLE projection.artifact_links (
    source_path         text        NOT NULL,
    target_path         text        NOT NULL,
    link_type           text        NOT NULL,
    source_commit       text        NOT NULL,

    PRIMARY KEY (source_path, target_path, link_type)
);

CREATE INDEX idx_links_target ON projection.artifact_links (target_path);
CREATE INDEX idx_links_type ON projection.artifact_links (link_type);
CREATE INDEX idx_links_source_target ON projection.artifact_links (source_path, target_path);

-- §3.3 projection.workflows
CREATE TABLE projection.workflows (
    workflow_path       text        PRIMARY KEY,
    workflow_id         text        NOT NULL,
    name                text        NOT NULL,
    version             text        NOT NULL,
    status              text        NOT NULL,
    applies_to          jsonb       NOT NULL,
    definition          jsonb       NOT NULL,
    source_commit       text        NOT NULL,
    synced_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_workflows_status ON projection.workflows (status);
CREATE INDEX idx_workflows_id ON projection.workflows (workflow_id);
CREATE INDEX idx_workflows_applies_to ON projection.workflows USING gin (applies_to);

-- §3.4 projection.sync_state
CREATE TABLE projection.sync_state (
    id                  text        PRIMARY KEY DEFAULT 'global',
    last_synced_commit  text        NOT NULL,
    last_synced_at      timestamptz NOT NULL,
    status              text        NOT NULL DEFAULT 'idle',
    error_detail        text
);

-- ════════════════════════════════════════════════════════════════════════════
-- RUNTIME SCHEMA
-- ════════════════════════════════════════════════════════════════════════════

-- §4.1 runtime.runs
CREATE TABLE runtime.runs (
    run_id              text        PRIMARY KEY,
    task_path           text        NOT NULL,
    workflow_path       text        NOT NULL,
    workflow_id         text        NOT NULL,
    workflow_version    text        NOT NULL,
    workflow_version_label text,
    status              text        NOT NULL DEFAULT 'pending',
    current_step_id     text,
    trace_id            text        NOT NULL,
    started_at          timestamptz,
    completed_at        timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT runs_status_check CHECK (status IN ('pending', 'active', 'paused', 'committing', 'completed', 'failed', 'cancelled'))
);

CREATE INDEX idx_runs_task_path ON runtime.runs (task_path);
CREATE INDEX idx_runs_status ON runtime.runs (status);
CREATE INDEX idx_runs_trace_id ON runtime.runs (trace_id);
CREATE INDEX idx_runs_workflow_path ON runtime.runs (workflow_path);
CREATE INDEX idx_runs_created_at ON runtime.runs (created_at);

-- §4.2 runtime.step_executions
CREATE TABLE runtime.step_executions (
    execution_id        text        PRIMARY KEY,
    run_id              text        NOT NULL REFERENCES runtime.runs(run_id),
    step_id             text        NOT NULL,
    branch_id           text,
    actor_id            text,
    status              text        NOT NULL DEFAULT 'waiting',
    attempt             integer     NOT NULL DEFAULT 1,
    outcome_id          text,
    started_at          timestamptz,
    completed_at        timestamptz,
    error_detail        jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT step_status_check CHECK (status IN ('waiting', 'assigned', 'in_progress', 'blocked', 'completed', 'failed', 'skipped'))
);

CREATE INDEX idx_step_exec_run_id ON runtime.step_executions (run_id);
CREATE INDEX idx_step_exec_status ON runtime.step_executions (status);
CREATE INDEX idx_step_exec_actor_id ON runtime.step_executions (actor_id);
CREATE INDEX idx_step_exec_run_step ON runtime.step_executions (run_id, step_id);
CREATE INDEX idx_step_exec_branch ON runtime.step_executions (run_id, branch_id);

-- §4.3 runtime.divergence_contexts
CREATE TABLE runtime.divergence_contexts (
    divergence_id       text        NOT NULL,
    run_id              text        NOT NULL REFERENCES runtime.runs(run_id),
    status              text        NOT NULL DEFAULT 'pending',
    divergence_mode     text        NOT NULL,
    divergence_window   text        DEFAULT 'open',
    convergence_id      text,
    triggered_at        timestamptz,
    resolved_at         timestamptz,

    PRIMARY KEY (run_id, divergence_id),
    CONSTRAINT div_status_check CHECK (status IN ('pending', 'active', 'converging', 'resolved', 'failed')),
    CONSTRAINT div_mode_check CHECK (divergence_mode IN ('structured', 'exploratory'))
);

-- §4.4 runtime.branches
CREATE TABLE runtime.branches (
    branch_id           text        NOT NULL,
    run_id              text        NOT NULL,
    divergence_id       text        NOT NULL,
    status              text        NOT NULL DEFAULT 'pending',
    current_step_id     text,
    outcome             jsonb,
    artifacts_produced  jsonb       DEFAULT '[]',
    created_at          timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz,

    PRIMARY KEY (run_id, divergence_id, branch_id),
    FOREIGN KEY (run_id, divergence_id) REFERENCES runtime.divergence_contexts(run_id, divergence_id),
    CONSTRAINT branch_status_check CHECK (status IN ('pending', 'in_progress', 'completed', 'failed'))
);

-- §4.5 runtime.convergence_results
CREATE TABLE runtime.convergence_results (
    run_id              text        NOT NULL,
    divergence_id       text        NOT NULL,
    convergence_id      text,
    strategy_applied    text        NOT NULL,
    entry_policy_applied text       NOT NULL,
    selected_branch     text,
    selected_branches   jsonb       DEFAULT '[]',
    merged_artifact     text,
    experiment_artifact text,
    evaluator_actor_id  text,
    rationale           text,
    evaluated_at        timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (run_id, divergence_id),
    FOREIGN KEY (run_id, divergence_id) REFERENCES runtime.divergence_contexts(run_id, divergence_id)
);

-- §4.6 runtime.queue_entries
CREATE TABLE runtime.queue_entries (
    entry_id            text        PRIMARY KEY,
    entry_type          text        NOT NULL,
    payload             jsonb       NOT NULL,
    status              text        NOT NULL DEFAULT 'pending',
    idempotency_key     text        UNIQUE,
    priority            integer     NOT NULL DEFAULT 0,
    max_attempts        integer     NOT NULL DEFAULT 3,
    attempt_count       integer     NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    scheduled_at        timestamptz NOT NULL DEFAULT now(),
    processing_at       timestamptz,
    completed_at        timestamptz,
    error_detail        jsonb,

    CONSTRAINT queue_status_check CHECK (status IN ('pending', 'processing', 'completed', 'failed', 'dead_letter'))
);

CREATE INDEX idx_queue_status_scheduled ON runtime.queue_entries (status, scheduled_at)
    WHERE status = 'pending';
CREATE INDEX idx_queue_idempotency ON runtime.queue_entries (idempotency_key);
CREATE INDEX idx_queue_type ON runtime.queue_entries (entry_type);

-- §4.7 runtime.actor_assignments
CREATE TABLE runtime.actor_assignments (
    assignment_id       text        PRIMARY KEY,
    run_id              text        NOT NULL REFERENCES runtime.runs(run_id),
    execution_id        text        NOT NULL REFERENCES runtime.step_executions(execution_id),
    actor_id            text        NOT NULL,
    status              text        NOT NULL DEFAULT 'active',
    assigned_at         timestamptz NOT NULL DEFAULT now(),
    responded_at        timestamptz,
    timeout_at          timestamptz,

    CONSTRAINT assignment_status_check CHECK (status IN ('active', 'completed', 'cancelled', 'timed_out'))
);

CREATE UNIQUE INDEX idx_assignments_active_execution
    ON runtime.actor_assignments (execution_id)
    WHERE status = 'active';

CREATE INDEX idx_assignments_actor ON runtime.actor_assignments (actor_id, status);
CREATE INDEX idx_assignments_run ON runtime.actor_assignments (run_id);
CREATE INDEX idx_assignments_timeout ON runtime.actor_assignments (timeout_at)
    WHERE status = 'active';

-- Record this migration
INSERT INTO public.schema_migrations (version) VALUES ('001_initial_schema');
