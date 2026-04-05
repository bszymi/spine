-- Actor-skill junction table for many-to-many association.
-- Per INIT-010 EPIC-001 TASK-002: Actor Skill Assignment.

CREATE TABLE auth.actor_skills (
    actor_id    text        NOT NULL REFERENCES auth.actors(actor_id),
    skill_id    text        NOT NULL REFERENCES auth.skills(skill_id),
    assigned_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (actor_id, skill_id)
);

CREATE INDEX idx_actor_skills_skill ON auth.actor_skills (skill_id);

INSERT INTO public.schema_migrations (version) VALUES ('010_actor_skills_table');
