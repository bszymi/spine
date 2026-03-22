-- Auth schema for actor and token management.
-- Per Security Model §3-4.

CREATE SCHEMA IF NOT EXISTS auth;

CREATE TABLE auth.actors (
    actor_id    text        PRIMARY KEY,
    actor_type  text        NOT NULL,
    name        text        NOT NULL,
    role        text        NOT NULL,
    capabilities jsonb      NOT NULL DEFAULT '[]',
    status      text        NOT NULL DEFAULT 'active',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT actor_type_check CHECK (actor_type IN ('human', 'ai_agent', 'automated_system')),
    CONSTRAINT actor_role_check CHECK (role IN ('reader', 'contributor', 'reviewer', 'operator', 'admin')),
    CONSTRAINT actor_status_check CHECK (status IN ('active', 'suspended', 'deactivated'))
);

CREATE TABLE auth.tokens (
    token_id    text        PRIMARY KEY,
    actor_id    text        NOT NULL REFERENCES auth.actors(actor_id),
    token_hash  text        NOT NULL UNIQUE,
    name        text        NOT NULL DEFAULT '',
    expires_at  timestamptz,
    revoked_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_tokens_actor ON auth.tokens (actor_id);

INSERT INTO public.schema_migrations (version) VALUES ('002_auth_tables');
