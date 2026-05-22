package workflow

import "context"

type writeContextKey struct{}
type bypassKey struct{}

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

// WithBypass marks the context as an operator-bypass write so that the
// resulting Git commit carries a Workflow-Bypass trailer (ADR-008). Bypass is
// reserved for operator/admin direct-commit recovery when the lifecycle
// governance flow cannot be used — the trailer makes it discoverable in audit.
func WithBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, bypassKey{}, true)
}

// IsBypass returns true when the context has been marked as an
// operator-bypass write via WithBypass.
func IsBypass(ctx context.Context) bool {
	v, _ := ctx.Value(bypassKey{}).(bool)
	return v
}
