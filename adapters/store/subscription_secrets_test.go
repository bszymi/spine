package store

// INIT-023 TASK-009 regression tests (audit P3-10, P3-1) for bound
// subscription signing secrets and the re-encryption sweep.
//
// memSubs is an in-memory poolQuerier modelled on the audit PoC
// poc/verify-r2-1: it records the RAW signing_secret column, so the tests
// see exactly what a database dump would hold. No database is needed, so
// these run in CI's plain `go test ./...`. The real-Postgres
// compare-and-swap tests live in postgres_subscription_secrets_integration_test.go.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Column indexes in the 12-column SELECT order of postgres_subscriptions.go.
const (
	colID     = 0
	colWS     = 1
	colSecret = 6
)

type memSubs struct {
	rows  map[string][]any
	execs int
	// secretWrites counts UpdateSubscription statements that write the
	// signing_secret column.
	secretWrites int
	// beforeCAS runs just before a sweep compare-and-swap is applied.
	beforeCAS func(id string)
	// queryErr, when set, fails every Query.
	queryErr error
	// execErr, when set, fails every compare-and-swap Exec.
	execErr error
}

func newMemSubs() *memSubs { return &memSubs{rows: map[string][]any{}} }

func (m *memSubs) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("memSubs: no tx") }
func (m *memSubs) Ping(context.Context) error            { return nil }

