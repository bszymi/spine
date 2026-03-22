-- Divergence and branch tables for parallel execution.
-- Per Engine State Machine §4-5.

CREATE TABLE runtime.divergence_contexts (
    divergence_id     text        PRIMARY KEY,
    run_id            text        NOT NULL REFERENCES runtime.runs(run_id),
    status            text        NOT NULL DEFAULT 'pending',
    divergence_mode   text        NOT NULL,
    divergence_window text        NOT NULL DEFAULT 'closed',
    convergence_id    text,
    triggered_at      timestamptz,
    resolved_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT div_status_check CHECK (status IN ('pending', 'active', 'converging', 'resolved', 'failed')),
    CONSTRAINT div_mode_check CHECK (divergence_mode IN ('structured', 'exploratory')),
    CONSTRAINT div_window_check CHECK (divergence_window IN ('open', 'closed'))
);

CREATE INDEX idx_div_ctx_run ON runtime.divergence_contexts (run_id);

CREATE TABLE runtime.branches (
    branch_id         text        PRIMARY KEY,
    run_id            text        NOT NULL,
    divergence_id     text        NOT NULL REFERENCES runtime.divergence_contexts(divergence_id),
    status            text        NOT NULL DEFAULT 'pending',
    current_step_id   text,
    outcome           jsonb,
    artifacts_produced jsonb      NOT NULL DEFAULT '[]',
    created_at        timestamptz NOT NULL DEFAULT now(),
    completed_at      timestamptz,

    CONSTRAINT branch_status_check CHECK (status IN ('pending', 'in_progress', 'completed', 'failed'))
);

CREATE INDEX idx_branches_div ON runtime.branches (divergence_id);

INSERT INTO public.schema_migrations (version) VALUES ('003_divergence_tables');
