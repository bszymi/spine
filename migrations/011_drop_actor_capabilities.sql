-- Remove legacy capabilities column from actors table.
-- Per INIT-010 EPIC-001 TASK-006: Remove legacy capabilities field.
-- Capabilities are now managed through the skill system (auth.skills + auth.actor_skills).

ALTER TABLE auth.actors DROP COLUMN IF EXISTS capabilities;

INSERT INTO public.schema_migrations (version) VALUES ('011_drop_actor_capabilities');
