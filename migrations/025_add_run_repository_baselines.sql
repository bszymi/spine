-- Persist the per-repository SHA each run's run branch was cut from
-- (INIT-014 EPIC-004 TASK-005).
--
-- Per ADR-015 + Multi-Repository Integration §5, every step assignment
-- carries a `commit_baseline` field — the SHA the resolved repository's
-- run branch was cut from. We capture that SHA in the engine when
-- creating run branches (parent default-branch tip just before
-- CreateBranch lands the new ref) and store it on the run row so
-- subsequent assignment payloads, retries, and recovery paths surface
-- the same baseline.
--
-- Backfill: pre-migration rows have an empty map. The DB DEFAULT
-- '{}'::jsonb preserves that semantics — pre-migration runs publish
-- assignment payloads without `commit_baseline`, which is contracted
-- as `omitempty` on the AssignmentContext. New runs populate the
-- column at branch creation time.

ALTER TABLE runtime.runs
    ADD COLUMN IF NOT EXISTS repository_baselines jsonb NOT NULL DEFAULT '{}'::jsonb;

INSERT INTO public.schema_migrations (version) VALUES ('025_add_run_repository_baselines');
