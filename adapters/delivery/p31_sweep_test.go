package delivery

// INIT-023 TASK-009 (audit P3-1): committed port of the 2026-10-06 audit
// PoC poc/verify-r2-1. Real adapters/store.PostgresStore and the real
// BootstrapInternalSubscription over an in-memory querier that records
// the RAW signing_secret column. No database, no network.
//
// The PoC showed that a bootstrap row written before
// SPINE_SECRET_ENCRYPTION_KEY existed stays plaintext forever ("after 3
// keyed bootstraps: UPDATEs=0, raw column encrypted=false"), because the
// bootstrap compares the decrypted secret and returns early. The PoC
// itself still passes after TASK-009 — it never runs the sweep; this
// port does.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/store"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// p31DB holds event_subscriptions rows (12 columns in SELECT order).
type p31DB struct {
	rows    map[string][]any
	order   []string
	updates int
}

func (f *p31DB) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("p31DB: no tx") }
func (f *p31DB) Ping(context.Context) error            { return nil }

func (f *p31DB) Exec(_ context.Context, sql string, a ...any) (pgconn.CommandTag, error) {
	s := strings.Join(strings.Fields(sql), " ")
	switch {
	case strings.HasPrefix(s, "INSERT INTO runtime.event_subscriptions"):
		id := a[0].(string)
		var ws any
		if p, ok := a[1].(*string); ok && p != nil {
			ws = *p
		}
		f.rows[id] = []any{a[0], ws, a[2], a[3], a[4], a[5], a[6], a[7], a[8], a[9], a[10], a[11]}
		f.order = append(f.order, id)
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	case strings.Contains(s, "SET signing_secret = $2 WHERE subscription_id = $1 AND signing_secret = $3"):
		r := f.rows[a[0].(string)]
		if len(r) < 7 || r[6] != a[2] {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		}
		r[6] = a[1]
		f.updates++
		return pgconn.NewCommandTag("UPDATE 1"), nil
	case strings.HasPrefix(s, "UPDATE runtime.event_subscriptions SET name"):
		r := f.rows[a[0].(string)]
		r[2], r[3], r[4], r[5], r[6], r[7], r[8] = a[1], a[2], a[3], a[4], a[5], a[6], a[7]
		f.updates++
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	return pgconn.CommandTag{}, fmt.Errorf("p31DB: unsupported exec %q", s)
}

func (f *p31DB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	sweep := strings.Contains(sql, "SELECT subscription_id, signing_secret FROM")
	var out [][]any
	for _, id := range f.order {
		if sweep {
			out = append(out, []any{f.rows[id][0], f.rows[id][6]})
			continue
		}
		out = append(out, f.rows[id])
	}
	return &p31Rows{d: out, i: -1}, nil
}

func (f *p31DB) QueryRow(_ context.Context, _ string, a ...any) pgx.Row {
	return p31Row{v: f.rows[a[0].(string)]}
}

// raw is the signing_secret column of the only row: what a dump holds.
func (f *p31DB) raw() string { return f.rows[f.order[0]][6].(string) }

func p31Assign(dest, vals []any) error {
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

type p31Row struct{ v []any }

func (r p31Row) Scan(d ...any) error { return p31Assign(d, r.v) }

type p31Rows struct {
	d [][]any
	i int
}

func (r *p31Rows) Close()                                       {}
func (r *p31Rows) Err() error                                   { return nil }
func (r *p31Rows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *p31Rows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *p31Rows) Next() bool                                   { r.i++; return r.i < len(r.d) }
func (r *p31Rows) Scan(d ...any) error                          { return p31Assign(d, r.d[r.i]) }
func (r *p31Rows) Values() ([]any, error)                       { return r.d[r.i], nil }
func (r *p31Rows) RawValues() [][]byte                          { return nil }
func (r *p31Rows) Conn() *pgx.Conn                              { return nil }

func p31Random(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// The operator adds SPINE_SECRET_ENCRYPTION_KEY after the bootstrap row
// was written in plaintext. The sweep the serve path runs when it builds
// the store re-encrypts the row, and a dumped column no longer forges
// the dispatcher's signature.
func TestP31_KeyAddedLater_SweepEncryptsBootstrapRow(t *testing.T) {
	ctx := context.Background()
	db := &p31DB{rows: map[string][]any{}}
	token := hex.EncodeToString(p31Random(t, 32))
	cfg := BootstrapConfig{EventURL: "https://smp.invalid/internal/step-events/ws-x", WorkspaceID: "ws-x", Token: token}
	if err := BootstrapInternalSubscription(ctx, store.NewPostgresStoreWithQuerier(db), cfg); err != nil {
		t.Fatal(err)
	}
	if db.raw() != token {
		t.Fatal("fixture: expected a plaintext row written without a key")
	}

	c, err := spinecrypto.NewSecretCipher(p31Random(t, spinecrypto.EncryptionKeySize))
	if err != nil {
		t.Fatal(err)
	}
	// What the PoC showed: a keyed bootstrap alone never rewrites the row.
	legacy := store.NewPostgresStoreWithQuerier(db)
	legacy.SetSecretCipher(c)
	if err := BootstrapInternalSubscription(ctx, legacy, cfg); err != nil {
		t.Fatal(err)
	}
	if db.raw() != token || db.updates != 0 {
		t.Fatal("fixture: the keyed bootstrap was expected not to re-encrypt (audit P3-1)")
	}

	keyed := store.NewPostgresStoreWithQuerier(db)
	keyed.SetSecretCipher(c)
	if err := keyed.SetSubscriptionSecretBinding(store.SubscriptionSecretBinding{WorkspaceID: "default"}); err != nil {
		t.Fatal(err)
	}
	rep, err := keyed.ReencryptSubscriptionSecrets(ctx)
	if err != nil || rep.Rewritten != 1 || rep.FromPlaintext != 1 || !rep.Clean() {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	if !strings.HasPrefix(db.raw(), "enc:v2:"+c.PrimaryKeyID()+":") {
		t.Fatalf("row not re-encrypted (encrypted=%v)", spinecrypto.IsEncrypted(db.raw()))
	}
	body := []byte(`{"event_type":"run_failed","run_id":"forged"}`)
	if computeHMAC(body, db.raw()) == computeHMAC(body, token) {
		t.Fatal("the dumped column still forges the dispatcher signature")
	}

	// A later keyed bootstrap on the bound store reads the token back
	// and issues no UPDATE.
	updates := db.updates
	if err := BootstrapInternalSubscription(ctx, keyed, cfg); err != nil {
		t.Fatal(err)
	}
	subs, err := keyed.ListSubscriptions(ctx, "")
	if err != nil || len(subs) != 1 || subs[0].SigningSecret != token {
		t.Fatalf("read-back: %v", err)
	}
	if db.updates != updates {
		t.Fatalf("keyed bootstrap rewrote an unchanged row: updates %d->%d", updates, db.updates)
	}
	// And the dispatcher's resolver hands the real token to the signer.
	resolved, err := NewStoreSubscriptionResolver(keyed).GetSubscription(ctx, subs[0].SubscriptionID)
	if err != nil || resolved.SigningSecret != token {
		t.Fatalf("resolver: %v", err)
	}
}
