-- Add mode column to runtime.runs for planning run support.
-- Per ADR-006: Planning Runs for Governed Artifact Creation.

ALTER TABLE runtime.runs ADD COLUMN IF NOT EXISTS mode text NOT NULL DEFAULT 'standard';

ALTER TABLE runtime.runs ADD CONSTRAINT runs_mode_check
    CHECK (mode IN ('standard', 'planning'));

CREATE INDEX IF NOT EXISTS idx_runs_mode_planning ON runtime.runs (mode)
    WHERE mode != 'standard';

INSERT INTO public.schema_migrations (version) VALUES ('008_add_run_mode');
