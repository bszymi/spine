-- Skills table for the formal skill system.
-- Per INIT-010 EPIC-001 TASK-001: Define Skill Domain Model.
-- Skills are workspace-scoped capabilities that formalize the existing
-- bare-string capabilities on actors.

CREATE TABLE auth.skills (
    skill_id    text        PRIMARY KEY,
    name        text        NOT NULL UNIQUE,
    description text        NOT NULL DEFAULT '',
    category    text        NOT NULL DEFAULT '',
    status      text        NOT NULL DEFAULT 'active',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT skill_status_check CHECK (status IN ('active', 'deprecated'))
);

CREATE INDEX idx_skills_name ON auth.skills (name);
CREATE INDEX idx_skills_category ON auth.skills (category);
CREATE INDEX idx_skills_status ON auth.skills (status);

INSERT INTO public.schema_migrations (version) VALUES ('009_skills_table');
