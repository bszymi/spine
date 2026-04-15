-- Dedicated event log for pull API and SSE replay.
-- Stores one row per emitted event (deduplicated by event_id),
-- separate from the per-subscription delivery queue.
-- Per INIT-013 codex review fix.

CREATE TABLE runtime.event_log (
    event_id    text        PRIMARY KEY,
    event_type  text        NOT NULL,
    payload     jsonb       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_event_log_created ON runtime.event_log (created_at);
CREATE INDEX idx_event_log_type_created ON runtime.event_log (event_type, created_at);

INSERT INTO public.schema_migrations (version) VALUES ('017_event_log');
