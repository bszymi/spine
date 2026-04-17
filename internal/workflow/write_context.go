package workflow

import "context"

type writeContextKey struct{}

// WriteContext carries branch information for scoped workflow writes.
// When attached to a context, workflow.Create/Update route commits to the
// specified branch via a git worktree instead of the authoritative branch.
// This mirrors the pattern in internal/artifact and is the mechanism that
// lets workflow edits flow through the ADR-008 lifecycle workflow.
type WriteContext struct {
	Branch string
}

// WithWriteContext attaches a WriteContext to the given context.
func WithWriteContext(ctx context.Context, wc WriteContext) context.Context {
	return context.WithValue(ctx, writeContextKey{}, &wc)
}

// GetWriteContext extracts the WriteContext from a context, if present.
// Returns nil when no WriteContext is set (writes go to the current branch).
func GetWriteContext(ctx context.Context) *WriteContext {
	if v, ok := ctx.Value(writeContextKey{}).(*WriteContext); ok {
		return v
	}
	return nil
}
