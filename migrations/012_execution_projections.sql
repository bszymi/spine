-- Execution projection table for operational task discovery.
-- Per INIT-010 EPIC-004 TASK-001: Execution Projection Schema.
-- Combines artifact state with runtime execution state for efficient querying.

CREATE TABLE projection.execution_projections (
    task_path           text        PRIMARY KEY,
    task_id             text        NOT NULL,
    title               text        NOT NULL DEFAULT '',
    status              text        NOT NULL,
    required_skills     jsonb       NOT NULL DEFAULT '[]',
    allowed_actor_types jsonb       NOT NULL DEFAULT '[]',
    blocked             boolean     NOT NULL DEFAULT false,
    blocked_by          jsonb       NOT NULL DEFAULT '[]',
    assigned_actor_id   text,
    assignment_status   text        NOT NULL DEFAULT 'unassigned',
    run_id              text,
    workflow_step       text,
    last_updated        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT exec_proj_assignment_check CHECK (assignment_status IN ('unassigned', 'assigned', 'in_progress'))
);

CREATE INDEX idx_exec_proj_blocked ON projection.execution_projections (blocked);
CREATE INDEX idx_exec_proj_assignment ON projection.execution_projections (assignment_status);
CREATE INDEX idx_exec_proj_actor ON projection.execution_projections (assigned_actor_id) WHERE assigned_actor_id IS NOT NULL;
CREATE INDEX idx_exec_proj_status ON projection.execution_projections (status);

INSERT INTO public.schema_migrations (version) VALUES ('012_execution_projections');
