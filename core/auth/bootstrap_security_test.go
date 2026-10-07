package auth_test

// Regression tests for INIT-023 TASK-013 (audit P3-31 and P3-35). The
// bodies derive from the 2026-10-06 audit PoCs
// poc/crypto-secrets-auth/bootstrap_rotation_test.go and
// poc/gap1-crypto-secrets-reaud/auth_bootstrap_poc_test.go with their
// assertions inverted: against 61994fe a rotated-away bootstrap bearer
// kept full admin, its token id was a prefix of sha256(bearer), and a
// deactivated smp-admin was re-activated on the next workspace load.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
)

// ── helpers ──

// lockedBuffer is a goroutine-safe log sink for the global slog
// handler the bootstrap logs through (observe.Logger → slog.Default).
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureBootstrapLog routes slog.Default to a buffer at DEBUG for the
// duration of t.
func captureBootstrapLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func randomBearer(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// memAuth is a complete in-memory store.AuthStore: unlike the audit
// PoC's store it honours RevokeToken (Postgres semantics: NotFound when
// the row is missing or already revoked) and ListTokensByActor, so the
// rotation sweep is observable end-to-end through auth.Service.
type memAuth struct {
	mu      sync.Mutex
	actors  map[string]domain.Actor
	tokens  map[string]store.TokenRecord // by hash
	revoked map[string]time.Time         // by token id
}

func newMemAuth() *memAuth {
	return &memAuth{
		actors:  map[string]domain.Actor{},
		tokens:  map[string]store.TokenRecord{},
		revoked: map[string]time.Time{},
	}
}

func (m *memAuth) GetActor(_ context.Context, id string) (*domain.Actor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.actors[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "actor not found")
	}
	return &a, nil
}

func (m *memAuth) CreateActor(_ context.Context, a *domain.Actor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.actors[a.ActorID]; ok {
		return domain.NewError(domain.ErrConflict, "actor_id already exists")
	}
	m.actors[a.ActorID] = *a
	return nil
}

// UpdateActor mirrors the Postgres UPDATE: name, role and status only.
func (m *memAuth) UpdateActor(_ context.Context, a *domain.Actor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.actors[a.ActorID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "actor not found")
	}
	cur.Name, cur.Role, cur.Status = a.Name, a.Role, a.Status
	m.actors[a.ActorID] = cur
	return nil
}

func (m *memAuth) ListActors(context.Context) ([]domain.Actor, error) { return nil, nil }
func (m *memAuth) ListActorsByStatus(context.Context, domain.ActorStatus) ([]domain.Actor, error) {
	return nil, nil
}

func (m *memAuth) tokenOf(r store.TokenRecord) *domain.Token {
	tok := &domain.Token{TokenID: r.TokenID, ActorID: r.ActorID, Name: r.Name, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt}
	if at, ok := m.revoked[r.TokenID]; ok {
		at := at
		tok.RevokedAt = &at
	}
	return tok
}

// GetActorByTokenHash returns revoked rows too, like the Postgres SQL
// (no revoked_at filter); auth.Service.ValidateToken refuses them.
func (m *memAuth) GetActorByTokenHash(_ context.Context, h string) (*domain.Actor, *domain.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.tokens[h]
	if !ok {
		return nil, nil, domain.NewError(domain.ErrUnauthorized, "invalid token")
	}
	a := m.actors[r.ActorID]
	return &a, m.tokenOf(r), nil
}

func (m *memAuth) CreateToken(_ context.Context, r *store.TokenRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tokens[r.TokenHash]; ok {
		return domain.NewError(domain.ErrConflict, "token_hash already exists")
	}
	for _, existing := range m.tokens {
		if existing.TokenID == r.TokenID {
			return domain.NewError(domain.ErrConflict, "token_id already exists")
		}
	}
	m.tokens[r.TokenHash] = *r
	return nil
}

