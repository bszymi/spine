package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
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
	// and pre-platform-binding deployments. core/auth does not judge its
	// strength: that is the embedder's policy (spine serve gates
	// SMP_ADMIN_TOKEN at boot; SMP owns its own bearer's policy).
	Token string

	// SupersededBefore bounds which older bootstrap token rows a
	// rotation revokes: rows created at or after it are never revoked.
	// A deployment that runs several replicas passes its process start
	// time, so a still-running replica that carries the previous bearer
	// can never revoke the newer bearer a newer replica seeded during a
	// rolling deploy. The zero value adds no bound: every bootstrap row
	// older than the current bearer's row is revoked.
	//
	// The zero value is therefore NOT the pre-INIT-023 behaviour (which
	// never revoked): this is a shared-behaviour change (INIT-023 §7b
	// rule 4), inert in an embedder that never validates the rows this
	// function writes. An embedder that does validate them (anything
	// that calls auth.Service.ValidateToken, or reads auth.tokens, for
	// a bootstrap bearer) should pass its process start here, and must
	// accept that revocation is never undone: a re-run with a mistyped
	// bearer revokes the real one, and a later rollback to it stays
	// refused until the bearer is rotated to a fresh value.
	SupersededBefore time.Time
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
// Mirror of delivery.BootstrapInternalSubscription — spine serve runs
// it on every workspace load (bootstrapInternalAdmin in the pooled
// workspace builder) and it is safe to re-run.
//
// An empty cfg.Token is the no-op path (single-workspace /
// pre-platform-binding deployments stay green). It does not retire an
// earlier bearer: rows seeded while SMP_ADMIN_TOKEN was set stay valid
// until an admin revokes them (DELETE /api/v1/tokens/{id}).
//
// Rotation (INIT-023 TASK-013, audit P3-31). Once the current bearer's
// row exists, every other live bootstrap row of smp-admin that is not
// newer than it — and, with a non-zero cfg.SupersededBefore, was
// created before that cutoff — is revoked. A rotated-away platform
// bearer therefore stops authenticating on the first workspace load
// after the rotation, instead of keeping admin forever. Revocation is
// never undone: a rollback to a revoked bearer is refused and logged at
// Error, not re-enabled. Tokens an admin minted for smp-admin through
// the token API carry "tok_" ids and are never touched. The sweep runs
// on every call that has a live current row — also when that row
// already existed — so the first call by this version also revokes
// bearers from rotations made before it, and a call that failed half
// way is completed by the next one. revokeSupersededAdminTokens
// documents the two rollout residues that ordering by time leaves.
//
// Errors. Any store error is returned (wrapped), the sweep's included;
// the function is idempotent, so the caller retries by calling it
// again. A failed sweep leaves the superseded bearer live until a call
// succeeds: spine serve therefore retries in the background for as
// long as the workspace stays loaded.
//
// Status (P3-31). The bootstrap heals smp-admin's name and role, but
// never re-activates a suspended or deactivated smp-admin: those are
// the operator's documented lifecycle controls (actor-model.md §7.1),
// and deactivation is permanent (actor.Service.Reactivate).
//
// Token ids (P3-35) are random ("bootstrap-" + 16 hex), not derived
// from the bearer's hash, so listing smp-admin's tokens discloses
// nothing about the bearer. Rows written by earlier versions keep their
// hash-derived ids until a rotation revokes them.
func BootstrapInternalAdmin(ctx context.Context, st store.AuthStore, cfg BootstrapAdminConfig) error {
	if cfg.Token == "" {
		return nil
	}

	log := observe.Logger(ctx)

	if err := upsertInternalAdminActor(ctx, st, log); err != nil {
		return err
	}
	current, err := upsertInternalAdminToken(ctx, st, cfg.Token, log)
	if err != nil {
		return err
	}
	if current == nil {
		// The current bearer's row is revoked (already logged at
		// Error). Nothing is un-revoked, and nothing else is revoked:
		// a rollback must not take down the newer bearer too.
		return nil
	}
	return revokeSupersededAdminTokens(ctx, st, current, cfg.SupersededBefore, log)
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
		// A suspended or deactivated smp-admin is operator intent
		// (incident response), not drift: keep it, and say why the
		// platform bearer is about to be refused. Only these two
		// statuses are preserved — an empty or unknown status is still
		// healed to active, otherwise the bearer would be refused as
		// "actor " forever with no log saying why (Decision 6).
		if existing.Status == domain.ActorStatusSuspended || existing.Status == domain.ActorStatusDeactivated {
			desired.Status = existing.Status
			log.Warn("internal admin actor is not active; bootstrap leaves its status unchanged and the platform bearer will be refused",
				"actor_id", InternalAdminActorID, "status", string(existing.Status))
		}
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

// upsertInternalAdminToken makes sure rawToken's hash has a bootstrap
// row bound to smp-admin and returns that row — the "current" row the
// rotation sweep measures against. It returns (nil, nil) when the row
// exists but is revoked: the bearer stays refused, and the caller must
// not sweep.
func upsertInternalAdminToken(ctx context.Context, st store.AuthStore, rawToken string, log *slog.Logger) (*domain.Token, error) {
	hash := HashToken(rawToken)

	actor, tok, err := st.GetActorByTokenHash(ctx, hash)
	switch {
	case err == nil:
		if actor != nil && actor.ActorID == InternalAdminActorID {
			// GetActorByTokenHash returns revoked rows (the SQL has no
			// revoked_at filter), so a rollback to a bearer a rotation
			// revoked lands here. Never un-revoke: that would let a
			// leaked bearer come back through a config rollback. Do not
			// fail the load either — in standalone production that would
			// lock every user out of the workspace, in SMP it would
			// wedge provisioning. The bearer is refused either way; the
			// log says why and what to do.
			if tok != nil && tok.RevokedAt != nil {
				log.Error("SMP_ADMIN_TOKEN matches a revoked bootstrap admin token; the platform bearer will be refused until it is rotated to a fresh value",
					"actor_id", InternalAdminActorID, "token_id", tok.TokenID)
				return nil, nil
			}
			log.Debug("internal admin token already configured", "actor_id", InternalAdminActorID, "token_id", tokenIDOrEmpty(tok))
			return tok, nil
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
		// token reuse, never random collision. It runs before any
		// revocation, so a collision never revokes anything.
		return nil, fmt.Errorf("bootstrap admin token hash already bound to actor %q; manual cleanup of colliding auth.tokens row required: %w",
			actorIDOrEmpty(actor), ErrAdminTokenHashCollision)
	case isUnauthorized(err) || isNotFound(err):
		// Fall through to insert.
	default:
		return nil, fmt.Errorf("lookup bootstrap admin token: %w", err)
	}

	tokenID, err := newInternalAdminTokenID()
	if err != nil {
		return nil, fmt.Errorf("generate bootstrap admin token id: %w", err)
	}
	record := &store.TokenRecord{
		TokenID:   tokenID,
		ActorID:   InternalAdminActorID,
		TokenHash: hash,
		Name:      internalAdminTokenName,
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateToken(ctx, record); err != nil {
		return nil, fmt.Errorf("create bootstrap admin token: %w", err)
	}
	log.Info("internal admin token created", "actor_id", InternalAdminActorID, "token_id", tokenID)
	return &domain.Token{
		TokenID:   record.TokenID,
		ActorID:   record.ActorID,
		Name:      record.Name,
		CreatedAt: record.CreatedAt,
	}, nil
}

// internalAdminTokenIDPrefix marks the rows this bootstrap writes, and
// is what the rotation sweep matches on (together with the row name).
// auth.Service.CreateToken mints "tok_" ids, so a token an admin minted
// for smp-admin through the API never matches, whatever its name.
const internalAdminTokenIDPrefix = "bootstrap-"

// newInternalAdminTokenID returns "bootstrap-" + 16 random hex
// characters: the same entropy as auth.Service's tok_ ids, and
// independent of the bearer (audit P3-35). The previous id embedded
// sha256(bearer)[:12], which a workspace admin could list and use as a
// 48-bit offline oracle for the platform bearer. Idempotency never
// depended on the id: it is keyed on token_hash (the
// GetActorByTokenHash early return and the UNIQUE constraint).
func newInternalAdminTokenID() (string, error) {
	suffix, err := generateRandomHex(8)
	if err != nil {
		return "", err
	}
	return internalAdminTokenIDPrefix + suffix, nil
}

// revokeSupersededAdminTokens revokes every live bootstrap row of
// smp-admin that a rotation superseded. A row is revoked only when all
// of these hold:
//
//   - it is not current, and not already revoked;
//   - it is a bootstrap row: name smp-admin-bootstrap AND a "bootstrap-"
//     id (admin-minted "tok_" tokens are never touched);
//   - it was not created after current — a newer row belongs to a newer
//     bearer, for example one a newer replica seeded;
//   - when cutoff is non-zero, it was created before cutoff. spine serve
//     passes its process start, so a replica never revokes a row created
//     after it booted: an old replica still carrying the previous bearer
//     cannot revoke the new bearer during a rolling deploy.
//
// ErrNotFound from RevokeToken means another workspace loader revoked
// the row first (Postgres' RevokeToken matches only live rows); it is
// skipped. Any other error is returned, naming the token id.
//
// Ordering is by created_at, which the inserting process stamps. Two
// residues are inherent to ordering by time (INIT-023 TASK-013 owner
// question 5); both are confined to workspaces first loaded during a
// rollout, so the workspace had a row for one bearer and none for the
// other:
//
//   - New bearer first, old bearer re-inserted later by a replica that
//     still carries it (an old-config replica, or old code during the
//     upgrade): the old row is newer than the current one, so no
//     replica carrying the new bearer ever revokes it, restarts
//     included. It stays live until the next rotation, or until an
//     admin revokes it. The sweep logs such a row at Warn, with its
//     token id, once no rollout can explain it any more (no cutoff, or
//     created before the cutoff).
//   - An old-config replica that restarts mid-rollout gets a fresh
//     cutoff and, where the new bearer's row is the only one, inserts
//     the old bearer and revokes the new one. The recovery is to
//     rotate forward.
//
// A row newer than the current one is never revoked, whatever the
// cutoff: from the data alone it is indistinguishable from a newer
// bearer that this process (a stale replica) must not revoke.
func revokeSupersededAdminTokens(ctx context.Context, st store.AuthStore, current *domain.Token, cutoff time.Time, log *slog.Logger) error {
	tokens, err := st.ListTokensByActor(ctx, InternalAdminActorID)
	if err != nil {
		return fmt.Errorf("list bootstrap admin tokens: %w", err)
	}
	for i := range tokens {
		t := &tokens[i]
		if t.TokenID == current.TokenID || t.RevokedAt != nil {
			continue
		}
		if t.Name != internalAdminTokenName || !strings.HasPrefix(t.TokenID, internalAdminTokenIDPrefix) {
			continue
		}
		if t.CreatedAt.After(current.CreatedAt) {
			// Never revoked (see above). A row created after the cutoff
			// is the expected rolling-deploy case — a newer replica's
			// bearer — and stays quiet. One with no cutoff, or created
			// before it, is either a superseded bearer that was
			// re-inserted after the current one, or a sign that this
			// process carries a stale SMP_ADMIN_TOKEN: either way an
			// operator has to look, so say which row.
			if cutoff.IsZero() || t.CreatedAt.Before(cutoff) {
				log.Warn("a live bootstrap admin token is newer than the current SMP_ADMIN_TOKEN's row and is left in place; if it belongs to a rotated-away bearer, revoke it (DELETE /api/v1/tokens/{token_id}), otherwise this process carries a stale SMP_ADMIN_TOKEN",
					"actor_id", InternalAdminActorID, "token_id", t.TokenID)
			}
			continue
		}
		if !cutoff.IsZero() && !t.CreatedAt.Before(cutoff) {
			continue
		}
		if err := st.RevokeToken(ctx, t.TokenID); err != nil {
			if isNotFound(err) {
				continue // revoked concurrently by another loader
			}
			return fmt.Errorf("revoke superseded bootstrap admin token %s: %w", t.TokenID, err)
		}
		log.Info("superseded bootstrap admin token revoked", "actor_id", InternalAdminActorID, "token_id", t.TokenID)
		observe.AuditLog(ctx, "bootstrap_admin_token_revoked",
			"actor_id", InternalAdminActorID, "token_id", t.TokenID, "reason", "superseded")
	}
	return nil
}

func tokenIDOrEmpty(t *domain.Token) string {
	if t == nil {
		return ""
	}
	return t.TokenID
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
