-- Add min/max branch limits to divergence contexts for exploratory divergence.
ALTER TABLE runtime.divergence_contexts ADD COLUMN min_branches integer NOT NULL DEFAULT 0;
ALTER TABLE runtime.divergence_contexts ADD COLUMN max_branches integer NOT NULL DEFAULT 0;
