-- Add eligible_actor_ids to step_executions.
-- When non-empty, only the listed actors may claim the step.
-- Empty array (default) preserves backward compatibility: any eligible actor type may claim.

ALTER TABLE runtime.step_executions
    ADD COLUMN IF NOT EXISTS eligible_actor_ids text[] NOT NULL DEFAULT '{}';
