-- Event subscription configuration table.
-- Per INIT-013 EPIC-002 TASK-001: Subscription Data Model and Database Schema.

CREATE TABLE runtime.event_subscriptions (
    subscription_id     text        PRIMARY KEY,
    workspace_id        text,
    name                text        NOT NULL,
    target_type         text        NOT NULL DEFAULT 'webhook',
    target_url          text        NOT NULL,
    event_types         text[]      NOT NULL DEFAULT '{}',
    signing_secret      text        NOT NULL DEFAULT '',
    status              text        NOT NULL DEFAULT 'active',
    metadata            jsonb       NOT NULL DEFAULT '{}',
    created_by          text        NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT subscription_status_check CHECK (status IN ('active', 'paused', 'disabled')),
    CONSTRAINT subscription_target_type_check CHECK (target_type IN ('webhook'))
);

-- Unique name per workspace (null workspace_id = internal/system subscription)
CREATE UNIQUE INDEX idx_subscriptions_workspace_name ON runtime.event_subscriptions (COALESCE(workspace_id, ''), name);

-- Look up active subscriptions for delivery fan-out
CREATE INDEX idx_subscriptions_status ON runtime.event_subscriptions (status) WHERE status = 'active';

INSERT INTO public.schema_migrations (version) VALUES ('016_event_subscriptions');
