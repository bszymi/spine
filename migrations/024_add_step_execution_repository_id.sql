-- Persist the resolved target repository on step executions (INIT-014
-- EPIC-004 TASK-004, governed by ADR-015).
--
-- Per ADR-015, every step execution targets exactly one repository,
-- resolved at activation as `step.repository` if set or `spine`
-- otherwise. We store the resolved value on the row so retries, audit
-- queries, and recovery paths do not have to re-resolve from the
-- workflow definition (which mirrors the ADR-013 pattern of recording
-- derived identity at the point of binding).
--
-- Backfill: pre-migration rows are implicitly primary-repo-only
-- (workflows on `main` as of this migration's date never declare
-- `step.repository`), so existing rows read back as `repository_id =
-- 'spine'`. The default + NOT NULL preserves that semantics without
-- needing an explicit data migration.

ALTER TABLE runtime.step_executions
    ADD COLUMN IF NOT EXISTS repository_id text NOT NULL DEFAULT 'spine';

INSERT INTO public.schema_migrations (version) VALUES ('024_add_step_execution_repository_id');
