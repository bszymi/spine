-- Workspace Registry
-- Per data-model.md §7.2 — workspace registry for shared runtime mode.
--
-- IMPORTANT: This migration runs against the REGISTRY database
-- (SPINE_REGISTRY_DATABASE_URL), NOT against workspace databases.
-- Workspace databases use migrations in the parent migrations/ directory.

CREATE TABLE IF NOT EXISTS public.workspace_registry (
    workspace_id  text        PRIMARY KEY,
    display_name  text        NOT NULL,
    database_url  text        NOT NULL,
    repo_path     text        NOT NULL,
    actor_scope   text        NOT NULL DEFAULT '',
    status        text        NOT NULL DEFAULT 'active',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_registry_status
    ON public.workspace_registry (status);
