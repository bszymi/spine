package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

// InternalAdminActorID is the well-known actor_id seeded by the
// platform-binding bootstrap so the platform's service_token_ref bearer
// authenticates against the workspace's runtime DB on the first request.
// Mirror of delivery.InternalSubscriptionName.
const InternalAdminActorID = "smp-admin"

const (
	internalAdminActorName = "SMP Bootstrap Admin"
	// internalAdminTokenName is the name field stored on the auth.tokens
	// row, not a credential. The bearer itself comes from cfg.Token.
	internalAdminTokenName = "smp-admin-bootstrap" // #nosec G101 -- name field, not a token
)

// BootstrapAdminConfig holds the env-derived config for the platform's
// bootstrap admin actor.
type BootstrapAdminConfig struct {
	// Token is the bearer the platform presents (SMP_ADMIN_TOKEN). An
	// empty value disables the bootstrap, preserving single-workspace
	// and pre-platform-binding deployments.
	Token string
}

// ErrAdminTokenHashCollision is the matchable sentinel returned when
// SMP_ADMIN_TOKEN's hash is already bound to an actor other than
// smp-admin. With a 256-bit hash this branch is reachable only via
// deliberate token reuse (an operator pasted the platform bearer onto a
// human or service actor) — never random collision. The caller decides
// whether to fail workspace load (production strict-startup) or
// log-and-continue (dev): wrapping ensures both paths can identify the
// failure mode without parsing log text.
var ErrAdminTokenHashCollision = errors.New("bootstrap admin token hash bound to non-bootstrap actor")

// BootstrapInternalAdmin idempotently inserts the platform's bootstrap
// admin actor + token row into the workspace runtime DB so the
// platform's bearer authenticates on the first workspace request.
// Mirror of delivery.BootstrapInternalSubscription — runs from
// wireWorkspaceDelivery on every workspace load and is safe to re-run.
//
// An empty cfg.Token is the no-op path (single-workspace /
// pre-platform-binding deployments stay green). On a token rotation
// the new hash inserts a new auth.tokens row; the stale row remains
// until rotation cleanup is built (out of scope here — the live bearer
// is the only one anyone uses).
func BootstrapInternalAdmin(ctx context.Context, st store.AuthStore, cfg BootstrapAdminConfig) error {
	if cfg.Token == "" {
		return nil
	}

	log := observe.Logger(ctx)

	if err := upsertInternalAdminActor(ctx, st, log); err != nil {
		return err
	}
	return upsertInternalAdminToken(ctx, st, cfg.Token, log)
}

func upsertInternalAdminActor(ctx context.Context, st store.AuthStore, log *slog.Logger) error {
	desired := &domain.Actor{
		ActorID: InternalAdminActorID,
		Type:    domain.ActorTypeAutomated,
		Name:    internalAdminActorName,
		Role:    domain.RoleAdmin,
		Status:  domain.ActorStatusActive,
	}

	existing, err := st.GetActor(ctx, InternalAdminActorID)
	switch {
	case err == nil:
		if internalAdminActorMatches(existing, desired) {
			log.Debug("internal admin actor already configured", "actor_id", InternalAdminActorID)
			return nil
		}
		if err := st.UpdateActor(ctx, desired); err != nil {
			return fmt.Errorf("update bootstrap admin actor: %w", err)
		}
		log.Info("internal admin actor updated", "actor_id", InternalAdminActorID)
		return nil
	case isNotFound(err):
		if err := st.CreateActor(ctx, desired); err != nil {
			return fmt.Errorf("create bootstrap admin actor: %w", err)
		}
		log.Info("internal admin actor created", "actor_id", InternalAdminActorID)
		return nil
	default:
		return fmt.Errorf("get bootstrap admin actor: %w", err)
	}
}

func upsertInternalAdminToken(ctx context.Context, st store.AuthStore, rawToken string, log *slog.Logger) error {
	hash := HashToken(rawToken)
	tokenID := internalAdminTokenID(hash)

	actor, _, err := st.GetActorByTokenHash(ctx, hash)
	switch {
	case err == nil:
		if actor != nil && actor.ActorID == InternalAdminActorID {
			log.Debug("internal admin token already configured", "actor_id", InternalAdminActorID, "token_id", tokenID)
			return nil
		}
		// A row exists for this hash but is bound to a different
		// actor. The spec's literal ON CONFLICT semantics would rebind
		// it to smp-admin, but doing so silently re-maps an operator's
		// bearer to admin without their knowledge — strictly worse
		// than a 401 they will notice. Return a typed error so the
		// caller can fail workspace load loudly in production (matching
		// SPINE_ENV=production strict-startup philosophy) while
		// non-production callers still get a matchable cause to
		// log-and-continue with operator-actionable context. With a
		// 256-bit hash this branch is reachable only via deliberate
		// token reuse, never random collision.
		return fmt.Errorf("bootstrap admin token hash already bound to actor %q; manual cleanup of colliding auth.tokens row required: %w",
			actorIDOrEmpty(actor), ErrAdminTokenHashCollision)
	case isUnauthorized(err) || isNotFound(err):
		// Fall through to insert.
	default:
		return fmt.Errorf("lookup bootstrap admin token: %w", err)
	}

	record := &store.TokenRecord{
		TokenID:   tokenID,
		ActorID:   InternalAdminActorID,
		TokenHash: hash,
		Name:      internalAdminTokenName,
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateToken(ctx, record); err != nil {
		return fmt.Errorf("create bootstrap admin token: %w", err)
	}
	log.Info("internal admin token created", "actor_id", InternalAdminActorID, "token_id", tokenID)
	return nil
}

func internalAdminTokenID(hash string) string {
	return "bootstrap-" + hash[:12]
}

// internalAdminActorMatches reports whether the existing actor row
// matches the canonical bootstrap shape on the fields Store.UpdateActor
// can write. actor_type is intentionally excluded: UpdateActor's UPDATE
// clause does not touch it, so comparing it would force an Update on
// every workspace load that still wouldn't repair the drift. A stale
// actor_type for smp-admin is operator-introduced and stays out of
// scope; everything Update can fix is in the comparison.
func internalAdminActorMatches(existing, desired *domain.Actor) bool {
	return existing.Name == desired.Name &&
		existing.Role == desired.Role &&
		existing.Status == desired.Status
}

func isNotFound(err error) bool {
	var spineErr *domain.SpineError
	return errors.As(err, &spineErr) && spineErr.Code == domain.ErrNotFound
}

func isUnauthorized(err error) bool {
	var spineErr *domain.SpineError
	return errors.As(err, &spineErr) && spineErr.Code == domain.ErrUnauthorized
}

func actorIDOrEmpty(a *domain.Actor) string {
	if a == nil {
		return ""
	}
	return a.ActorID
}
