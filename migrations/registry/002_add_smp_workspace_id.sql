-- Add SMP workspace ID column for credential helper integration.
-- In shared mode, each workspace can have a management platform workspace ID
-- that is passed to the credential helper during git push.

ALTER TABLE public.workspace_registry
    ADD COLUMN IF NOT EXISTS smp_workspace_id text NOT NULL DEFAULT '';
