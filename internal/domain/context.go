package domain

import "context"

type actorCtxKey struct{}

// WithActor returns a copy of ctx carrying the authenticated actor.
// The gateway middleware sets this at request entry; downstream services
// (artifact, orchestrator) consume it via ActorFromContext to populate
// identity on branch-protection and audit requests.
func WithActor(ctx context.Context, a *Actor) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, a)
}

// ActorFromContext returns the authenticated actor from ctx, or nil when
// no actor is bound (unauthenticated request, test harness without one).
// Callers that require an actor must check for nil; this package does not
// synthesize a default actor.
func ActorFromContext(ctx context.Context) *Actor {
	a, _ := ctx.Value(actorCtxKey{}).(*Actor)
	return a
}