func (m *memSubs) Exec(_ context.Context, sql string, a ...any) (pgconn.CommandTag, error) {
	s := strings.Join(strings.Fields(sql), " ")
	switch {
	case strings.HasPrefix(s, "INSERT INTO runtime.event_subscriptions"):
		m.execs++
		var ws any
		if p, ok := a[1].(*string); ok && p != nil {
			ws = *p
		}
		m.rows[a[0].(string)] = []any{a[0], ws, a[2], a[3], a[4], a[5], a[6], a[7], a[8], a[9], a[10], a[11]}
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	case strings.Contains(s, "SET signing_secret = $2 WHERE subscription_id = $1 AND signing_secret = $3"):
		if m.execErr != nil {
			return pgconn.CommandTag{}, m.execErr
		}
		id := a[0].(string)
		if m.beforeCAS != nil {
			m.beforeCAS(id)
		}
		m.execs++
		r, ok := m.rows[id]
		if !ok || r[colSecret] != a[2] {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		}
		r[colSecret] = a[1]
		return pgconn.NewCommandTag("UPDATE 1"), nil
	case strings.Contains(s, "SET signing_secret = $2, updated_at = now() WHERE subscription_id = $1"):
		m.execs++
		r, ok := m.rows[a[0].(string)]
		if !ok {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		}
		r[colSecret] = a[1]
		return pgconn.NewCommandTag("UPDATE 1"), nil
	case strings.HasPrefix(s, "UPDATE runtime.event_subscriptions SET name") && strings.Contains(s, "signing_secret = $6"):
		m.execs++
		m.secretWrites++
		r, ok := m.rows[a[0].(string)]
		if !ok {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		}
		r[2], r[3], r[4], r[5], r[6], r[7], r[8] = a[1], a[2], a[3], a[4], a[5], a[6], a[7]
		return pgconn.NewCommandTag("UPDATE 1"), nil
	case strings.HasPrefix(s, "UPDATE runtime.event_subscriptions SET name") && !strings.Contains(s, "signing_secret"):
		// The bound update that keeps the stored secret: every mutable
		// column except signing_secret.
		m.execs++
		r, ok := m.rows[a[0].(string)]
		if !ok {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		}
		r[2], r[3], r[4], r[5], r[7], r[8] = a[1], a[2], a[3], a[4], a[5], a[6]
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	return pgconn.CommandTag{}, fmt.Errorf("memSubs: unsupported exec %q", s)
}

func (m *memSubs) ids() []string {
	ids := make([]string, 0, len(m.rows))
	for id := range m.rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (m *memSubs) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	sweep := strings.Contains(sql, "SELECT subscription_id, signing_secret FROM")
	var out [][]any
	for _, id := range m.ids() {
		r := m.rows[id]
		if sweep {
			out = append(out, []any{r[colID], r[colSecret]})
		} else {
			out = append(out, r)
		}
	}
	return &memRows{d: out, i: -1}, nil
}

func (m *memSubs) QueryRow(_ context.Context, sql string, a ...any) pgx.Row {
	r, ok := m.rows[a[0].(string)]
	if !ok {
		return memRow{err: pgx.ErrNoRows}
	}
	if strings.HasPrefix(strings.TrimSpace(sql), "SELECT signing_secret FROM") {
		return memRow{v: []any{r[colSecret]}}
	}
	return memRow{v: r}
}

func assignScan(dest, vals []any) error {
	for i := range dest {
		dv := reflect.ValueOf(dest[i]).Elem()
		vv := reflect.ValueOf(vals[i])
		if !vv.IsValid() {
			dv.Set(reflect.Zero(dv.Type()))
			continue
		}
		if dv.Kind() == reflect.Pointer && vv.Kind() != reflect.Pointer {
			p := reflect.New(dv.Type().Elem())
			p.Elem().Set(vv)
			dv.Set(p)
			continue
		}
		dv.Set(vv)
	}
	return nil
}

type memRow struct {
	v   []any
	err error
}

func (r memRow) Scan(d ...any) error {
	if r.err != nil {
		return r.err
	}
	return assignScan(d, r.v)
}

type memRows struct {
	d [][]any
	i int
}

func (r *memRows) Close()                                       {}
func (r *memRows) Err() error                                   { return nil }
func (r *memRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *memRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *memRows) Next() bool                                   { r.i++; return r.i < len(r.d) }
func (r *memRows) Scan(d ...any) error                          { return assignScan(d, r.d[r.i]) }
func (r *memRows) Values() ([]any, error)                       { return r.d[r.i], nil }
func (r *memRows) RawValues() [][]byte                          { return nil }
func (r *memRows) Conn() *pgx.Conn                              { return nil }

// ── fixtures ──

var ctxBG = context.Background()

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, spinecrypto.EncryptionKeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func testRing(t *testing.T, primary []byte, prev ...[]byte) *spinecrypto.SecretCipher {
	t.Helper()
	c, err := spinecrypto.NewSecretCipherRing(primary, prev...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func boundStore(t *testing.T, db *memSubs, c *spinecrypto.SecretCipher, ws string, mode SecretEnvelopeMode) *PostgresStore {
	t.Helper()
	s := NewPostgresStoreWithQuerier(db)
	s.SetSecretCipher(c)
	if err := s.SetSubscriptionSecretBinding(SubscriptionSecretBinding{WorkspaceID: ws, Mode: mode}); err != nil {
		t.Fatal(err)
	}
	return s
}

func legacyStore(db *memSubs, c *spinecrypto.SecretCipher) *PostgresStore {
	s := NewPostgresStoreWithQuerier(db)
	s.SetSecretCipher(c)
	return s
}

func newSub(id, secret string) *EventSubscription {
	now := time.Now().UTC()
	return &EventSubscription{
		SubscriptionID: id, Name: "n-" + id, TargetType: "webhook", TargetURL: "https://hook.invalid/h",
		EventTypes: []string{}, SigningSecret: secret, Status: "active", Metadata: []byte("{}"),
		CreatedBy: "t", CreatedAt: now, UpdatedAt: now,
	}
}

func mustCreate(t *testing.T, s *PostgresStore, id, secret string) {
	t.Helper()
	if err := s.CreateSubscription(ctxBG, newSub(id, secret)); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

func rawSecret(db *memSubs, id string) string { return db.rows[id][colSecret].(string) }

func setRaw(db *memSubs, id, v string) { db.rows[id][colSecret] = v }

// sealFor builds a value as a store bound to (ws, id) under c would.
func sealFor(t *testing.T, c *spinecrypto.SecretCipher, ws, id, secret string) string {
	t.Helper()
	v, err := c.Seal(secret, subscriptionSecretAAD(ws, id))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func encryptV1(t *testing.T, c *spinecrypto.SecretCipher, secret string) string {
	t.Helper()
	v, err := c.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// oldBinaryWriteBack is what a 61994fe binary does to a row it reads and
// writes back (a pause/activate handler, SMP's ensureScopedTargetURL):
// its Decrypt returns an unknown-prefix value verbatim and its
// IsEncrypted knows only enc:v1:, so its write path seals the enc:v2
// text itself as enc:v1 (design-check probes X1, X5).
func oldBinaryWriteBack(t *testing.T, c *spinecrypto.SecretCipher, stored string) string {
	t.Helper()
	if strings.HasPrefix(stored, "enc:v1:") {
		t.Fatal("oldBinaryWriteBack expects a value an old binary would not recognise")
	}
	return encryptV1(t, c, stored)
}

// ── AC6: binding validation and legacy mode ──

func TestSetSubscriptionSecretBinding_Validation(t *testing.T) {
	s := NewPostgresStoreWithQuerier(newMemSubs())
	for _, ws := range []string{"", "   "} {
		if err := s.SetSubscriptionSecretBinding(SubscriptionSecretBinding{WorkspaceID: ws}); err == nil {
			t.Fatalf("empty workspace id %q accepted", ws)
		}
	}
	if err := s.SetSubscriptionSecretBinding(SubscriptionSecretBinding{WorkspaceID: "ws", Mode: "Strict"}); err == nil {
		t.Fatal("non-canonical mode accepted by the setter (use ParseSecretEnvelopeMode)")
	}
	if err := s.SetSubscriptionSecretBinding(SubscriptionSecretBinding{WorkspaceID: "ws", Mode: "bogus"}); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if _, ok := s.SubscriptionSecretBinding(); ok {
		t.Fatal("a refused binding must not be installed")
	}
	if err := s.SetSubscriptionSecretBinding(SubscriptionSecretBinding{WorkspaceID: "ws"}); err != nil {
		t.Fatal(err)
	}
	if b, ok := s.SubscriptionSecretBinding(); !ok || b.Mode != SecretEnvelopeMigrate || b.WorkspaceID != "ws" {
		t.Fatalf(`"" must map to migrate: %+v %v`, b, ok)
	}
}

func TestParseSecretEnvelopeMode(t *testing.T) {
	cases := []struct {
		in   string
		want SecretEnvelopeMode
		ok   bool
	}{
		{"", SecretEnvelopeMigrate, true},
		{"migrate", SecretEnvelopeMigrate, true},
		{"Strict", SecretEnvelopeStrict, true},
		{" DOWNGRADE ", SecretEnvelopeDowngrade, true},
		{"bogus", "", false},
		{"strictly", "", false},
	}
	for _, tc := range cases {
		got, err := ParseSecretEnvelopeMode(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParseSecretEnvelopeMode(%q) = %q, %v", tc.in, got, err)
		}
	}
}

// With NO binding the store is what it was at 61994fe: enc:v1 writes,
// plaintext and enc:v1 reads, envelope inputs passed through, no sweep.
// This is the SMP compatibility guard at unit level.
func TestLegacyMode_Unchanged(t *testing.T) {
	c := testRing(t, testKey(t))
	db := newMemSubs()
	s := legacyStore(db, c)
	mustCreate(t, s, "sub-1", "whsec")
	if !strings.HasPrefix(rawSecret(db, "sub-1"), "enc:v1:") {
		t.Fatalf("legacy mode no longer writes enc:v1: %q", rawSecret(db, "sub-1"))
	}
	mustCreate(t, NewPostgresStoreWithQuerier(db), "sub-p", "legacy-plaintext")
	subs, err := s.ListSubscriptions(ctxBG, "")
	if err != nil || len(subs) != 2 {
		t.Fatalf("legacy list: %v", err)
	}
	for _, sub := range subs {
		want := map[string]string{"sub-1": "whsec", "sub-p": "legacy-plaintext"}[sub.SubscriptionID]
		if sub.SigningSecret != want {
			t.Fatalf("%s: got %q", sub.SubscriptionID, sub.SigningSecret)
		}
	}
	// Unbound writes still pass an existing envelope through verbatim.
	v1 := encryptV1(t, c, "passed-through")
	mustCreate(t, s, "sub-env", v1)
	if rawSecret(db, "sub-env") != v1 {
		t.Fatal("unbound write no longer passes an envelope through")
	}
	// Documented embedder delta (CHANGELOG "For embedders"): IsEncrypted
	// now covers enc:v2:, so an unbound write passes an enc:v2:-prefixed
	// input through verbatim too, where 61994fe sealed it as enc:v1.
	v2In := sealFor(t, c, "ws", "sub-env2", "x")
	mustCreate(t, s, "sub-env2", v2In)
	if rawSecret(db, "sub-env2") != v2In {
		t.Fatal("unbound write of an enc:v2 input: documented pass-through changed")
	}
	delete(db.rows, "sub-env2")
	// Unbound updates keep the historical single UPDATE that always
	// writes the secret column (no extra read).
	sub1, err := s.GetSubscription(ctxBG, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if sub1.secretRead || sub1.readSecret != "" {
		t.Fatal("an unbound read recorded read provenance; unbound values must be exactly as before")
	}
	if err := s.UpdateSubscription(ctxBG, sub1); err != nil || db.secretWrites != 1 || !strings.HasPrefix(rawSecret(db, "sub-1"), "enc:v1:") {
		t.Fatalf("unbound update no longer rewrites the secret column: %v", err)
	}
	before := db.execs
	if _, err := s.ReencryptSubscriptionSecrets(ctxBG); !errors.Is(err, ErrNoSecretBinding) {
		t.Fatalf("sweep without binding: %v", err)
	}
	if db.execs != before {
		t.Fatal("sweep without binding wrote")
	}
	// The unbound read delta: an enc:v2 row is an error, not "plaintext".
	setRaw(db, "sub-p", sealFor(t, c, "ws", "sub-p", "x"))
	if _, err := s.GetSubscription(ctxBG, "sub-p"); !errors.Is(err, spinecrypto.ErrMissingAAD) {
		t.Fatalf("unbound read of enc:v2: %v", err)
	}
}

func TestBoundStore_NilCipherFailsClosed(t *testing.T) {
	db := newMemSubs()
	mustCreate(t, NewPostgresStoreWithQuerier(db), "sub-p", "plain")
	s := NewPostgresStoreWithQuerier(db)
	if err := s.SetSubscriptionSecretBinding(SubscriptionSecretBinding{WorkspaceID: "ws"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSubscription(ctxBG, newSub("sub-1", "whsec")); !errors.Is(err, ErrSecretCipherRequired) {
		t.Fatalf("bound write without cipher: %v", err)
	}
	if _, ok := db.rows["sub-1"]; ok {
		t.Fatal("refused write was stored")
	}
	if err := s.CreateSubscription(ctxBG, newSub("sub-e", "")); err != nil {
		t.Fatalf("empty secret must still be storable: %v", err)
	}
	if _, err := s.GetSubscription(ctxBG, "sub-p"); !errors.Is(err, ErrSecretCipherRequired) {
		t.Fatalf("bound read without cipher: %v", err)
	}
	if _, err := s.ReencryptSubscriptionSecrets(ctxBG); !errors.Is(err, ErrSecretCipherRequired) {
		t.Fatalf("sweep without cipher: %v", err)
	}
	// A bound write needs the subscription id it binds to.
	b := boundStore(t, db, testRing(t, testKey(t)), "ws", SecretEnvelopeMigrate)
	if err := b.CreateSubscription(ctxBG, newSub("", "whsec")); err == nil {
		t.Fatal("bound write without a subscription id accepted")
	}
}

// ── AC7: P3-10 at the store level ──

// Two workspaces share one deployment-wide key over two separate
// databases. Workspace A's sealed row is copied into B's row with the
// same subscription id, and B's workspace_id column is rewritten to
// "ws-A" too: the column is never trusted, so nothing opens.
func TestP310_CrossWorkspaceTransplantRefused(t *testing.T) {
	c := testRing(t, testKey(t))
	dbA, dbB := newMemSubs(), newMemSubs()
	a := boundStore(t, dbA, c, "ws-A", SecretEnvelopeMigrate)
	mustCreate(t, a, "sub-1", "whsec_A")
	if !strings.HasPrefix(rawSecret(dbA, "sub-1"), "enc:v2:"+c.PrimaryKeyID()+":") {
		t.Fatalf("bound store did not write enc:v2 under the primary key: %q", rawSecret(dbA, "sub-1")[:7])
	}
	b := boundStore(t, dbB, c, "ws-B", SecretEnvelopeMigrate)
	mustCreate(t, b, "sub-1", "whsec_B")
	stolen := rawSecret(dbA, "sub-1")
	setRaw(dbB, "sub-1", stolen) // the transplant
	dbB.rows["sub-1"][colWS] = "ws-A"

	if _, err := b.GetSubscription(ctxBG, "sub-1"); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("transplanted secret opened in workspace B: %v", err)
	}
	if _, err := b.ListSubscriptions(ctxBG, ""); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("ListSubscriptions opened the transplant: %v", err)
	}
	if _, err := b.ListActiveSubscriptionsByEventType(ctxBG, "x"); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("ListActiveSubscriptionsByEventType opened the transplant: %v", err)
	}
	rep, err := b.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil || len(rep.Refused) != 1 || rep.Rewritten != 0 || rep.Clean() {
		t.Fatalf("sweep laundered the transplant: %+v %v", rep, err)
	}
	if rawSecret(dbB, "sub-1") != stolen {
		t.Fatal("sweep rewrote a row it could not open")
	}
	if iss := rep.Refused[0]; iss.SubscriptionID != "sub-1" || iss.Envelope != spinecrypto.EnvelopeV2 || iss.KeyID != c.PrimaryKeyID() {
		t.Fatalf("refusal record: %+v", iss)
	}
	// The own row still opens in A.
	if got, err := a.GetSubscription(ctxBG, "sub-1"); err != nil || got.SigningSecret != "whsec_A" {
		t.Fatalf("own row: %v", err)
	}
}

// ── AC8: strict mode ──

func TestP310_StrictRefusesLegacyEnvelopes(t *testing.T) {
	c := testRing(t, testKey(t))
	v1A := encryptV1(t, c, "whsec_A") // from a pre-upgrade backup of workspace A
	db := newMemSubs()
	s := boundStore(t, db, c, "ws-B", SecretEnvelopeStrict)
	mustCreate(t, s, "sub-1", "whsec_B")
	mustCreate(t, s, "sub-2", "whsec_B2")
	setRaw(db, "sub-1", v1A)
	setRaw(db, "sub-2", "attacker-known")
	for _, id := range []string{"sub-1", "sub-2"} {
		if _, err := s.GetSubscription(ctxBG, id); !errors.Is(err, ErrLegacySecretRefused) {
			t.Fatalf("%s: legacy value accepted in strict mode: %v", id, err)
		}
	}
	rep, err := s.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil || len(rep.Refused) != 2 || rep.Rewritten != 0 {
		t.Fatalf("strict sweep converted legacy rows: %+v %v", rep, err)
	}
	if rawSecret(db, "sub-1") != v1A || rawSecret(db, "sub-2") != "attacker-known" {
		t.Fatal("strict sweep changed the stored bytes")
	}
	// Strict still reads its own v2 rows.
	mustCreate(t, s, "sub-3", "whsec_3")
	if got, err := s.GetSubscription(ctxBG, "sub-3"); err != nil || got.SigningSecret != "whsec_3" {
		t.Fatalf("strict own row: %v", err)
	}
}

// ── AC9: bound writes refuse envelope inputs (Decision 13, probe X4) ──

func TestBoundWrite_RefusesEnvelopeInput(t *testing.T) {
	for _, mode := range []SecretEnvelopeMode{SecretEnvelopeMigrate, SecretEnvelopeStrict, SecretEnvelopeDowngrade} {
		t.Run(string(mode), func(t *testing.T) {
			c := testRing(t, testKey(t))
			db := newMemSubs()
			s := boundStore(t, db, c, "ws-B", mode)
			mustCreate(t, s, "sub-1", "whsec_B")
			before := rawSecret(db, "sub-1")
			inputs := map[string]string{
				"foreign v1":  encryptV1(t, c, "whsec_A"),
				"own-row v2":  sealFor(t, c, "ws-B", "sub-1", "whsec_B"),
				"unknown env": "enc:v9:x",
			}
			for name, in := range inputs {
				err := s.CreateSubscription(ctxBG, newSub("sub-new", in))
				if !errors.Is(err, ErrEnvelopeInput) {
					t.Fatalf("Create(%s): %v", name, err)
				}
				if _, ok := db.rows["sub-new"]; ok {
					t.Fatalf("Create(%s) stored a row", name)
				}
				sub := newSub("sub-1", in)
				if err := s.UpdateSubscription(ctxBG, sub); !errors.Is(err, ErrEnvelopeInput) {
					t.Fatalf("Update(%s): %v", name, err)
				}
				if rawSecret(db, "sub-1") != before {
					t.Fatalf("Update(%s) changed the stored bytes", name)
				}
			}
			// A plaintext secret is sealed per the mode; "" is stored as "".
			mustCreate(t, s, "sub-p", "whsec_plain")
			want := "enc:v2:" + c.PrimaryKeyID() + ":"
			if mode == SecretEnvelopeDowngrade {
				want = "enc:v1:"
			}
			if !strings.HasPrefix(rawSecret(db, "sub-p"), want) {
				t.Fatalf("plaintext not sealed as %s", want)
			}
			if got, err := s.GetSubscription(ctxBG, "sub-p"); err != nil || got.SigningSecret != "whsec_plain" {
				t.Fatalf("read-back: %v", err)
			}
			mustCreate(t, s, "sub-e", "")
			if rawSecret(db, "sub-e") != "" {
				t.Fatal("empty secret not stored as empty")
			}
		})
	}
}

// ── AC10: P3-1 sweep ──

// Port of poc/verify-r2-1 step 2: a plaintext row written before the key
// existed and a v1 row written by a keyed legacy store are re-encrypted
// by the sweep once the store is bound.
func TestP31_SweepEncryptsPlaintextAndV1(t *testing.T) {
	db := newMemSubs()
	mustCreate(t, NewPostgresStoreWithQuerier(db), "sub-p", "whsec_plain") // key unset: plaintext writer
	c := testRing(t, testKey(t))
	legacy := legacyStore(db, c) // keyed but unbound: v1 writer
	mustCreate(t, legacy, "sub-1", "whsec_v1")
	mustCreate(t, legacy, "sub-e", "")
	if rawSecret(db, "sub-p") != "whsec_plain" || !strings.HasPrefix(rawSecret(db, "sub-1"), "enc:v1:") {
		t.Fatal("fixture")
	}

	s := boundStore(t, db, c, "ws-X", SecretEnvelopeMigrate)
	rep, err := s.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil || rep.Rewritten != 2 || rep.Empty != 1 || rep.Scanned != 3 || !rep.Clean() {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	if rep.FromPlaintext != 1 || rep.FromV1 != 1 {
		t.Fatalf("breakdown: %+v", rep)
	}
	for id, want := range map[string]string{"sub-p": "whsec_plain", "sub-1": "whsec_v1"} {
		if !strings.HasPrefix(rawSecret(db, id), "enc:v2:"+c.PrimaryKeyID()+":") {
			t.Fatalf("%s not enc:v2 under the primary key after the sweep", id)
		}
		got, err := s.GetSubscription(ctxBG, id)
		if err != nil || got.SigningSecret != want {
			t.Fatalf("%s read-back: %v", id, err)
		}
	}
	before := db.execs
	rep, err = s.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil || rep.Rewritten != 0 || rep.Current != 2 || rep.Empty != 1 || db.execs != before {
		t.Fatalf("second sweep not a no-op: %+v execs %d->%d", rep, before, db.execs)
	}
}

// ── AC11: compare-and-swap ──

// A concurrent rotation between the sweep's read and its write wins.
func TestP31_SweepLosesToConcurrentWrite(t *testing.T) {
	c := testRing(t, testKey(t))
	db := newMemSubs()
	mustCreate(t, NewPostgresStoreWithQuerier(db), "sub-1", "old")
	s := boundStore(t, db, c, "ws-X", SecretEnvelopeMigrate)
	db.beforeCAS = func(id string) {
		db.beforeCAS = nil
		sub, err := s.GetSubscription(ctxBG, id)
		if err != nil {
			t.Fatal(err)
		}
		sub.SigningSecret = "rotated"
		if err := s.UpdateSubscription(ctxBG, sub); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil || rep.Conflicts != 1 || rep.Rewritten != 0 || rep.Clean() {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	got, err := s.GetSubscription(ctxBG, "sub-1")
	if err != nil || got.SigningSecret != "rotated" {
		t.Fatalf("sweep reverted a concurrent rotation: %v", err)
	}
	// The next sweep finds the rotated row current.
	if rep, err := s.ReencryptSubscriptionSecrets(ctxBG); err != nil || rep.Current != 1 || !rep.Clean() {
		t.Fatalf("follow-up sweep: %+v %v", rep, err)
	}
}

// Database errors return the partial report with the error.
func TestSweep_DatabaseErrors(t *testing.T) {
	c := testRing(t, testKey(t))
	db := newMemSubs()
	mustCreate(t, NewPostgresStoreWithQuerier(db), "sub-1", "plain")
	s := boundStore(t, db, c, "ws-X", SecretEnvelopeMigrate)
	db.queryErr = errors.New("connection reset")
	if _, err := s.ReencryptSubscriptionSecrets(ctxBG); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("query error: %v", err)
	}
	db.queryErr = nil
	db.execErr = errors.New("lock timeout")
	rep, err := s.ReencryptSubscriptionSecrets(ctxBG)
	if err == nil || !strings.Contains(err.Error(), "lock timeout") || rep.Scanned != 1 {
		t.Fatalf("exec error: %+v %v", rep, err)
	}
	if rawSecret(db, "sub-1") != "plain" {
		t.Fatal("failed write changed the row")
	}
}

// ── AC12: rotation and downgrade ──

func TestP310_RotationRekeysToPrimary(t *testing.T) {
	k1, k2 := testKey(t), testKey(t)
	c1 := testRing(t, k1)
	db := newMemSubs()
	mustCreate(t, boundStore(t, db, c1, "ws-X", SecretEnvelopeMigrate), "sub-1", "whsec_1")
	mustCreate(t, legacyStore(db, c1), "sub-2", "whsec_2")

	c21 := testRing(t, k2, k1)
	s21 := boundStore(t, db, c21, "ws-X", SecretEnvelopeMigrate)
	rep, err := s21.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil || rep.Rewritten != 2 || rep.Rekeyed < 1 || !rep.Clean() {
		t.Fatalf("rotation sweep: %+v %v", rep, err)
	}
	if rep.Rekeyed != 1 || rep.FromV1 != 1 {
		t.Fatalf("breakdown: %+v", rep)
	}
	s2 := boundStore(t, db, testRing(t, k2), "ws-X", SecretEnvelopeStrict)
	for id, want := range map[string]string{"sub-1": "whsec_1", "sub-2": "whsec_2"} {
		got, err := s2.GetSubscription(ctxBG, id)
		if err != nil || got.SigningSecret != want {
			t.Fatalf("%s after dropping the old key: %v", id, err)
		}
	}
}

// codex review (round 3): the key-rotation runbook's two rollouts. A
// process whose ring lacks K2 refuses a row the first K2-primary process
// re-keyed (so K2 must reach every ring before it is promoted); once K2 is
// in every ring, processes of the "distribute" phase (K1 primary, K2
// previous) and the "promote" phase (K2 primary, K1 previous) read each
// other's rows, even while their sweeps re-key rows back and forth.
func TestP310_TwoPhaseRotationOverlapReads(t *testing.T) {
	k1, k2 := testKey(t), testKey(t)
	db := newMemSubs()
	old := boundStore(t, db, testRing(t, k1), "ws-X", SecretEnvelopeMigrate)
	mustCreate(t, old, "sub-1", "whsec_1")

	promoted := boundStore(t, db, testRing(t, k2, k1), "ws-X", SecretEnvelopeMigrate)
	if rep, err := promoted.ReencryptSubscriptionSecrets(ctxBG); err != nil || rep.Rekeyed != 1 {
		t.Fatalf("promote sweep: %+v %v", rep, err)
	}
	if _, err := old.GetSubscription(ctxBG, "sub-1"); !errors.Is(err, spinecrypto.ErrUnknownKeyID) {
		t.Fatalf("a K1-only process must refuse a K2 row (why K2 is distributed first): %v", err)
	}

	distributed := boundStore(t, db, testRing(t, k1, k2), "ws-X", SecretEnvelopeMigrate)
	for i := 0; i < 2; i++ {
		for name, st := range map[string]*PostgresStore{"distributed": distributed, "promoted": promoted} {
			if got, err := st.GetSubscription(ctxBG, "sub-1"); err != nil || got.SigningSecret != "whsec_1" {
				t.Fatalf("round %d: %s process cannot read the row: %v", i, name, err)
			}
		}
		// The distribute-phase sweep re-keys back to K1; the promote-phase
		// sweep re-keys to K2 again. Both stay readable throughout.
		if rep, err := distributed.ReencryptSubscriptionSecrets(ctxBG); err != nil || !rep.Clean() {
			t.Fatalf("round %d: distribute sweep: %+v %v", i, rep, err)
		}
		if rep, err := promoted.ReencryptSubscriptionSecrets(ctxBG); err != nil || !rep.Clean() {
			t.Fatalf("round %d: promote sweep: %+v %v", i, rep, err)
		}
	}
	if !strings.HasPrefix(rawSecret(db, "sub-1"), "enc:v2:"+testRing(t, k2).PrimaryKeyID()+":") {
		t.Fatal("rows do not settle on K2 once only promote-phase processes sweep")
	}

	// Review round 2: the rows are on K2 only if a K2-primary sweep came
	// last. When the last sweep is a distribute-phase one (a step-2 process
	// that first loads the workspace late in the promote rollout), the rows
	// stay on K1 after every distribute-phase process is gone: nothing
	// re-keys them until a K2-primary process sweeps again, which is why the
	// runbook verifies with a fresh sweep after the rollout (the command, or
	// a restart in platform-binding mode) before K1 is dropped.
	if rep, err := distributed.ReencryptSubscriptionSecrets(ctxBG); err != nil || rep.Rekeyed != 1 {
		t.Fatalf("late distribute sweep: %+v %v", rep, err)
	}
	if !strings.HasPrefix(rawSecret(db, "sub-1"), "enc:v2:"+testRing(t, k1).PrimaryKeyID()+":") {
		t.Fatal("a late distribute-phase sweep should leave the row on K1")
	}
	k2Only := boundStore(t, db, testRing(t, k2), "ws-X", SecretEnvelopeMigrate)
	if _, err := k2Only.GetSubscription(ctxBG, "sub-1"); !errors.Is(err, spinecrypto.ErrUnknownKeyID) {
		t.Fatalf("dropping K1 now would strand the row: %v", err)
	}
	if rep, err := promoted.ReencryptSubscriptionSecrets(ctxBG); err != nil || rep.Rekeyed != 1 || !rep.Clean() {
		t.Fatalf("verification sweep after the rollout: %+v %v", rep, err)
	}
	if got, err := k2Only.GetSubscription(ctxBG, "sub-1"); err != nil || got.SigningSecret != "whsec_1" {
		t.Fatalf("after the verification sweep a K2-only ring must read the row: %v", err)
	}
}

func TestP310_DowngradeRewritesV2ToV1(t *testing.T) {
	c := testRing(t, testKey(t))
	db := newMemSubs()
	mustCreate(t, boundStore(t, db, c, "ws-X", SecretEnvelopeMigrate), "sub-1", "whsec")
	d := boundStore(t, db, c, "ws-X", SecretEnvelopeDowngrade)
	rep, err := d.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil || rep.Rewritten != 1 || rep.ToV1 != 1 || !rep.Clean() {
		t.Fatalf("downgrade sweep: %+v %v", rep, err)
	}
	if !strings.HasPrefix(rawSecret(db, "sub-1"), "enc:v1:") {
		t.Fatal("not enc:v1 after the downgrade sweep")
	}
	old := legacyStore(db, c) // what a pre-TASK-009 binary does
	if got, err := old.GetSubscription(ctxBG, "sub-1"); err != nil || got.SigningSecret != "whsec" {
		t.Fatalf("legacy reader after downgrade: %v", err)
	}
	if rep, err := d.ReencryptSubscriptionSecrets(ctxBG); err != nil || rep.Current != 1 || rep.Rewritten != 0 {
		t.Fatalf("second downgrade sweep: %+v %v", rep, err)
	}
}

// ── AC21: nested envelopes (I8, Decision 14; probes X1, X5) ──

func TestNested_OwnRowV2Repaired(t *testing.T) {
	c := testRing(t, testKey(t))
	db := newMemSubs()
	s := boundStore(t, db, c, "ws-B", SecretEnvelopeMigrate)
	mustCreate(t, s, "sub-1", "whsec_real")
	setRaw(db, "sub-1", oldBinaryWriteBack(t, c, rawSecret(db, "sub-1")))

	got, err := s.GetSubscription(ctxBG, "sub-1")
	if err != nil || got.SigningSecret != "whsec_real" {
		t.Fatalf("nested own row not repaired on read: %v", err)
	}
	rep, err := s.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil || rep.Rewritten != 1 || rep.Repaired != 1 || !rep.Clean() {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	if len(rep.Converted) != 1 || !rep.Converted[0].Repaired || rep.Converted[0].From != spinecrypto.EnvelopeV1 {
		t.Fatalf("conversion record: %+v", rep.Converted)
	}
	raw := rawSecret(db, "sub-1")
	if !strings.HasPrefix(raw, "enc:v2:"+c.PrimaryKeyID()+":") {
		t.Fatal("not a single-level enc:v2 after repair")
	}
	op, err := c.Open(raw, subscriptionSecretAAD("ws-B", "sub-1"))
	if err != nil || op.Plaintext != "whsec_real" {
		t.Fatalf("repaired value: %v", err)
	}
	before := db.execs
	if rep, err := s.ReencryptSubscriptionSecrets(ctxBG); err != nil || rep.Rewritten != 0 || rep.Current != 1 || db.execs != before {
		t.Fatalf("second sweep wrote: %+v %v", rep, err)
	}
}

func TestNested_ForeignV2Refused(t *testing.T) {
	c := testRing(t, testKey(t))
	db := newMemSubs()
	s := boundStore(t, db, c, "ws-B", SecretEnvelopeMigrate)
	mustCreate(t, s, "sub-1", "whsec_B")
	nested := encryptV1(t, c, sealFor(t, c, "ws-A", "sub-1", "whsec_A"))
	setRaw(db, "sub-1", nested)
	if _, err := s.GetSubscription(ctxBG, "sub-1"); !errors.Is(err, ErrNestedEnvelope) {
		t.Fatalf("foreign nested v2: %v", err)
	}
	rep, err := s.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil || len(rep.Refused) != 1 || rep.Rewritten != 0 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	if rawSecret(db, "sub-1") != nested {
		t.Fatal("sweep changed a refused nested row")
	}
}

func TestNested_StrictRefusesOuterV1(t *testing.T) {
	c := testRing(t, testKey(t))
	db := newMemSubs()
	s := boundStore(t, db, c, "ws-B", SecretEnvelopeStrict)
	mustCreate(t, s, "sub-1", "whsec_real")
	setRaw(db, "sub-1", oldBinaryWriteBack(t, c, rawSecret(db, "sub-1")))
	if _, err := s.GetSubscription(ctxBG, "sub-1"); !errors.Is(err, ErrLegacySecretRefused) {
		t.Fatalf("strict nested outer v1: %v", err)
	}
}

// nestedFixtures is every stored-value shape the AC6–AC12 and AC21 tests
// use, for one row (ws-B, sub-1), under ring (k2 primary, k1 previous).
func nestedFixtures(t *testing.T, k1, k2 []byte) map[string]string {
	c1, c2 := testRing(t, k1), testRing(t, k2)
	own2 := sealFor(t, c2, "ws-B", "sub-1", "whsec_own")
	own1 := sealFor(t, c1, "ws-B", "sub-1", "whsec_own_k1")
	return map[string]string{
		"empty":                     "",
		"plaintext":                 "whsec_plain",
		"near-miss plaintext":       "ENC:v2:not-an-envelope",
		"unknown version":           "enc:v9:x",
		"v1 primary":                encryptV1(t, c2, "whsec_v1"),
		"v1 previous":               encryptV1(t, c1, "whsec_v1_k1"),
		"v2 own primary":            own2,
		"v2 own previous":           own1,
		"v2 other workspace":        sealFor(t, c2, "ws-A", "sub-1", "whsec_A"),
		"v2 other subscription":     sealFor(t, c2, "ws-B", "sub-2", "whsec_2"),
		"v1(v2 own)":                encryptV1(t, c2, own2),
		"v1 previous(v2 own)":       encryptV1(t, c1, own1),
		"v2 own(v2 own)":            sealFor(t, c2, "ws-B", "sub-1", own2),
		"v1(v2 other workspace)":    encryptV1(t, c2, sealFor(t, c2, "ws-A", "sub-1", "whsec_A")),
		"v1(v1)":                    encryptV1(t, c2, encryptV1(t, c2, "whsec_x")),
		"v2 own(v1)":                sealFor(t, c2, "ws-B", "sub-1", encryptV1(t, c2, "whsec_x")),
		"v1(enc:v9)":                encryptV1(t, c2, "enc:v9:x"),
		"v1(v2 own(v2 own))":        encryptV1(t, c2, sealFor(t, c2, "ws-B", "sub-1", own2)),
		"v1(v2 own previous)":       encryptV1(t, c2, own1),
		"v2 own(empty)":             sealFor(t, c2, "ws-B", "sub-1", ""),
		"v1(plaintext near-miss)":   encryptV1(t, c2, "enc:v2"),
		"v2 own(v1 other key ring)": sealFor(t, c2, "ws-B", "sub-1", encryptV1(t, testRing(t, testKey(t)), "z")),
	}
}

// I8 across every mode and every fixture: no bound read returns an
// envelope as the signing secret, and no sweep writes a value whose
// opened plaintext is an envelope.
func TestNested_NeverReturnsEnvelope(t *testing.T) {
	k1, k2 := testKey(t), testKey(t)
	ringC := testRing(t, k2, k1)
	fixtures := nestedFixtures(t, k1, k2)
	plainish := func(v string) bool {
		e := spinecrypto.ClassifyEnvelope(v)
		return e == spinecrypto.EnvelopePlaintext || e == spinecrypto.EnvelopeEmpty
	}
	for _, mode := range []SecretEnvelopeMode{SecretEnvelopeMigrate, SecretEnvelopeStrict, SecretEnvelopeDowngrade} {
		for name, stored := range fixtures {
			t.Run(string(mode)+"/"+name, func(t *testing.T) {
				db := newMemSubs()
				s := boundStore(t, db, ringC, "ws-B", mode)
				mustCreate(t, s, "sub-1", "seed")
				setRaw(db, "sub-1", stored)
				if got, err := s.GetSubscription(ctxBG, "sub-1"); err == nil && !plainish(got.SigningSecret) {
					t.Fatalf("GetSubscription returned an envelope as the secret (%s)", spinecrypto.ClassifyEnvelope(got.SigningSecret))
				}
				if subs, err := s.ListActiveSubscriptionsByEventType(ctxBG, "x"); err == nil {
					for _, sub := range subs {
						if !plainish(sub.SigningSecret) {
							t.Fatal("list returned an envelope as the secret")
						}
					}
				}
				rep, err := s.ReencryptSubscriptionSecrets(ctxBG)
				if err != nil {
					t.Fatal(err)
				}
				if rep.Rewritten == 1 {
					raw := rawSecret(db, "sub-1")
					op, err := ringC.Open(raw, subscriptionSecretAAD("ws-B", "sub-1"))
					if err != nil || !plainish(op.Plaintext) {
						t.Fatalf("sweep wrote a value whose plaintext is an envelope (%v)", err)
					}
				}
				if got, err := s.GetSubscription(ctxBG, "sub-1"); err == nil && !plainish(got.SigningSecret) {
					t.Fatal("post-sweep read returned an envelope")
				}
			})
		}
	}
}

// ── AC22: per-row conversion record and the X2 trust boundary ──

func TestSweep_ReportsEachConversion(t *testing.T) {
	k1, k2 := testKey(t), testKey(t)
	c1, c2 := testRing(t, k1), testRing(t, k2)
	c21 := testRing(t, k2, k1)
	db := newMemSubs()
	s := boundStore(t, db, c21, "ws-X", SecretEnvelopeMigrate)
	for _, id := range []string{"a-plain", "b-v1-primary", "c-v1-previous", "d-v2-previous", "e-current"} {
		mustCreate(t, s, id, "whsec-"+id)
	}
	setRaw(db, "a-plain", "whsec-a-plain")
	setRaw(db, "b-v1-primary", encryptV1(t, c2, "whsec-b"))
	setRaw(db, "c-v1-previous", encryptV1(t, c1, "whsec-c"))
	setRaw(db, "d-v2-previous", sealFor(t, c1, "ws-X", "d-v2-previous", "whsec-d"))
	id1, id2 := c1.PrimaryKeyID(), c2.PrimaryKeyID()

	rep, err := s.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil || !rep.Clean() {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	want := []SecretSweepConversion{
		{SubscriptionID: "a-plain", From: spinecrypto.EnvelopePlaintext, FromKeyID: ""},
		{SubscriptionID: "b-v1-primary", From: spinecrypto.EnvelopeV1, FromKeyID: id2},
		{SubscriptionID: "c-v1-previous", From: spinecrypto.EnvelopeV1, FromKeyID: id1},
		{SubscriptionID: "d-v2-previous", From: spinecrypto.EnvelopeV2, FromKeyID: id1},
	}
	if !reflect.DeepEqual(rep.Converted, want) {
		t.Fatalf("Converted =\n %+v\nwant\n %+v", rep.Converted, want)
	}
	if len(rep.Converted) != rep.Rewritten || rep.Current != 1 {
		t.Fatalf("counts: %+v", rep)
	}
	if rep.FromPlaintext != 1 || rep.FromV1 != 2 || rep.Rekeyed != 1 || rep.ToV1 != 0 || rep.Repaired != 0 {
		t.Fatalf("breakdown: %+v", rep)
	}
	if rep.Scanned != rep.Rewritten+rep.Current+rep.Empty+rep.Conflicts+len(rep.Refused) {
		t.Fatalf("Scanned invariant: %+v", rep)
	}
}

// The X2 trust boundary, pinned as DOCUMENTED behaviour (TASK-009 §3
// design-check note "the laundering persists"; docs/integration-guide.md
// §2.1). In migrate mode an enc:v1 value sealed for ANOTHER workspace —
// exactly what a pre-upgrade backup of workspace A holds — opens (v1 has
// no associated data) and the sweep converts it into a valid enc:v2 row
// for this workspace. Nothing can tell it from a genuine pre-upgrade
// row. This is not an oversight: the conversion is recorded in Converted
// (and logged per row by workspace.PrepareSubscriptionSecrets) so it can
// be audited, and strict mode or retiring the v1 key closes the path.
func TestSweep_X2TrustBoundaryPinned(t *testing.T) {
	c := testRing(t, testKey(t))
	plantFromA := encryptV1(t, c, "whsec_A")
	db := newMemSubs()
	s := boundStore(t, db, c, "ws-B", SecretEnvelopeMigrate)
	mustCreate(t, s, "sub-x", "whsec_B")
	setRaw(db, "sub-x", plantFromA)
	rep, err := s.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil || rep.Rewritten != 1 || !rep.Clean() {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	if len(rep.Converted) != 1 || rep.Converted[0].SubscriptionID != "sub-x" || rep.Converted[0].From != spinecrypto.EnvelopeV1 {
		t.Fatalf("the conversion must be recorded: %+v", rep.Converted)
	}
	strict := boundStore(t, db, c, "ws-B", SecretEnvelopeStrict)
	if got, err := strict.GetSubscription(ctxBG, "sub-x"); err != nil || got.SigningSecret != "whsec_A" {
		t.Fatalf("documented boundary changed: %v", err)
	}
}

// Review round 1 (P2, Decision 15): a bound read-modify-write that does
// not change the secret — the gateway's PATCH, pause and activate
// handlers (GetSubscription → mutate → UpdateSubscription), a bootstrap
// that only realigns the target URL, SMP's ensureScopedTargetURL — must
// not convert a legacy value into enc:v2 behind the sweep's back. Before
// the fix, in migrate mode GetSubscription opened a copy of workspace A's
// pre-upgrade enc:v1 value planted after B's first clean sweep, and
// UpdateSubscription re-sealed it as B's enc:v2: the next sweep saw a
// current row and recorded nothing, and strict then accepted A's secret
// indefinitely with no audit trail. Now the stored bytes are left alone,
// so the next sweep converts the plant AND records it.
func TestBoundUpdate_UnchangedSecretKeepsStoredBytes(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*EventSubscription)
		check  func(*testing.T, *EventSubscription)
	}{
		{"patch", func(s *EventSubscription) {
			s.Name, s.TargetURL, s.EventTypes, s.Metadata = "renamed", "https://hook.invalid/new", []string{"run_failed"}, []byte(`{"k":1}`)
		}, func(t *testing.T, s *EventSubscription) {
			if s.Name != "renamed" || s.TargetURL != "https://hook.invalid/new" || !reflect.DeepEqual(s.EventTypes, []string{"run_failed"}) || string(s.Metadata) != `{"k":1}` {
				t.Fatalf("patched columns not written: %+v", s)
			}
		}},
		{"pause", func(s *EventSubscription) { s.Status = "paused" }, func(t *testing.T, s *EventSubscription) {
			if s.Status != "paused" {
				t.Fatalf("status not written: %q", s.Status)
			}
		}},
		{"activate", func(s *EventSubscription) { s.Status = "active" }, func(t *testing.T, s *EventSubscription) {
			if s.Status != "active" {
				t.Fatalf("status not written: %q", s.Status)
			}
		}},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			c := testRing(t, testKey(t))
			plantFromA := encryptV1(t, c, "whsec_A_receiver_key")
			db := newMemSubs()
			s := boundStore(t, db, c, "ws-B", SecretEnvelopeMigrate)
			mustCreate(t, s, "sub-1", "whsec_B")
			if rep, err := s.ReencryptSubscriptionSecrets(ctxBG); err != nil || !rep.Clean() || rep.Current != 1 {
				t.Fatalf("first clean sweep: %+v %v", rep, err)
			}
			if m.name == "activate" {
				db.rows["sub-1"][7] = "paused"
			}
			setRaw(db, "sub-1", plantFromA) // planted after B's first clean sweep

			// migrate accepts the plant on read (the documented X2 boundary).
			sub, err := s.GetSubscription(ctxBG, "sub-1")
			if err != nil || sub.SigningSecret != "whsec_A_receiver_key" {
				t.Fatalf("fixture read: %v", err)
			}
			m.mutate(sub)
			if err := s.UpdateSubscription(ctxBG, sub); err != nil {
				t.Fatalf("update: %v", err)
			}
			if rawSecret(db, "sub-1") != plantFromA || db.secretWrites != 0 {
				t.Fatal("a read-modify-write re-sealed the stored secret: an unrecorded legacy→enc:v2 conversion")
			}
			got, err := s.GetSubscription(ctxBG, "sub-1")
			if err != nil {
				t.Fatal(err)
			}
			m.check(t, got)

			// The next sweep converts the plant and records it, so the
			// operator's conversion-log review finds it.
			rep, err := s.ReencryptSubscriptionSecrets(ctxBG)
			if err != nil || rep.Rewritten != 1 || rep.FromV1 != 1 || len(rep.Converted) != 1 ||
				rep.Converted[0].SubscriptionID != "sub-1" || rep.Converted[0].From != spinecrypto.EnvelopeV1 {
				t.Fatalf("the plant must be converted by the sweep, with a record: %+v %v", rep, err)
			}
		})
	}

	// codex review: "kept" is decided from the caller's read, not from a
	// later read of the row. A rotation that lands between the PATCH's
	// read (which returned the plant) and its update must survive, and the
	// stale planted secret must not be re-sealed as enc:v2. Deciding from
	// a fresh read would see "rotated ≠ plant" and write the plant.
	t.Run("rotation between read and update", func(t *testing.T) {
		c := testRing(t, testKey(t))
		plantFromA := encryptV1(t, c, "whsec_A_receiver_key")
		db := newMemSubs()
		s := boundStore(t, db, c, "ws-B", SecretEnvelopeMigrate)
		mustCreate(t, s, "sub-1", "whsec_B")
		setRaw(db, "sub-1", plantFromA)
		sub, err := s.GetSubscription(ctxBG, "sub-1")
		if err != nil || sub.SigningSecret != "whsec_A_receiver_key" {
			t.Fatalf("fixture read: %v", err)
		}
		if err := s.ReplaceSubscriptionSecret(ctxBG, "sub-1", "whsec_rotated"); err != nil {
			t.Fatal(err)
		}
		rotated := rawSecret(db, "sub-1")
		writes := db.secretWrites
		sub.Status = "paused"
		if err := s.UpdateSubscription(ctxBG, sub); err != nil {
			t.Fatal(err)
		}
		if rawSecret(db, "sub-1") != rotated || db.secretWrites != writes {
			t.Fatal("the update re-sealed the stale planted secret over a concurrent rotation")
		}
		got, err := s.GetSubscription(ctxBG, "sub-1")
		if err != nil || got.SigningSecret != "whsec_rotated" || got.Status != "paused" {
			t.Fatalf("after the update: %v", err)
		}
		if rep, err := s.ReencryptSubscriptionSecrets(ctxBG); err != nil || rep.Current != 1 || rep.Rewritten != 0 {
			t.Fatalf("sweep: %+v %v", rep, err)
		}
	})

	// SMP's ensureScopedTargetURL pattern: list, mutate subs[i], update
	// &subs[i]. The listed value carries its read too.
	t.Run("list then update", func(t *testing.T) {
		c := testRing(t, testKey(t))
		db := newMemSubs()
		s := boundStore(t, db, c, "ws-B", SecretEnvelopeMigrate)
		mustCreate(t, s, "sub-1", "whsec_B")
		plant := encryptV1(t, c, "whsec_A")
		setRaw(db, "sub-1", plant)
		subs, err := s.ListSubscriptions(ctxBG, "")
		if err != nil || len(subs) != 1 {
			t.Fatalf("list: %v", err)
		}
		subs[0].TargetURL = "https://hook.invalid/scoped"
		if err := s.UpdateSubscription(ctxBG, &subs[0]); err != nil {
			t.Fatal(err)
		}
		if rawSecret(db, "sub-1") != plant || db.secretWrites != 0 {
			t.Fatal("a listed value's update re-sealed the kept secret")
		}
	})

	// A value that did not come from a bound read (a struct the caller
	// built) falls back to comparing against the stored value.
	t.Run("caller-built value falls back to the stored value", func(t *testing.T) {
		c := testRing(t, testKey(t))
		db := newMemSubs()
		s := boundStore(t, db, c, "ws-B", SecretEnvelopeMigrate)
		mustCreate(t, s, "sub-1", "whsec_B")
		plant := encryptV1(t, c, "whsec_A")
		setRaw(db, "sub-1", plant)
		built := newSub("sub-1", "whsec_A")
		built.Status = "paused"
		if err := s.UpdateSubscription(ctxBG, built); err != nil {
			t.Fatal(err)
		}
		if rawSecret(db, "sub-1") != plant || db.secretWrites != 0 || db.rows["sub-1"][7] != "paused" {
			t.Fatal("a caller-built update with the stored secret rewrote it")
		}
		built.SigningSecret = "whsec_set"
		if err := s.UpdateSubscription(ctxBG, built); err != nil || db.secretWrites != 1 {
			t.Fatalf("a caller-built update that sets a secret did not write it: %v", err)
		}
		if got, err := s.GetSubscription(ctxBG, "sub-1"); err != nil || got.SigningSecret != "whsec_set" {
			t.Fatalf("read-back: %v", err)
		}
	})

	// An unchanged secret on a current row is not rewritten either (no
	// fresh nonce, no needless write); a changed secret replaces the
	// stored value outright, carrying nothing over; a refused stored value
	// is replaced by the caller's secret as before; a missing row is not an
	// error, as before.
	t.Run("current row, changed secret, refused row, missing row", func(t *testing.T) {
		c := testRing(t, testKey(t))
		db := newMemSubs()
		s := boundStore(t, db, c, "ws-B", SecretEnvelopeMigrate)
		mustCreate(t, s, "sub-1", "whsec_B")
		current := rawSecret(db, "sub-1")
		sub, err := s.GetSubscription(ctxBG, "sub-1")
		if err != nil {
			t.Fatal(err)
		}
		sub.Name = "renamed"
		if err := s.UpdateSubscription(ctxBG, sub); err != nil || rawSecret(db, "sub-1") != current || db.secretWrites != 0 {
			t.Fatalf("unchanged secret on a current row was rewritten: %v", err)
		}
		sub.SigningSecret = "whsec_new"
		if err := s.UpdateSubscription(ctxBG, sub); err != nil || db.secretWrites != 1 {
			t.Fatalf("changed secret not written: %v", err)
		}
		if raw := rawSecret(db, "sub-1"); raw == current || !strings.HasPrefix(raw, "enc:v2:"+c.PrimaryKeyID()+":") {
			t.Fatal("changed secret not sealed for the binding")
		}
		if got, err := s.GetSubscription(ctxBG, "sub-1"); err != nil || got.SigningSecret != "whsec_new" || got.Name != "renamed" {
			t.Fatalf("read-back: %v", err)
		}
		setRaw(db, "sub-1", sealFor(t, c, "ws-A", "sub-1", "whsec_A")) // refused transplant
		sub.SigningSecret = "whsec_fresh"
		if err := s.UpdateSubscription(ctxBG, sub); err != nil {
			t.Fatalf("update over a refused row: %v", err)
		}
		if got, err := s.GetSubscription(ctxBG, "sub-1"); err != nil || got.SigningSecret != "whsec_fresh" {
			t.Fatalf("refused row not replaced: %v", err)
		}
		if err := s.UpdateSubscription(ctxBG, newSub("missing", "whsec_x")); err != nil {
			t.Fatalf("update of a missing row: %v", err)
		}
		if _, ok := db.rows["missing"]; ok {
			t.Fatal("update created a row")
		}
	})
}

// ── AC15: no secret material in reports or errors (I6) ──

func TestSweep_NoSecretMaterialInReportOrErrors(t *testing.T) {
	k1, k2 := testKey(t), testKey(t)
	ringC := testRing(t, k2, k1)
	fixtures := nestedFixtures(t, k1, k2)
	db := newMemSubs()
	s := boundStore(t, db, ringC, "ws-B", SecretEnvelopeStrict)
	var stored []string
	i := 0
	for _, v := range fixtures {
		id := fmt.Sprintf("sub-%02d", i)
		i++
		mustCreate(t, s, id, "seed")
		setRaw(db, id, v)
		stored = append(stored, v)
	}
	var errs []string
	for _, id := range db.ids() {
		if _, err := s.GetSubscription(ctxBG, id); err != nil {
			errs = append(errs, err.Error())
		}
	}
	rep, err := s.ReencryptSubscriptionSecrets(ctxBG)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Refused) == 0 {
		t.Fatal("fixture should produce refused rows")
	}
	haystack := fmt.Sprintf("%+v %v", rep, errs)
	for _, needle := range secretNeedles(stored) {
		if strings.Contains(haystack, needle) {
			t.Fatalf("report or error leaks secret material %q", needle)
		}
	}
}

// secretNeedles is every fixture plaintext plus every stored value's
// payload (an enc:v2 value's key id is non-secret and allowed).
func secretNeedles(stored []string) []string {
	needles := []string{"whsec_", "whsec-", "not-an-envelope"}
	for _, v := range stored {
		switch spinecrypto.ClassifyEnvelope(v) {
		case spinecrypto.EnvelopeV1:
			needles = append(needles, strings.TrimPrefix(v, "enc:v1:"))
		case spinecrypto.EnvelopeV2:
			needles = append(needles, v[strings.LastIndex(v, ":")+1:])
		case spinecrypto.EnvelopePlaintext:
			needles = append(needles, v)
		}
	}
	return needles
}

// ReplaceSubscriptionSecret repairs a refused row without opening it
// (codex review: rotate-secret must work on exactly the rows a bound
// store refuses).
func TestReplaceSubscriptionSecret_RepairsRefusedRow(t *testing.T) {
	c := testRing(t, testKey(t))
	db := newMemSubs()
	s := boundStore(t, db, c, "ws-B", SecretEnvelopeStrict)
	mustCreate(t, s, "sub-1", "whsec_B")
	setRaw(db, "sub-1", sealFor(t, c, "ws-A", "sub-1", "whsec_A")) // a transplant
	if _, err := s.GetSubscription(ctxBG, "sub-1"); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("fixture: %v", err)
	}
	if err := s.ReplaceSubscriptionSecret(ctxBG, "sub-1", "whsec_rotated"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !strings.HasPrefix(rawSecret(db, "sub-1"), "enc:v2:"+c.PrimaryKeyID()+":") {
		t.Fatal("replacement not sealed for the binding")
	}
	got, err := s.GetSubscription(ctxBG, "sub-1")
	if err != nil || got.SigningSecret != "whsec_rotated" {
		t.Fatalf("read-back: %v", err)
	}
	if rep, err := s.ReencryptSubscriptionSecrets(ctxBG); err != nil || !rep.Clean() || rep.Current != 1 {
		t.Fatalf("sweep after repair: %+v %v", rep, err)
	}
	// Missing row, empty secret and envelope input are refused.
	if err := s.ReplaceSubscriptionSecret(ctxBG, "missing", "whsec_x"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing row: %v", err)
	}
	if err := s.ReplaceSubscriptionSecret(ctxBG, "sub-1", ""); err == nil {
		t.Fatal("empty replacement accepted")
	}
	if err := s.ReplaceSubscriptionSecret(ctxBG, "sub-1", encryptV1(t, c, "x")); !errors.Is(err, ErrEnvelopeInput) {
		t.Fatalf("envelope replacement: %v", err)
	}
	// Unbound stores keep the historical sealing (enc:v1 with a key).
	legacy := legacyStore(db, c)
	if err := legacy.ReplaceSubscriptionSecret(ctxBG, "sub-1", "whsec_legacy"); err != nil || !strings.HasPrefix(rawSecret(db, "sub-1"), "enc:v1:") {
		t.Fatalf("unbound replace: %v", err)
	}
}
