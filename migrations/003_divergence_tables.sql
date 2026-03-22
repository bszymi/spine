-- Divergence tables already exist in 001_initial_schema.sql.
-- This migration is a no-op marker for tracking purposes.

INSERT INTO public.schema_migrations (version) VALUES ('003_divergence_tables')
ON CONFLICT DO NOTHING;
