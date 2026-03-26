-- Discussion threads and comments tables
-- Per architecture/discussion-model.md §3

CREATE TABLE runtime.discussion_threads (
    thread_id           text        PRIMARY KEY,
    anchor_type         text        NOT NULL,
    anchor_id           text        NOT NULL,
    topic_key           text,
    title               text,
    status              text        NOT NULL DEFAULT 'open',
    created_by          text        NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    resolved_at         timestamptz,
    resolution_type     text,
    resolution_refs     jsonb       DEFAULT '[]',

    CONSTRAINT thread_status_check CHECK (status IN ('open', 'resolved', 'archived')),
    CONSTRAINT thread_anchor_check CHECK (anchor_type IN ('artifact', 'run', 'step_execution', 'divergence_context'))
);

CREATE INDEX idx_threads_anchor ON runtime.discussion_threads (anchor_type, anchor_id);
CREATE INDEX idx_threads_status ON runtime.discussion_threads (status);
CREATE INDEX idx_threads_created_by ON runtime.discussion_threads (created_by);
CREATE UNIQUE INDEX idx_threads_topic_key ON runtime.discussion_threads (anchor_type, anchor_id, topic_key)
    WHERE topic_key IS NOT NULL;

CREATE TABLE runtime.comments (
    comment_id          text        PRIMARY KEY,
    thread_id           text        NOT NULL REFERENCES runtime.discussion_threads(thread_id),
    parent_comment_id   text        REFERENCES runtime.comments(comment_id),
    author_id           text        NOT NULL,
    author_type         text        NOT NULL,
    content             text        NOT NULL,
    metadata            jsonb       DEFAULT '{}',
    created_at          timestamptz NOT NULL DEFAULT now(),
    edited_at           timestamptz,
    deleted             boolean     NOT NULL DEFAULT false
);

CREATE INDEX idx_comments_thread ON runtime.comments (thread_id, created_at);
CREATE INDEX idx_comments_author ON runtime.comments (author_id);
CREATE INDEX idx_comments_parent ON runtime.comments (parent_comment_id);

INSERT INTO public.schema_migrations (version) VALUES ('007_discussion_tables');
