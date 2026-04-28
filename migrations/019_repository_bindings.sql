-- Runtime repository binding rows for code repositories
-- (INIT-014 EPIC-001 TASK-002, ADR-013).
--
-- Each row holds the operational connection details for a code
-- repository registered in the workspace catalog at
-- /.spine/repositories.yaml. The catalog is the source of truth
-- for identity (id, kind, default_branch); this table is the
-- source of truth for how to reach the clone.
--
-- The primary "spine" repository has no row. It is resolved
-- virtually from the existing workspace state (RepoPath and the
-- configured authoritative branch). Backward compatibility for
-- single-repo workspaces depends on this — see EPIC-001 TASK-006.

CREATE TABLE runtime.repositories (
    repository_id     text        NOT NULL,
    workspace_id      text        NOT NULL,
    clone_url         text        NOT NULL,
    credentials_ref   text        NULL,
    local_path        text        NOT NULL,
    default_branch    text        NULL,
    status            text        NOT NULL DEFAULT 'active',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (workspace_id, repository_id),
    CHECK (status IN ('active', 'inactive')),
    CHECK (repository_id <> 'spine')
);

CREATE INDEX idx_repositories_workspace_status
    ON runtime.repositories (workspace_id, status);

INSERT INTO public.schema_migrations (version) VALUES ('019_repository_bindings');
