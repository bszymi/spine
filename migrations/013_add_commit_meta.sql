-- Add commit_meta column to store workflow outcome commit metadata (e.g., status: Pending).
-- Used by MergeRunBranch to rewrite artifact frontmatter before merging to main.
ALTER TABLE runtime.runs ADD COLUMN commit_meta jsonb;
