-- Persist the repository set a run is responsible for (INIT-014 EPIC-004
-- TASK-001).
--
-- A run is now multi-repo aware: it records every repository ID it will
-- touch (affected_repositories), whether the workspace primary repo is
-- one of them (primary_repository), and an optional per-repo branch map
-- for future recovery scenarios where the shared run branch is not
-- present in every affected repository (e.g. partial branch-creation
-- cleanup, manual resolution retries).
--
-- Defaults match the missing-metadata fallback the engine helper uses
-- (`AffectedRepositoriesForTask` returns `[spine]` for a task with no
-- `repositories` field). Backfilling existing rows with the same value
-- means pre-migration rows read back as primary-repo-only without any
-- explicit data migration, which is what they actually were.

ALTER TABLE runtime.runs
    ADD COLUMN IF NOT EXISTS affected_repositories text[]   NOT NULL DEFAULT ARRAY['spine'],
    ADD COLUMN IF NOT EXISTS primary_repository    boolean  NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS repository_branches   jsonb    NOT NULL DEFAULT '{}'::jsonb;

INSERT INTO public.schema_migrations (version) VALUES ('021_add_run_affected_repositories');
