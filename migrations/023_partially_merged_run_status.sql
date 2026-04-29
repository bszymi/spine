-- Allow the new partially-merged run state (INIT-014 EPIC-005 TASK-003).
--
-- The runs.status CHECK constraint has been the single source of
-- truth for valid run-state strings since migration 001. EPIC-005
-- TASK-003 adds `partially-merged` as a non-terminal state for runs
-- whose primary repo merged but at least one affected code repo
-- ended in a permanent failure. Without this migration the state
-- transition would fail the INSERT/UPDATE at the database boundary
-- before reaching the engine's run-state machine.
--
-- The constraint is dropped and re-added rather than altered in
-- place because Postgres has no `ADD value` syntax for CHECK lists.
-- The new list keeps the existing terminals + the new state.

ALTER TABLE runtime.runs
    DROP CONSTRAINT IF EXISTS runs_status_check;

ALTER TABLE runtime.runs
    ADD CONSTRAINT runs_status_check
        CHECK (status IN (
            'pending',
            'active',
            'paused',
            'committing',
            'partially-merged',
            'completed',
            'failed',
            'cancelled'
        ));

INSERT INTO public.schema_migrations (version) VALUES ('023_partially_merged_run_status');
