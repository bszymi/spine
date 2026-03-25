package artifact

import "context"

type writeContextKey struct{}

// WriteContext carries branch information for scoped Git writes.
// When attached to a context, artifact writes go to the specified branch
// instead of the current branch (authoritative/main).
type WriteContext struct {
	Branch string // Target branch for writes (e.g., "run-abc123")
}

// WithWriteContext attaches a WriteContext to the given context.
func WithWriteContext(ctx context.Context, wc WriteContext) context.Context {
	return context.WithValue(ctx, writeContextKey{}, &wc)
}

// GetWriteContext extracts the WriteContext from a context, if present.
// Returns nil if no WriteContext is set (writes go to the current branch).
func GetWriteContext(ctx context.Context) *WriteContext {
	if v, ok := ctx.Value(writeContextKey{}).(*WriteContext); ok {
		return v
	}
	return nil
}
