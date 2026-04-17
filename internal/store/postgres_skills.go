package store

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
)

// ── Skills ──

func (s *PostgresStore) CreateSkill(ctx context.Context, skill *domain.Skill) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth.skills (skill_id, name, description, category, status)
		VALUES ($1, $2, $3, $4, $5)`,
		skill.SkillID, skill.Name, skill.Description, skill.Category, skill.Status,
	)
	return err
}

func (s *PostgresStore) GetSkill(ctx context.Context, skillID string) (*domain.Skill, error) {
	var skill domain.Skill
	err := s.pool.QueryRow(ctx, `
		SELECT skill_id, name, description, category, status, created_at, updated_at
		FROM auth.skills WHERE skill_id = $1`, skillID,
	).Scan(&skill.SkillID, &skill.Name, &skill.Description, &skill.Category, &skill.Status, &skill.CreatedAt, &skill.UpdatedAt)
	if err != nil {
		return nil, notFoundOr(err, "skill not found")
	}
	return &skill, nil
}

func (s *PostgresStore) UpdateSkill(ctx context.Context, skill *domain.Skill) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE auth.skills SET name = $1, description = $2, category = $3, status = $4, updated_at = now()
		WHERE skill_id = $5`,
		skill.Name, skill.Description, skill.Category, skill.Status, skill.SkillID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "skill not found")
}

func (s *PostgresStore) ListSkills(ctx context.Context) ([]domain.Skill, error) {
	return s.listSkillsQuery(ctx, `
		SELECT skill_id, name, description, category, status, created_at, updated_at
		FROM auth.skills ORDER BY name`)
}

func (s *PostgresStore) ListSkillsByCategory(ctx context.Context, category string) ([]domain.Skill, error) {
	return s.listSkillsQuery(ctx, `
		SELECT skill_id, name, description, category, status, created_at, updated_at
		FROM auth.skills WHERE category = $1 ORDER BY name`, category)
}

func (s *PostgresStore) listSkillsQuery(ctx context.Context, query string, args ...any) ([]domain.Skill, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var skills []domain.Skill
	for rows.Next() {
		var skill domain.Skill
		if err := rows.Scan(&skill.SkillID, &skill.Name, &skill.Description, &skill.Category, &skill.Status, &skill.CreatedAt, &skill.UpdatedAt); err != nil {
			return nil, err
		}
		skills = append(skills, skill)
	}
	return skills, rows.Err()
}

// ── Actor-Skill Associations ──

func (s *PostgresStore) AddSkillToActor(ctx context.Context, actorID, skillID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth.actor_skills (actor_id, skill_id)
		VALUES ($1, $2)
		ON CONFLICT (actor_id, skill_id) DO NOTHING`,
		actorID, skillID,
	)
	return err
}

func (s *PostgresStore) RemoveSkillFromActor(ctx context.Context, actorID, skillID string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM auth.actor_skills
		WHERE actor_id = $1 AND skill_id = $2`,
		actorID, skillID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "actor-skill assignment not found")
}

func (s *PostgresStore) ListActorSkills(ctx context.Context, actorID string) ([]domain.Skill, error) {
	return s.listSkillsQuery(ctx, `
		SELECT s.skill_id, s.name, s.description, s.category, s.status, s.created_at, s.updated_at
		FROM auth.skills s
		JOIN auth.actor_skills as_ ON s.skill_id = as_.skill_id
		WHERE as_.actor_id = $1
		ORDER BY s.name`, actorID)
}

func (s *PostgresStore) ListActorsBySkills(ctx context.Context, skillNames []string) ([]domain.Actor, error) {
	if len(skillNames) == 0 {
		return s.ListActorsByStatus(ctx, domain.ActorStatusActive)
	}

	// Find active actors possessing ALL specified skills (AND matching).
	// Uses a COUNT/HAVING pattern to require all skills are present.
	return s.listActorsQuery(ctx, `
		SELECT a.actor_id, a.actor_type, a.name, a.role, a.status
		FROM auth.actors a
		JOIN auth.actor_skills as_ ON a.actor_id = as_.actor_id
		JOIN auth.skills s ON as_.skill_id = s.skill_id
		WHERE a.status = 'active'
		  AND s.status = 'active'
		  AND s.name = ANY($1)
		GROUP BY a.actor_id, a.actor_type, a.name, a.role, a.status
		HAVING COUNT(DISTINCT s.name) = $2
		ORDER BY a.actor_id`, skillNames, len(skillNames))
}
