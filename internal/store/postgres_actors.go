package store

import (
	"context"
	"errors"

	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

// ── Actors ──

func (s *PostgresStore) GetActor(ctx context.Context, actorID string) (*domain.Actor, error) {
	var actor domain.Actor
	err := s.pool.QueryRow(ctx, `
		SELECT actor_id, actor_type, name, role, status
		FROM auth.actors WHERE actor_id = $1`, actorID,
	).Scan(&actor.ActorID, &actor.Type, &actor.Name, &actor.Role, &actor.Status)
	if err != nil {
		return nil, notFoundOr(err, "actor not found")
	}
	return &actor, nil
}

func (s *PostgresStore) CreateActor(ctx context.Context, actor *domain.Actor) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth.actors (actor_id, actor_type, name, role, status)
		VALUES ($1, $2, $3, $4, $5)`,
		actor.ActorID, actor.Type, actor.Name, actor.Role, actor.Status,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.NewError(domain.ErrConflict, "actor_id already exists")
		}
		return err
	}
	return nil
}

func (s *PostgresStore) UpdateActor(ctx context.Context, actor *domain.Actor) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE auth.actors SET name = $1, role = $2, status = $3, updated_at = now()
		WHERE actor_id = $4`,
		actor.Name, actor.Role, actor.Status, actor.ActorID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "actor not found")
}

func (s *PostgresStore) ListActors(ctx context.Context) ([]domain.Actor, error) {
	return s.listActorsQuery(ctx, `
		SELECT actor_id, actor_type, name, role, status
		FROM auth.actors ORDER BY actor_id`)
}

func (s *PostgresStore) ListActorsByStatus(ctx context.Context, status domain.ActorStatus) ([]domain.Actor, error) {
	return s.listActorsQuery(ctx, `
		SELECT actor_id, actor_type, name, role, status
		FROM auth.actors WHERE status = $1 ORDER BY actor_id`, status)
}

func (s *PostgresStore) listActorsQuery(ctx context.Context, query string, args ...any) ([]domain.Actor, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actors []domain.Actor
	for rows.Next() {
		var actor domain.Actor
		if err := rows.Scan(&actor.ActorID, &actor.Type, &actor.Name, &actor.Role, &actor.Status); err != nil {
			return nil, err
		}
		actors = append(actors, actor)
	}
	return actors, rows.Err()
}
