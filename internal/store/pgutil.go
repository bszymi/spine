package store

import (
	"context"
	"errors"

	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// notFoundOr maps pgx.ErrNoRows to a domain NotFound error with the given
// message. All other errors pass through unchanged. This is a tiny wrapper
// but it's been copy-pasted 15 times across postgres.go — collapsing it
// keeps the per-entity "not found" messages uniform.
func notFoundOr(err error, message string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NewError(domain.ErrNotFound, message)
	}
	return err
}

// mustAffect returns a domain NotFound error when the command affected zero
// rows. Mirrors the "no row matched the WHERE clause" convention used by
// the existing store.
func mustAffect(tag pgconn.CommandTag, message string) error {
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, message)
	}
	return nil
}

// queryAll runs a parameterised query and accumulates rows via scan.
// The scan callback receives one row at a time and fills *T; returning an
// error aborts iteration and surfaces the error to the caller. This
// consolidates the `pool.Query → defer rows.Close → for rows.Next → scan
// → append` loop that otherwise repeats at every "List by X" site.
func queryAll[T any](
	ctx context.Context,
	pool *pgxpool.Pool,
	sql string,
	args []any,
	scan func(pgx.Rows, *T) error,
) ([]T, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []T
	for rows.Next() {
		var item T
		if err := scan(rows, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
