-- Event delivery queue and log tables for external event delivery.
-- Per INIT-013 EPIC-001 TASK-002: Delivery Queue Database Schema.

CREATE TABLE runtime.event_delivery_queue (
    delivery_id         text        PRIMARY KEY,
    subscription_id     text        NOT NULL,
    event_id            text        NOT NULL,
    event_type          text        NOT NULL,
    payload             jsonb       NOT NULL,
    status              text        NOT NULL DEFAULT 'pending',
    attempt_count       integer     NOT NULL DEFAULT 0,
    next_retry_at       timestamptz,
    last_error          text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    delivered_at        timestamptz,

    CONSTRAINT delivery_status_check CHECK (status IN ('pending', 'delivering', 'delivered', 'failed', 'dead'))
);

-- Dispatcher reads: find pending/failed entries ready for delivery
CREATE INDEX idx_delivery_queue_status_retry ON runtime.event_delivery_queue (status, next_retry_at)
    WHERE status IN ('pending', 'failed');

-- Per-subscription queue depth
CREATE INDEX idx_delivery_queue_subscription ON runtime.event_delivery_queue (subscription_id, created_at);

-- Idempotency: prevent duplicate deliveries for the same event+subscription
CREATE UNIQUE INDEX idx_delivery_queue_idempotent ON runtime.event_delivery_queue (subscription_id, event_id);

CREATE TABLE runtime.event_delivery_log (
    log_id              text        PRIMARY KEY,
    delivery_id         text        NOT NULL REFERENCES runtime.event_delivery_queue(delivery_id),
    subscription_id     text        NOT NULL,
    event_id            text        NOT NULL,
    status_code         integer,
    duration_ms         integer,
    error               text,
    created_at          timestamptz NOT NULL DEFAULT now()
);

-- Per-subscription delivery history
CREATE INDEX idx_delivery_log_subscription ON runtime.event_delivery_log (subscription_id, created_at);

-- Look up delivery attempts for a specific delivery
CREATE INDEX idx_delivery_log_delivery ON runtime.event_delivery_log (delivery_id);

INSERT INTO public.schema_migrations (version) VALUES ('015_event_delivery_tables');
