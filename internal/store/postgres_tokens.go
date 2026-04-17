package store

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ── Tokens ──

func (s *PostgresStore) GetActorByTokenHash(ctx context.Context, tokenHash string) (*domain.Actor, *domain.Token, error) {
	var actor domain.Actor
	var token domain.Token
	err := s.pool.QueryRow(ctx, `
		SELECT a.actor_id, a.actor_type, a.name, a.role, a.status,
		       t.token_id, t.actor_id, t.name, t.expires_at, t.revoked_at, t.created_at
		FROM auth.tokens t
		JOIN auth.actors a ON t.actor_id = a.actor_id
		WHERE t.token_hash = $1`, tokenHash,
	).Scan(
		&actor.ActorID, &actor.Type, &actor.Name, &actor.Role, &actor.Status,
		&token.TokenID, &token.ActorID, &token.Name, &token.ExpiresAt, &token.RevokedAt, &token.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil, domain.NewError(domain.ErrUnauthorized, "invalid token")
		}
		return nil, nil, err
	}
	return &actor, &token, nil
}

func (s *PostgresStore) CreateToken(ctx context.Context, record *TokenRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth.tokens (token_id, actor_id, token_hash, name, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		record.TokenID, record.ActorID, record.TokenHash, record.Name, record.ExpiresAt, record.CreatedAt,
	)
	return err
}

func (s *PostgresStore) RevokeToken(ctx context.Context, tokenID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE auth.tokens SET revoked_at = now() WHERE token_id = $1 AND revoked_at IS NULL`, tokenID)
	if err != nil {
		return err
	}
	return mustAffect(tag, "token not found or already revoked")
}

func (s *PostgresStore) ListTokensByActor(ctx context.Context, actorID string) ([]domain.Token, error) {
	return queryAll(ctx, s.pool, `
		SELECT token_id, actor_id, name, expires_at, revoked_at, created_at
		FROM auth.tokens WHERE actor_id = $1 ORDER BY created_at DESC`,
		[]any{actorID},
		func(row pgx.Rows, t *domain.Token) error {
			return row.Scan(&t.TokenID, &t.ActorID, &t.Name, &t.ExpiresAt, &t.RevokedAt, &t.CreatedAt)
		},
	)
}
