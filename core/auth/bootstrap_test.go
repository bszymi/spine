package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/adapters/store"
)

// ── Fake store extensions for bootstrap tests ──
//
// The auth_test fakeStore in auth_test.go covers token validation. The
// bootstrap path additionally needs CreateActor / UpdateActor and a way
// to inject store-side errors, hung onto the same fake via a small set
// of method overrides defined here.

func (f *fakeStore) CreateActor(_ context.Context, actor *domain.Actor) error {
	if _, ok := f.actors[actor.ActorID]; ok {
		return domain.NewError(domain.ErrConflict, "actor_id already exists")
	}
	cpy := *actor
	f.actors[actor.ActorID] = &cpy
	return nil
}

func (f *fakeStore) UpdateActor(_ context.Context, actor *domain.Actor) error {
	if _, ok := f.actors[actor.ActorID]; !ok {
		return domain.NewError(domain.ErrNotFound, "actor not found")
	}
	cpy := *actor
	f.actors[actor.ActorID] = &cpy
	return nil
}

// errStore wraps fakeStore and forces specific store calls to fail so
// the bootstrap's store-error branches are exercised.
type errStore struct {
	*fakeStore
	getActorErr       error
	createActorErr    error
	updateActorErr    error
	getByTokenHashErr error
	createTokenErr    error
}

func (e *errStore) GetActor(ctx context.Context, id string) (*domain.Actor, error) {
	if e.getActorErr != nil {
		return nil, e.getActorErr
	}
	return e.fakeStore.GetActor(ctx, id)
}

func (e *errStore) CreateActor(ctx context.Context, a *domain.Actor) error {
	if e.createActorErr != nil {
		return e.createActorErr
	}
	return e.fakeStore.CreateActor(ctx, a)
}

func (e *errStore) UpdateActor(ctx context.Context, a *domain.Actor) error {
	if e.updateActorErr != nil {
		return e.updateActorErr
	}
	return e.fakeStore.UpdateActor(ctx, a)
}

func (e *errStore) GetActorByTokenHash(ctx context.Context, hash string) (*domain.Actor, *domain.Token, error) {
	if e.getByTokenHashErr != nil {
		return nil, nil, e.getByTokenHashErr
	}
	return e.fakeStore.GetActorByTokenHash(ctx, hash)
}

func (e *errStore) CreateToken(ctx context.Context, r *store.TokenRecord) error {
	if e.createTokenErr != nil {
		return e.createTokenErr
	}
	return e.fakeStore.CreateToken(ctx, r)
}

// ── BootstrapInternalAdmin ──

