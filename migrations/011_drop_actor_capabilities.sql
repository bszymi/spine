-- Remove legacy capabilities column from actors table.
-- Per INIT-010 EPIC-001 TASK-006/TASK-007.
-- Before dropping, backfill existing capability strings into the skill system.

-- Step 1: Create skill entities from distinct capability values across all actors.
-- Uses jsonb_array_elements_text to extract strings from the capabilities JSONB array.
-- ON CONFLICT DO NOTHING ensures idempotency if skills already exist.
INSERT INTO auth.skills (skill_id, name, description, category, status)
SELECT
    'migrated-' || md5(cap) AS skill_id,
    cap AS name,
    'Migrated from legacy capabilities' AS description,
    'migrated' AS category,
    'active' AS status
FROM (
    SELECT DISTINCT jsonb_array_elements_text(capabilities) AS cap
    FROM auth.actors
    WHERE capabilities IS NOT NULL AND capabilities != '[]'::jsonb
) caps
ON CONFLICT (name) DO NOTHING;

-- Step 2: Create actor-skill associations for all existing capabilities.
-- ON CONFLICT DO NOTHING ensures idempotency if associations already exist.
INSERT INTO auth.actor_skills (actor_id, skill_id)
SELECT
    a.actor_id,
    s.skill_id
FROM auth.actors a,
     jsonb_array_elements_text(a.capabilities) AS cap
JOIN auth.skills s ON s.name = cap
WHERE a.capabilities IS NOT NULL AND a.capabilities != '[]'::jsonb
ON CONFLICT (actor_id, skill_id) DO NOTHING;

-- Step 3: Drop the legacy column now that data is preserved.
ALTER TABLE auth.actors DROP COLUMN IF EXISTS capabilities;

INSERT INTO public.schema_migrations (version) VALUES ('011_drop_actor_capabilities');