func (m *memAuth) RevokeToken(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, already := m.revoked[id]; already {
		return domain.NewError(domain.ErrNotFound, "token not found or already revoked")
	}
	for _, r := range m.tokens {
		if r.TokenID == id {
			m.revoked[id] = time.Now().UTC()
			return nil
		}
	}
	return domain.NewError(domain.ErrNotFound, "token not found or already revoked")
}

func (m *memAuth) ListTokensByActor(_ context.Context, actorID string) ([]domain.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Token
	for _, r := range m.tokens {
		if r.ActorID == actorID {
			out = append(out, *m.tokenOf(r))
		}
	}
	return out, nil
}

func (m *memAuth) isRevoked(tokenID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.revoked[tokenID]
	return ok
}

// seedLegacyBootstrapRow inserts a bootstrap row as an earlier Spine
// would have (the pre-INIT-023 hash-derived id shape is irrelevant to
// the sweep; only the bootstrap- prefix and the name are matched).
func (m *memAuth) seedLegacyBootstrapRow(id, hash string, createdAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[hash] = store.TokenRecord{
		TokenID: id, ActorID: auth.InternalAdminActorID, TokenHash: hash,
		Name: "smp-admin-bootstrap", CreatedAt: createdAt,
	}
}

var _ store.AuthStore = (*memAuth)(nil)

// actorOnly adapts memAuth to the store.Store actor.Service needs, so
// the tests drive suspension and deactivation through Spine's own
// lifecycle service, exactly as an operator's incident response would.
type actorOnly struct {
	store.Store
	m *memAuth
}

func (a actorOnly) GetActor(ctx context.Context, id string) (*domain.Actor, error) {
	return a.m.GetActor(ctx, id)
}

func (a actorOnly) UpdateActor(ctx context.Context, x *domain.Actor) error {
	return a.m.UpdateActor(ctx, x)
}

// ── AC 8: rotation revokes the rotated-away bearer ──

func TestBootstrapSecurity_RotationRevokesOldBearer(t *testing.T) {
	ctx := context.Background()
	logs := captureBootstrapLog(t)
	st := newFakeStore()
	const leakedOld = "old-platform-admin-bearer-that-leaked"
	const rotatedNew = "new-platform-admin-bearer-after-rotation"

	if err := auth.BootstrapInternalAdmin(ctx, st, auth.BootstrapAdminConfig{Token: leakedOld}); err != nil {
		t.Fatal(err)
	}
	if err := auth.BootstrapInternalAdmin(ctx, st, auth.BootstrapAdminConfig{Token: rotatedNew}); err != nil {
		t.Fatal(err)
	}

	svc := auth.NewService(st)
	if _, err := svc.ValidateToken(ctx, leakedOld); err == nil || !strings.Contains(err.Error(), "token revoked") {
		t.Fatalf("rotated-away bootstrap bearer: err=%v, want \"token revoked\"", err)
	}
	a, err := svc.ValidateToken(ctx, rotatedNew)
	if err != nil {
		t.Fatalf("new bearer refused: %v", err)
	}
	if err := auth.Authorize(a, "token.create"); err != nil {
		t.Fatalf("new bearer lost admin: %v", err)
	}

	out := logs.String()
	if !strings.Contains(out, "audit_operation=bootstrap_admin_token_revoked") || !strings.Contains(out, "reason=superseded") {
		t.Fatalf("revocation not audited; log:\n%s", out)
	}
	// No log line carries a bearer or any part of its hash (P3-35:
	// the insert log used to print bootstrap-<sha256[:12]>).
	for _, bearer := range []string{leakedOld, rotatedNew} {
		if strings.Contains(out, bearer) || strings.Contains(out, auth.HashToken(bearer)[:12]) {
			t.Fatalf("log leaks a bearer or its hash prefix:\n%s", out)
		}
	}
}

// ── AC 9: the token id is independent of the bearer ──

