-- Per-repository merge outcomes (INIT-014 EPIC-005 TASK-001).
--
-- Spine does not pretend cross-repo merges are atomic. Each affected
-- repository records its merge outcome independently — pending,
-- merged, failed (with classification), skipped, or resolved
-- externally — so partial merge states are explicit and queryable
-- rather than inferred from prose. The shape mirrors
-- internal/domain/repository_merge_outcome.go: callers should treat
-- domain.RepositoryMergeOutcome as the source of truth and this
-- table as the persistence projection.
--
-- Identity is (run_id, repository_id). Outcomes are scoped by run, so
-- the foreign key cascades on run delete — a removed run cannot leave
-- orphaned merge rows around. The status / failure_class CHECK
-- constraints mirror domain.ValidRepositoryMergeStatuses() and
-- ValidMergeFailureClasses(); a SQL surface check stops malformed
-- data even when a future direct-write path bypasses the Go
-- validator.
--
-- ledger_commit_sha is the governance-ledger commit SHA, only
-- meaningful on the primary repo. The CHECK constraint mirrors the
-- domain validator so neither layer can drift from the other.

CREATE TABLE runtime.repository_merge_outcomes (
    run_id              text        NOT NULL,
    repository_id       text        NOT NULL,
    status              text        NOT NULL DEFAULT 'pending',
    source_branch       text        NOT NULL,
    target_branch       text        NOT NULL,
    merge_commit_sha    text        NULL,
    ledger_commit_sha   text        NULL,
    failure_class       text        NULL,
    failure_detail      text        NULL,
    resolved_by         text        NULL,
    resolution_reason   text        NULL,
    attempts            integer     NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    merged_at           timestamptz NULL,
    last_attempted_at   timestamptz NULL,

    PRIMARY KEY (run_id, repository_id),
    FOREIGN KEY (run_id) REFERENCES runtime.runs (run_id) ON DELETE CASCADE,
    CHECK (status IN ('pending', 'merged', 'failed', 'skipped', 'resolved-externally')),
    CHECK (
        failure_class IS NULL OR failure_class IN (
            'unknown', 'merge_conflict', 'branch_protection',
            'precondition', 'auth', 'network', 'remote_unavailable'
        )
    ),
    CHECK (attempts >= 0),
    -- Required text columns must be non-empty. The Go validator
    -- rejects empty strings, but a direct INSERT bypasses Go — these
    -- CHECKs mirror that invariant so empty strings cannot slip in.
    -- Optional columns where empty has a meaning ("not set") use the
    -- presence/absence (IS NULL) checks below; those columns are
    -- persisted via nilIfEmpty so empty strings never reach SQL from
    -- the upsert path.
    CHECK (run_id <> '' AND repository_id <> ''),
    CHECK (source_branch <> '' AND target_branch <> ''),
    CHECK (merge_commit_sha IS NULL OR merge_commit_sha <> ''),
    CHECK (ledger_commit_sha IS NULL OR ledger_commit_sha <> ''),
    CHECK (failure_detail IS NULL OR failure_detail <> ''),
    CHECK (resolved_by IS NULL OR resolved_by <> ''),
    CHECK (resolution_reason IS NULL OR resolution_reason <> ''),
    -- Only the primary repo may carry a ledger commit SHA. Mirrors
    -- domain.RepositoryMergeOutcome.Validate.
    CHECK (ledger_commit_sha IS NULL OR repository_id = 'spine'),
    -- Status-conditional invariants. The Go validator owns the
    -- canonical version (domain.RepositoryMergeOutcome.Validate); these
    -- CHECK constraints are a SQL-side backstop so a direct INSERT
    -- (migration, manual psql, future code path) cannot persist a row
    -- that the read path would surface to dashboards as a malformed
    -- mix (e.g. resolved row carrying stale failure fields).
    CHECK (
        CASE status
            WHEN 'merged' THEN
                merge_commit_sha IS NOT NULL
                AND merged_at IS NOT NULL
                AND failure_class IS NULL
                AND failure_detail IS NULL
            WHEN 'failed' THEN
                failure_class IS NOT NULL
                AND merge_commit_sha IS NULL
                AND merged_at IS NULL
            WHEN 'pending' THEN
                merge_commit_sha IS NULL
                AND failure_class IS NULL
                AND failure_detail IS NULL
                AND merged_at IS NULL
            WHEN 'skipped' THEN
                merge_commit_sha IS NULL
                AND failure_class IS NULL
                AND failure_detail IS NULL
                AND merged_at IS NULL
            WHEN 'resolved-externally' THEN
                resolved_by IS NOT NULL
                AND resolution_reason IS NOT NULL
                AND failure_class IS NULL
                AND failure_detail IS NULL
                AND merge_commit_sha IS NULL
                AND merged_at IS NULL
            ELSE TRUE
        END
    )
);

-- Per-run lookup is the dominant access pattern (status dashboards,
-- recovery planners). The primary-key index already covers prefix
-- lookups on run_id, so an extra index would be redundant.

INSERT INTO public.schema_migrations (version) VALUES ('022_repository_merge_outcomes');
