-- Add branch_name column to runtime.runs for branch-per-run strategy.
-- Per EPIC-004: Git Orchestration Layer.

ALTER TABLE runtime.runs ADD COLUMN IF NOT EXISTS branch_name text;

CREATE INDEX IF NOT EXISTS idx_runs_branch_name ON runtime.runs (branch_name)
    WHERE branch_name IS NOT NULL;

INSERT INTO public.schema_migrations (version) VALUES ('002_add_branch_name');