func TestBootstrapSecurity_TokenIDIndependentOfBearer(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	// core/auth does not judge bearer strength: embedders own that
	// policy, and spine serve checks SMP_ADMIN_TOKEN at boot.
	const weak = "admin"
	if err := auth.BootstrapInternalAdmin(ctx, st, auth.BootstrapAdminConfig{Token: weak}); err != nil {
		t.Fatalf("core/auth must not refuse a weak bearer: %v", err)
	}
	h := auth.HashToken(weak)
	entry := st.tokens[h]
	if entry == nil {
		t.Fatal("no row for the bearer")
	}
	id := entry.token.TokenID
	if !bootstrapTokenIDPattern.MatchString(id) {
		t.Fatalf("token_id %q does not match %s", id, bootstrapTokenIDPattern)
	}
	if strings.HasPrefix(h, strings.TrimPrefix(id, "bootstrap-")) {
		t.Fatalf("token_id %q is a prefix of sha256(bearer)", id)
	}
}

// ── AC 10: the bootstrap never re-activates smp-admin ──

func TestBootstrapSecurity_DoesNotResurrectDeactivatedAdmin(t *testing.T) {
	ctx := context.Background()
	logs := captureBootstrapLog(t)
	m := newMemAuth()
	bearer := randomBearer(t)

	if err := auth.BootstrapInternalAdmin(ctx, m, auth.BootstrapAdminConfig{Token: bearer}); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(m)
	if _, err := svc.ValidateToken(ctx, bearer); err != nil {
		t.Fatalf("bearer should validate initially: %v", err)
	}
	// Incident response: the operator permanently deactivates smp-admin
	// through Spine's own lifecycle service.
	if err := actor.NewService(actorOnly{m: m}).Deactivate(ctx, auth.InternalAdminActorID); err != nil {
		t.Fatal(err)
	}

	// Next workspace load.
	if err := auth.BootstrapInternalAdmin(ctx, m, auth.BootstrapAdminConfig{Token: bearer}); err != nil {
		t.Fatal(err)
	}
	a, _ := m.GetActor(ctx, auth.InternalAdminActorID)
	if a.Status != domain.ActorStatusDeactivated {
		t.Fatalf("bootstrap re-activated a deactivated smp-admin (status %q)", a.Status)
	}
	if _, err := svc.ValidateToken(ctx, bearer); err == nil {
		t.Fatal("bearer of a deactivated smp-admin authenticates again")
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "internal admin actor is not active") {
		t.Fatalf("non-active smp-admin not logged at WARN:\n%s", logs.String())
	}
}

func TestBootstrapSecurity_KeepsSuspendedAdminSuspended(t *testing.T) {
	ctx := context.Background()
	m := newMemAuth()
	bearer := randomBearer(t)

	if err := auth.BootstrapInternalAdmin(ctx, m, auth.BootstrapAdminConfig{Token: bearer}); err != nil {
		t.Fatal(err)
	}
	if err := actor.NewService(actorOnly{m: m}).Suspend(ctx, auth.InternalAdminActorID); err != nil {
		t.Fatal(err)
	}
	if err := auth.BootstrapInternalAdmin(ctx, m, auth.BootstrapAdminConfig{Token: bearer}); err != nil {
		t.Fatal(err)
	}
	a, _ := m.GetActor(ctx, auth.InternalAdminActorID)
	if a.Status != domain.ActorStatusSuspended {
		t.Fatalf("status %q after bootstrap, want suspended", a.Status)
	}
	if _, err := auth.NewService(m).ValidateToken(ctx, bearer); err == nil {
		t.Fatal("bearer of a suspended smp-admin authenticates")
	}
	// Suspension stays reversible through the lifecycle service: the
	// bootstrap left the row intact, so an explicit Reactivate works.
	if err := actor.NewService(actorOnly{m: m}).Reactivate(ctx, auth.InternalAdminActorID); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if _, err := auth.NewService(m).ValidateToken(ctx, bearer); err != nil {
		t.Fatalf("bearer refused after explicit reactivation: %v", err)
	}
}

