-- Project task repository bindings into the artifact and execution
-- projections (INIT-014 EPIC-002 TASK-002).
--
-- The repositories list a Task declares in front matter is part of
-- task intent (EPIC-002 TASK-001). Reading it back through the
-- artifact and execution surfaces requires it to live in the
-- projection store, not be reparsed from Markdown on every query.
--
-- Adds a `repositories` JSONB column to both projection tables. Each
-- row is a JSON array of repository ID strings (e.g.
-- ["payments-service","api-gateway"]). The default is an empty array
-- so existing rows projected before this migration — and tasks with
-- no repositories field — round-trip as the primary-repo-only case
-- without any data backfill.
--
-- An expression index on repositories supports filter queries that
-- need to find tasks targeting a specific repository ID. The index
-- uses jsonb_path_ops to keep size bounded; callers query with @>
-- containment (e.g. WHERE repositories @> '["payments-service"]'::jsonb).

ALTER TABLE projection.artifacts
    ADD COLUMN repositories jsonb NOT NULL DEFAULT '[]';

ALTER TABLE projection.execution_projections
    ADD COLUMN repositories jsonb NOT NULL DEFAULT '[]';

CREATE INDEX idx_exec_proj_repositories
    ON projection.execution_projections USING gin (repositories jsonb_path_ops);

INSERT INTO public.schema_migrations (version) VALUES ('020_task_repositories');
