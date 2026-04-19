-- Branch-protection rule projection table.
-- Per ADR-009 §1 and INIT-018 EPIC-002 TASK-003.
-- Mirrors /.spine/branch-protection.yaml so the policy hot-path in
-- internal/branchprotect reads the projection instead of hitting Git.

CREATE TABLE projection.branch_protection_rules (
    branch_pattern  text        PRIMARY KEY,
    rule_order      integer     NOT NULL,
    protections     jsonb       NOT NULL,
    source_commit   text        NOT NULL DEFAULT '',
    synced_at       timestamptz NOT NULL DEFAULT now()
);

-- rule_order preserves the source-file ordering so MatchRules returns
-- matches in the author's intended sequence (the existing config API
-- contract). Indexed because reads sort by it.
CREATE INDEX idx_bpr_rule_order ON projection.branch_protection_rules (rule_order);

-- Seed the bootstrap defaults (ADR-009 §1): `main` is protected with
-- no-delete + no-direct-write even before a workspace authors its own
-- branch-protection.yaml. source_commit='bootstrap' distinguishes these
-- seed rows from projected rows — the projection handler replaces both
-- kinds atomically on each sync.
INSERT INTO projection.branch_protection_rules (branch_pattern, rule_order, protections, source_commit)
VALUES ('main', 0, '["no-delete","no-direct-write"]', 'bootstrap');

INSERT INTO public.schema_migrations (version) VALUES ('018_branch_protection_rules');