// Decision 6: only suspended and deactivated are operator intent. An
// empty (or otherwise unknown) status is drift and is still healed to
// active — otherwise the platform bearer would be refused as "actor "
// forever, with no log saying why.
func TestBootstrapSecurity_HealsEmptyStatusToActive(t *testing.T) {
	ctx := context.Background()
	m := newMemAuth()
	m.actors[auth.InternalAdminActorID] = domain.Actor{
		ActorID: auth.InternalAdminActorID,
		Type:    domain.ActorTypeAutomated,
		Name:    "SMP Bootstrap Admin",
		Role:    domain.RoleAdmin,
		Status:  "",
	}
	bearer := randomBearer(t)
	if err := auth.BootstrapInternalAdmin(ctx, m, auth.BootstrapAdminConfig{Token: bearer}); err != nil {
		t.Fatal(err)
	}
	a, _ := m.GetActor(ctx, auth.InternalAdminActorID)
	if a.Status != domain.ActorStatusActive {
		t.Fatalf("empty status not healed: %q", a.Status)
	}
	if _, err := auth.NewService(m).ValidateToken(ctx, bearer); err != nil {
		t.Fatalf("bearer refused after heal: %v", err)
	}
}

// ── AC 11: a rollback never un-revokes ──

func TestBootstrapSecurity_RevokedCurrentBearerStaysRevoked(t *testing.T) {
	ctx := context.Background()
	logs := captureBootstrapLog(t)
	m := newMemAuth()
	a, b := randomBearer(t), randomBearer(t)

	for _, tok := range []string{a, b, a} { // rotate A -> B, then roll back to A
		if err := auth.BootstrapInternalAdmin(ctx, m, auth.BootstrapAdminConfig{Token: tok}); err != nil {
			t.Fatal(err)
		}
	}

	svc := auth.NewService(m)
	if _, err := svc.ValidateToken(ctx, a); err == nil {
		t.Fatal("rolled-back bearer A was un-revoked")
	}
	if _, err := svc.ValidateToken(ctx, b); err != nil {
		t.Fatalf("bearer B revoked by a rollback to A: %v", err)
	}

	var errLine string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "revoked bootstrap admin token") {
			errLine = line
			break
		}
	}
	if errLine == "" || !strings.Contains(errLine, "level=ERROR") {
		t.Fatalf("rollback to a revoked bearer not logged at ERROR; log:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), a) || strings.Contains(logs.String(), b) {
		t.Fatal("log leaks a bearer")
	}
}

// ── AC 12: rollout cutoff ──