func TestBootstrapInternalAdmin_NoOpWhenTokenEmpty(t *testing.T) {
	fs := newFakeStore()

	if err := auth.BootstrapInternalAdmin(context.Background(), fs, auth.BootstrapAdminConfig{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fs.actors) != 0 {
		t.Errorf("expected no actors created, got %d", len(fs.actors))
	}
	if len(fs.tokens) != 0 {
		t.Errorf("expected no tokens created, got %d", len(fs.tokens))
	}
}

func TestBootstrapInternalAdmin_CreatesActorAndToken(t *testing.T) {
	fs := newFakeStore()

	cfg := auth.BootstrapAdminConfig{Token: "smp-bearer-secret"}
	if err := auth.BootstrapInternalAdmin(context.Background(), fs, cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	actor, ok := fs.actors[auth.InternalAdminActorID]
	if !ok {
		t.Fatalf("expected actor %q to be created", auth.InternalAdminActorID)
	}
	if actor.Role != domain.RoleAdmin || actor.Status != domain.ActorStatusActive ||
		actor.Type != domain.ActorTypeAutomated {
		t.Errorf("unexpected actor shape: %+v", actor)
	}

	hash := auth.HashToken(cfg.Token)
	entry, ok := fs.tokens[hash]
	if !ok {
		t.Fatalf("expected token row for hash %s", hash)
	}
	if entry.token.ActorID != auth.InternalAdminActorID {
		t.Errorf("expected token bound to %q, got %q", auth.InternalAdminActorID, entry.token.ActorID)
	}
	wantTokenID := "bootstrap-" + hash[:12]
	if entry.token.TokenID != wantTokenID {
		t.Errorf("expected deterministic token_id %q, got %q", wantTokenID, entry.token.TokenID)
	}
}

func TestBootstrapInternalAdmin_HealsStaleActor(t *testing.T) {
	fs := newFakeStore()
	fs.actors[auth.InternalAdminActorID] = &domain.Actor{
		ActorID: auth.InternalAdminActorID,
		Type:    domain.ActorTypeAutomated,
		Name:    "drifted name",
		Role:    domain.RoleReader, // wrong role
		Status:  domain.ActorStatusSuspended,
	}

	if err := auth.BootstrapInternalAdmin(context.Background(), fs, auth.BootstrapAdminConfig{Token: "x"}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	got := fs.actors[auth.InternalAdminActorID]
	if got.Role != domain.RoleAdmin || got.Status != domain.ActorStatusActive {
		t.Errorf("stale actor not healed: %+v", got)
	}
}

func TestBootstrapInternalAdmin_NoOpWhenAlreadyConfigured(t *testing.T) {
	fs := newFakeStore()
	cfg := auth.BootstrapAdminConfig{Token: "stable-bearer"}

	if err := auth.BootstrapInternalAdmin(context.Background(), fs, cfg); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	hash := auth.HashToken(cfg.Token)
	firstTokenID := fs.tokens[hash].token.TokenID

	if err := auth.BootstrapInternalAdmin(context.Background(), fs, cfg); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	if len(fs.tokens) != 1 {
		t.Errorf("expected idempotent token, got %d rows", len(fs.tokens))
	}
	if fs.tokens[hash].token.TokenID != firstTokenID {
		t.Errorf("token_id changed across re-bootstrap: %q -> %q", firstTokenID, fs.tokens[hash].token.TokenID)
	}
}

func TestBootstrapInternalAdmin_RotatedTokenInsertsNewRow(t *testing.T) {
	fs := newFakeStore()

	if err := auth.BootstrapInternalAdmin(context.Background(), fs, auth.BootstrapAdminConfig{Token: "old-bearer"}); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	if err := auth.BootstrapInternalAdmin(context.Background(), fs, auth.BootstrapAdminConfig{Token: "new-bearer"}); err != nil {
		t.Fatalf("rotated bootstrap: %v", err)
	}

	if len(fs.tokens) != 2 {
		t.Errorf("expected old + new token rows, got %d", len(fs.tokens))
	}
}

func TestBootstrapInternalAdmin_TokenHashCollision(t *testing.T) {
	// A non-smp-admin actor whose token row hashes to the same value
	// as SMP_ADMIN_TOKEN must surface auth.ErrAdminTokenHashCollision
	// so the workspace-load wireup can fail loudly in production. The
	// historical behaviour (warn-and-return-nil) hid this under
	// sampled logging — see TASK-027.
	fs := newFakeStore()
	const collidingToken = "platform-bearer"
	hash := auth.HashToken(collidingToken)

	colliding := &domain.Actor{
		ActorID: "human-operator-42",
		Type:    domain.ActorTypeHuman,
		Name:    "Operator",
		Role:    domain.RoleReader,
		Status:  domain.ActorStatusActive,
	}
	fs.actors[colliding.ActorID] = colliding
	fs.tokens[hash] = &fakeTokenEntry{
		actor: colliding,
		token: &domain.Token{TokenID: "operator-token", ActorID: colliding.ActorID},
	}

	err := auth.BootstrapInternalAdmin(context.Background(), fs, auth.BootstrapAdminConfig{Token: collidingToken})
	if err == nil {
		t.Fatalf("expected collision error, got nil")
	}
	if !errors.Is(err, auth.ErrAdminTokenHashCollision) {
		t.Fatalf("expected ErrAdminTokenHashCollision, got %v", err)
	}
	// The bound actor's ID is operator-actionable context — it tells
	// the operator which auth.tokens row needs manual cleanup. Lock it
	// into the error message so log scrapers and on-call dashboards
	// surface it without parsing structured fields.
	if msg := err.Error(); !strings.Contains(msg, colliding.ActorID) {
		t.Errorf("error message %q should name the colliding actor %q", msg, colliding.ActorID)
	}

	// The collision must NOT rebind the existing token row — the whole
	// point of returning is to require manual cleanup. Asserting both
	// the unchanged ActorID and the unchanged TokenID guards against a
	// future refactor that "fixes" the collision by overwriting in place.
	got := fs.tokens[hash]
	if got == nil {
		t.Fatalf("colliding token row was deleted")
	}
	if got.token.ActorID != colliding.ActorID {
		t.Errorf("colliding actor was rebound: %q -> %q", colliding.ActorID, got.token.ActorID)
	}
	if got.token.TokenID != "operator-token" {
		t.Errorf("colliding token_id was overwritten: %q", got.token.TokenID)
	}
}

func TestBootstrapInternalAdmin_PropagatesStoreErrors(t *testing.T) {
	boom := errors.New("boom")

	cases := []struct {
		name   string
		setup  func(*errStore)
		expect string
	}{
		{
			name:   "GetActor failure",
			setup:  func(e *errStore) { e.getActorErr = boom },
			expect: "get bootstrap admin actor",
		},
		{
			name:   "CreateActor failure",
			setup:  func(e *errStore) { e.createActorErr = boom },
			expect: "create bootstrap admin actor",
		},
		{
			name: "UpdateActor failure",
			setup: func(e *errStore) {
				e.actors[auth.InternalAdminActorID] = &domain.Actor{ActorID: auth.InternalAdminActorID}
				e.updateActorErr = boom
			},
			expect: "update bootstrap admin actor",
		},
		{
			name:   "GetActorByTokenHash failure",
			setup:  func(e *errStore) { e.getByTokenHashErr = boom },
			expect: "lookup bootstrap admin token",
		},
		{
			name:   "CreateToken failure",
			setup:  func(e *errStore) { e.createTokenErr = boom },
			expect: "create bootstrap admin token",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			es := &errStore{fakeStore: newFakeStore()}
			tc.setup(es)

			err := auth.BootstrapInternalAdmin(context.Background(), es, auth.BootstrapAdminConfig{Token: "bearer"})
			if err == nil {
				t.Fatalf("expected error containing %q", tc.expect)
			}
			if !errors.Is(err, boom) {
				t.Errorf("expected wrapped boom, got %v", err)
			}
		})
	}
}
