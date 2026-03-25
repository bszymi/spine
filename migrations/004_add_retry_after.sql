-- Add retry_after column to step_executions for tracking backoff delays.
ALTER TABLE runtime.step_executions ADD COLUMN retry_after timestamptz;

CREATE INDEX idx_step_exec_retry_after ON runtime.step_executions (retry_after)
    WHERE status = 'waiting' AND retry_after IS NOT NULL;