// Rolling deploy: a new replica (bearer B, booted now) seeds a fresh
// workspace, then an old replica (bearer A, booted an hour ago) loads
// it. A's row is now the newest, so ordering by insertion alone would
// revoke B; the old replica's process-start cutoff must stop it.
func TestBootstrapSecurity_OldReplicaCannotRevokeNewerBearer(t *testing.T) {
	ctx := context.Background()
	m := newMemAuth()
	a, b := randomBearer(t), randomBearer(t)
	oldReplicaStart := time.Now().UTC().Add(-time.Hour)
	newReplicaStart := time.Now().UTC()

	if err := auth.BootstrapInternalAdmin(ctx, m, auth.BootstrapAdminConfig{Token: b, SupersededBefore: newReplicaStart}); err != nil {
		t.Fatal(err)
	}
	if err := auth.BootstrapInternalAdmin(ctx, m, auth.BootstrapAdminConfig{Token: a, SupersededBefore: oldReplicaStart}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.NewService(m).ValidateToken(ctx, b); err != nil {
		t.Fatalf("old replica revoked the newer bearer: %v", err)
	}
}

// A non-zero cutoff still revokes rows from before it: the cutoff
// narrows the sweep, it does not switch it off.
func TestBootstrapSecurity_CutoffStillRevokesRowsBeforeIt(t *testing.T) {
	ctx := context.Background()
	m := newMemAuth()
	m.seedLegacyBootstrapRow("bootstrap-0123456789ab", "hash-before-boot", time.Now().UTC().Add(-2*time.Hour))
	m.seedLegacyBootstrapRow("bootstrap-ba9876543210", "hash-after-boot", time.Now().UTC().Add(-time.Minute))
	m.actors[auth.InternalAdminActorID] = domain.Actor{ActorID: auth.InternalAdminActorID, Type: domain.ActorTypeAutomated,
		Name: "SMP Bootstrap Admin", Role: domain.RoleAdmin, Status: domain.ActorStatusActive}

	cutoff := time.Now().UTC().Add(-time.Hour)
	if err := auth.BootstrapInternalAdmin(ctx, m, auth.BootstrapAdminConfig{Token: randomBearer(t), SupersededBefore: cutoff}); err != nil {
		t.Fatal(err)
	}
	if !m.isRevoked("bootstrap-0123456789ab") {
		t.Error("row created before the cutoff not revoked")
	}
	if m.isRevoked("bootstrap-ba9876543210") {
		t.Error("row created after the cutoff revoked")
	}
}

// ── AC 13: only bootstrap rows are swept ──

func TestBootstrapSecurity_AdminMintedTokensSurviveRotation(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	if err := auth.BootstrapInternalAdmin(ctx, st, auth.BootstrapAdminConfig{Token: "bearer-one-0123456789abcdef0123456789"}); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(st)
	// Even a tok_ token that reuses the bootstrap row's name is an
	// admin-minted credential, not a superseded platform bearer.
	minted, _, err := svc.CreateToken(ctx, auth.InternalAdminActorID, "smp-admin-bootstrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Make the minted row strictly older than the rotation so only the
	// id-prefix rule can spare it.
	for _, e := range st.tokens {
		if strings.HasPrefix(e.token.TokenID, "tok_") {
			e.token.CreatedAt = e.token.CreatedAt.Add(-time.Hour)
		}
	}
	if err := auth.BootstrapInternalAdmin(ctx, st, auth.BootstrapAdminConfig{Token: "bearer-two-0123456789abcdef0123456789"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ValidateToken(ctx, minted); err != nil {
		t.Fatalf("rotation revoked an admin-minted (tok_) token: %v", err)
	}
}

// ── AC 21: revocation error paths ──

// sweepErrStore injects failures into the two calls the rotation sweep
// makes, on top of a working memAuth.
type sweepErrStore struct {
	*memAuth
	listErr        error
	revokeErr      error
	notFoundFirstN int // first N RevokeToken calls: mark revoked "by another loader" and return NotFound
	mu             sync.Mutex
	revokeCalls    []string
}

func (s *sweepErrStore) ListTokensByActor(ctx context.Context, actorID string) ([]domain.Token, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.memAuth.ListTokensByActor(ctx, actorID)
}

func (s *sweepErrStore) RevokeToken(ctx context.Context, id string) error {
	s.mu.Lock()
	s.revokeCalls = append(s.revokeCalls, id)
	n := len(s.revokeCalls)
	s.mu.Unlock()
	if s.revokeErr != nil {
		return s.revokeErr
	}
	if n <= s.notFoundFirstN {
		// Another workspace loader revoked it first.
		if err := s.memAuth.RevokeToken(ctx, id); err != nil {
			return err
		}
		return domain.NewError(domain.ErrNotFound, "token not found or already revoked")
	}
	return s.memAuth.RevokeToken(ctx, id)
}

func newSweepFixture() *sweepErrStore {
	m := newMemAuth()
	m.actors[auth.InternalAdminActorID] = domain.Actor{ActorID: auth.InternalAdminActorID, Type: domain.ActorTypeAutomated,
		Name: "SMP Bootstrap Admin", Role: domain.RoleAdmin, Status: domain.ActorStatusActive}
	old := time.Now().UTC().Add(-time.Hour)
	m.seedLegacyBootstrapRow("bootstrap-aaaaaaaaaaaa", "hash-a", old)
	m.seedLegacyBootstrapRow("bootstrap-bbbbbbbbbbbb", "hash-b", old.Add(time.Minute))
	return &sweepErrStore{memAuth: m}
}

func TestBootstrapSecurity_RevocationErrorPaths(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")

	t.Run("list failure is returned", func(t *testing.T) {
		s := newSweepFixture()
		s.listErr = boom
		err := auth.BootstrapInternalAdmin(ctx, s, auth.BootstrapAdminConfig{Token: randomBearer(t)})
		if err == nil || !errors.Is(err, boom) || !strings.Contains(err.Error(), "list bootstrap admin tokens") {
			t.Fatalf("err = %v, want a wrapped boom mentioning \"list bootstrap admin tokens\"", err)
		}
	})

	t.Run("revoke failure is returned with the token id", func(t *testing.T) {
		s := newSweepFixture()
		s.revokeErr = boom
		err := auth.BootstrapInternalAdmin(ctx, s, auth.BootstrapAdminConfig{Token: randomBearer(t)})
		if err == nil || !errors.Is(err, boom) || !strings.Contains(err.Error(), "revoke superseded bootstrap admin token") {
			t.Fatalf("err = %v, want a wrapped boom mentioning \"revoke superseded bootstrap admin token\"", err)
		}
		if len(s.revokeCalls) == 0 || !strings.Contains(err.Error(), s.revokeCalls[0]) {
			t.Fatalf("err %v does not name the token id (calls %v)", err, s.revokeCalls)
		}
	})

	t.Run("concurrent not-found is skipped and the sweep continues", func(t *testing.T) {
		s := newSweepFixture()
		s.notFoundFirstN = 1
		if err := auth.BootstrapInternalAdmin(ctx, s, auth.BootstrapAdminConfig{Token: randomBearer(t)}); err != nil {
			t.Fatalf("not-found from a concurrent revoke must be skipped: %v", err)
		}
		if len(s.revokeCalls) != 2 {
			t.Fatalf("revoke calls %v, want both superseded rows attempted", s.revokeCalls)
		}
		for _, id := range []string{"bootstrap-aaaaaaaaaaaa", "bootstrap-bbbbbbbbbbbb"} {
			if !s.isRevoked(id) {
				t.Errorf("%s not revoked", id)
			}
		}
	})
}

// The sweep touches smp-admin's bootstrap rows only, never another
// actor's tokens, and never a row that is not named like a bootstrap
// row even if its id carries the prefix.
func TestBootstrapSecurity_SweepScope(t *testing.T) {
	ctx := context.Background()
	m := newMemAuth()
	m.actors[auth.InternalAdminActorID] = domain.Actor{ActorID: auth.InternalAdminActorID, Type: domain.ActorTypeAutomated,
		Name: "SMP Bootstrap Admin", Role: domain.RoleAdmin, Status: domain.ActorStatusActive}
	m.actors["human-1"] = domain.Actor{ActorID: "human-1", Type: domain.ActorTypeHuman, Role: domain.RoleAdmin, Status: domain.ActorStatusActive}
	old := time.Now().UTC().Add(-time.Hour)
	m.tokens["h-human"] = store.TokenRecord{TokenID: "bootstrap-111111111111", ActorID: "human-1", TokenHash: "h-human",
		Name: "smp-admin-bootstrap", CreatedAt: old}
	m.tokens["h-renamed"] = store.TokenRecord{TokenID: "bootstrap-222222222222", ActorID: auth.InternalAdminActorID,
		TokenHash: "h-renamed", Name: "something-else", CreatedAt: old}

	if err := auth.BootstrapInternalAdmin(ctx, m, auth.BootstrapAdminConfig{Token: randomBearer(t)}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bootstrap-111111111111", "bootstrap-222222222222"} {
		if m.isRevoked(id) {
			t.Errorf("%s revoked; outside the sweep's scope", id)
		}
	}
}

// Sanity: the fixtures above model Postgres's RevokeToken contract.
func TestMemAuthRevokeContract(t *testing.T) {
	m := newMemAuth()
	m.seedLegacyBootstrapRow("bootstrap-x", "h", time.Now())
	if err := m.RevokeToken(context.Background(), "bootstrap-x"); err != nil {
		t.Fatal(err)
	}
	err := m.RevokeToken(context.Background(), "bootstrap-x")
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrNotFound {
		t.Fatalf("second revoke: %v, want ErrNotFound", err)
	}
}
