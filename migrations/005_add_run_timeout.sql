-- Add timeout_at column to runs for run-level timeout enforcement.
ALTER TABLE runtime.runs ADD COLUMN timeout_at timestamptz;

CREATE INDEX idx_runs_timeout_at ON runtime.runs (timeout_at)
    WHERE status IN ('active', 'paused') AND timeout_at IS NOT NULL;
