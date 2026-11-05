package workspace

// INIT-023 TASK-009 AC14 (unit) and AC15 (logs): PrepareSubscriptionSecrets
// over an in-memory runtime.event_subscriptions table.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// subsDB is a minimal in-memory event_subscriptions table (12 columns in
// SELECT order) for the store's subscription SQL.
type subsDB struct {
	mu        sync.Mutex
	rows      map[string][]any
	execs     int
	queryErr  error
	blockExec bool // CAS Exec blocks until its context is done
}

func newSubsDB() *subsDB { return &subsDB{rows: map[string][]any{}} }

func (d *subsDB) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("subsDB: no tx") }
func (d *subsDB) Ping(context.Context) error            { return nil }

func (d *subsDB) Exec(ctx context.Context, sql string, a ...any) (pgconn.CommandTag, error) {
	s := strings.Join(strings.Fields(sql), " ")
	if strings.Contains(s, "AND signing_secret = $3") && d.blockExec {
		<-ctx.Done()
		return pgconn.CommandTag{}, ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.execs++
	switch {
	case strings.HasPrefix(s, "INSERT INTO runtime.event_subscriptions"):
		var ws any
		if p, ok := a[1].(*string); ok && p != nil {
			ws = *p
		}
		d.rows[a[0].(string)] = []any{a[0], ws, a[2], a[3], a[4], a[5], a[6], a[7], a[8], a[9], a[10], a[11]}
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	case strings.Contains(s, "AND signing_secret = $3"):
		r := d.rows[a[0].(string)]
		if len(r) < 7 || r[6] != a[2] {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		}
		r[6] = a[1]
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	return pgconn.CommandTag{}, fmt.Errorf("subsDB: unsupported exec %q", s)
}

func (d *subsDB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	if d.queryErr != nil {
		return nil, d.queryErr
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make([]string, 0, len(d.rows))
	for id := range d.rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sweep := strings.Contains(sql, "SELECT subscription_id, signing_secret FROM")
	var out [][]any
	for _, id := range ids {
		r := d.rows[id]
		if sweep {
			out = append(out, []any{r[0], r[6]})
		} else {
			out = append(out, append([]any(nil), r...))
		}
	}
	return &subsRows{d: out, i: -1}, nil
}

func (d *subsDB) QueryRow(_ context.Context, _ string, a ...any) pgx.Row {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.rows[a[0].(string)]
	if !ok {
		return subsRow{err: pgx.ErrNoRows}
	}
	return subsRow{v: append([]any(nil), r...)}
}

func (d *subsDB) raw(id string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rows[id][6].(string)
}

func (d *subsDB) setRaw(id, v string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rows[id][6] = v
}

func subsAssign(dest, vals []any) error {
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

type subsRow struct {
	v   []any
	err error
}

func (r subsRow) Scan(d ...any) error {
	if r.err != nil {
		return r.err
	}
	return subsAssign(d, r.v)
}

type subsRows struct {
	d [][]any
	i int
}

func (r *subsRows) Close()                                       {}
func (r *subsRows) Err() error                                   { return nil }
func (r *subsRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *subsRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *subsRows) Next() bool                                   { r.i++; return r.i < len(r.d) }
func (r *subsRows) Scan(d ...any) error                          { return subsAssign(d, r.d[r.i]) }
func (r *subsRows) Values() ([]any, error)                       { return r.d[r.i], nil }
func (r *subsRows) RawValues() [][]byte                          { return nil }
func (r *subsRows) Conn() *pgx.Conn                              { return nil }

// TypeMap is part of pgx.Rows since pgx v5.11.0. These rows are decoded by
// Scan above, not through a pgtype.Map, so there is none to return.
func (r *subsRows) TypeMap() *pgtype.Map { return nil }

func subsCipher(t *testing.T) *spinecrypto.SecretCipher {
	t.Helper()
	k := make([]byte, spinecrypto.EncryptionKeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	c, err := spinecrypto.NewSecretCipher(k)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func seedSub(t *testing.T, db *subsDB, id, secret string) {
	t.Helper()
	now := time.Now().UTC()
	err := store.NewPostgresStoreWithQuerier(db).CreateSubscription(context.Background(), &store.EventSubscription{
		SubscriptionID: id, Name: "n-" + id, TargetType: "webhook", TargetURL: "https://hook.invalid",
		EventTypes: []string{}, SigningSecret: secret, Status: "active", Metadata: []byte("{}"),
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// jsonLog captures slog records as decoded JSON objects.
type jsonLog struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (l *jsonLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *jsonLog) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (l *jsonLog) records(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func (l *jsonLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func TestPrepareSubscriptionSecrets_ConvertsAndBinds(t *testing.T) {
	db := newSubsDB()
	seedSub(t, db, "sub-1", "whsec_plain")
	c := subsCipher(t)
	st := store.NewPostgresStoreWithQuerier(db)
	st.SetSecretCipher(c)
	var lg jsonLog
	rep, err := PrepareSubscriptionSecrets(context.Background(), st, c, "ws-unit", store.SecretEnvelopeMigrate, lg.logger())
	if err != nil || rep.Rewritten != 1 || !rep.Clean() {
		t.Fatalf("prepare: %+v %v", rep, err)
	}
	raw := db.raw("sub-1")
	if !strings.HasPrefix(raw, "enc:v2:"+c.PrimaryKeyID()+":") {
		t.Fatal("row not converted to enc:v2")
	}
	aad := spinecrypto.AssociatedData("runtime.event_subscriptions.signing_secret", "ws-unit", "sub-1")
	if op, err := c.Open(raw, aad); err != nil || op.Plaintext != "whsec_plain" {
		t.Fatalf("not bound to the given workspace id: %v", err)
	}
	if _, err := c.Open(raw, spinecrypto.AssociatedData("runtime.event_subscriptions.signing_secret", "default", "sub-1")); !errors.Is(err, spinecrypto.ErrEnvelopeAuth) {
		t.Fatalf("opened under another workspace id: %v", err)
	}
	if b, ok := st.SubscriptionSecretBinding(); !ok || b.WorkspaceID != "ws-unit" || b.Mode != store.SecretEnvelopeMigrate {
		t.Fatalf("binding: %+v %v", b, ok)
	}
	if got, err := st.GetSubscription(context.Background(), "sub-1"); err != nil || got.SigningSecret != "whsec_plain" {
		t.Fatalf("read-back: %v", err)
	}
	// A clean migrate sweep logs the strict hint (Info outside production).
	var hint, summary map[string]any
	for _, r := range lg.records(t) {
		switch r["msg"] {
		case strictModeHint:
			hint = r
		case "subscription secrets swept":
			summary = r
		}
	}
	if hint == nil || hint["level"] != "INFO" {
		t.Fatalf("strict hint: %v", hint)
	}
	if summary == nil || summary["rewritten"] != float64(1) || summary["primary_key_id"] != c.PrimaryKeyID() || summary["mode"] != "migrate" {
		t.Fatalf("summary record: %v", summary)
	}
}

func TestPrepareSubscriptionSecrets_ProductionHintIsWarn(t *testing.T) {
	t.Setenv("SPINE_ENV", "production")
	db := newSubsDB()
	c := subsCipher(t)
	st := store.NewPostgresStoreWithQuerier(db)
	st.SetSecretCipher(c)
	var lg jsonLog
	if _, err := PrepareSubscriptionSecrets(context.Background(), st, c, "ws", "", lg.logger()); err != nil {
		t.Fatal(err)
	}
	for _, r := range lg.records(t) {
		if r["msg"] == strictModeHint {
			if r["level"] != "WARN" {
				t.Fatalf("production hint level = %v", r["level"])
			}
			return
		}
	}
	t.Fatal("no strict hint logged")
}

func TestPrepareSubscriptionSecrets_NilCipherIsNoOp(t *testing.T) {
	db := newSubsDB()
	seedSub(t, db, "sub-1", "whsec_plain")
	st := store.NewPostgresStoreWithQuerier(db)
	before := db.execs
	rep, err := PrepareSubscriptionSecrets(context.Background(), st, nil, "ws", store.SecretEnvelopeStrict, slog.New(slog.DiscardHandler))
	if err != nil || rep.Scanned != 0 {
		t.Fatalf("nil cipher: %+v %v", rep, err)
	}
	if _, ok := st.SubscriptionSecretBinding(); ok {
		t.Fatal("nil cipher must not bind the store")
	}
	if db.execs != before || db.raw("sub-1") != "whsec_plain" {
		t.Fatal("nil cipher wrote")
	}
	if got, err := st.GetSubscription(context.Background(), "sub-1"); err != nil || got.SigningSecret != "whsec_plain" {
		t.Fatalf("plaintext dev posture changed: %v", err)
	}
	if _, err := PrepareSubscriptionSecrets(context.Background(), nil, subsCipher(t), "ws", "", nil); err != nil {
		t.Fatalf("nil store: %v", err)
	}
}

func TestPrepareSubscriptionSecrets_BindingErrorIsTyped(t *testing.T) {
	st := store.NewPostgresStoreWithQuerier(newSubsDB())
	c := subsCipher(t)
	for _, tc := range []struct {
		ws   string
		mode store.SecretEnvelopeMode
	}{{"", ""}, {"ws", "bogus"}} {
		_, err := PrepareSubscriptionSecrets(context.Background(), st, c, tc.ws, tc.mode, slog.New(slog.DiscardHandler))
		if !errors.Is(err, ErrSubscriptionSecretBinding) {
			t.Fatalf("binding error for %+v not typed: %v", tc, err)
		}
	}
}

func TestPrepareSubscriptionSecrets_QueryErrorReturnedAndLogged(t *testing.T) {
	db := newSubsDB()
	db.queryErr = errors.New("connection reset by peer")
	c := subsCipher(t)
	st := store.NewPostgresStoreWithQuerier(db)
	st.SetSecretCipher(c)
	var lg jsonLog
	_, err := PrepareSubscriptionSecrets(context.Background(), st, c, "ws", "", lg.logger())
	if err == nil || errors.Is(err, ErrSubscriptionSecretBinding) || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("query error: %v", err)
	}
	if !strings.Contains(lg.text(), "subscription secret sweep failed") {
		t.Fatal("sweep error not logged")
	}
	if _, ok := st.SubscriptionSecretBinding(); !ok {
		t.Fatal("store must stay bound after a sweep error")
	}
}

func TestSubscriptionSweepTimeout_Default(t *testing.T) {
	if subscriptionSweepTimeout != 10*time.Second {
		t.Fatalf("subscriptionSweepTimeout = %v, want 10s (TASK-009 §4)", subscriptionSweepTimeout)
	}
}

// A compare-and-swap UPDATE stuck on a row lock (modelled by an Exec
// that blocks until its context is done) must not stall the build past
// the sweep timeout. Mutation check: without the timeout this test hangs
// until the go test timeout.
func TestPrepareSubscriptionSecrets_TimesOutOnBlockedWrite(t *testing.T) {
	orig := subscriptionSweepTimeout
	subscriptionSweepTimeout = 300 * time.Millisecond
	t.Cleanup(func() { subscriptionSweepTimeout = orig })

	db := newSubsDB()
	seedSub(t, db, "sub-1", "whsec_plain")
	db.blockExec = true
	c := subsCipher(t)
	st := store.NewPostgresStoreWithQuerier(db)
	st.SetSecretCipher(c)
	start := time.Now()
	_, err := PrepareSubscriptionSecrets(context.Background(), st, c, "ws", "", slog.New(slog.DiscardHandler))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked sweep: %v", err)
	}
	if elapsed := time.Since(start); elapsed > subscriptionSweepTimeout+time.Second {
		t.Fatalf("sweep returned after %v", elapsed)
	}
	if _, ok := st.SubscriptionSecretBinding(); !ok {
		t.Fatal("store must stay bound after a timeout")
	}
	if got, err := st.GetSubscription(context.Background(), "sub-1"); err != nil || got.SigningSecret != "whsec_plain" {
		t.Fatalf("store must stay readable after a timeout: %v", err)
	}
}

// AC14 (per-row record) and AC15 (no secret material in logs).
func TestPrepareSubscriptionSecrets_LogsEachConversionWithoutSecrets(t *testing.T) {
	db := newSubsDB()
	c := subsCipher(t)
	other := subsCipher(t)
	seedSub(t, db, "sub-a", "LEAK-plaintext-a")
	seedSub(t, db, "sub-b", "seed")
	seedSub(t, db, "sub-c", "seed")
	seedSub(t, db, "sub-d", "seed")
	v1, _ := c.Encrypt("LEAK-v1-b")
	foreign, _ := c.Seal("LEAK-foreign-c", spinecrypto.AssociatedData("runtime.event_subscriptions.signing_secret", "ws-A", "sub-c"))
	wrongKey, _ := other.Encrypt("LEAK-wrongkey-d")
	db.setRaw("sub-b", v1)
	db.setRaw("sub-c", foreign)
	db.setRaw("sub-d", wrongKey)
	stored := []string{"LEAK-plaintext-a", v1, foreign, wrongKey}

	st := store.NewPostgresStoreWithQuerier(db)
	st.SetSecretCipher(c)
	var lg jsonLog
	rep, err := PrepareSubscriptionSecrets(context.Background(), st, c, "ws-B", "", lg.logger())
	if err != nil || rep.Rewritten != 2 || len(rep.Refused) != 2 {
		t.Fatalf("prepare: %+v %v", rep, err)
	}

	converted := map[string]map[string]any{}
	refused := 0
	for _, r := range lg.records(t) {
		switch r["msg"] {
		case "subscription secret converted":
			if r["level"] != "INFO" {
				t.Fatalf("conversion record level %v", r["level"])
			}
			converted[r["subscription_id"].(string)] = r
			for k := range r {
				switch k {
				case "time", "level", "msg", "workspace_id", "subscription_id", "from", "from_key_id", "repaired":
				default:
					t.Fatalf("conversion record carries unexpected field %q", k)
				}
			}
		default:
			if strings.HasPrefix(r["msg"].(string), "subscription secret refused") {
				refused++
			}
		}
	}
	if len(converted) != 2 || converted["sub-a"]["from"] != "plaintext" || converted["sub-a"]["from_key_id"] != "" ||
		converted["sub-b"]["from"] != "enc:v1" || converted["sub-b"]["from_key_id"] != c.PrimaryKeyID() {
		t.Fatalf("conversion records: %v", converted)
	}
	if refused != 2 {
		t.Fatalf("refused records = %d", refused)
	}
	haystack := lg.text() + fmt.Sprintf("%+v", rep)
	if strings.Contains(haystack, "LEAK-") {
		t.Fatal("log or report contains a fixture plaintext")
	}
	for _, v := range stored {
		needle := v
		if i := strings.LastIndex(v, ":"); i >= 0 {
			needle = v[i+1:]
		}
		if strings.Contains(haystack, needle) {
			t.Fatal("log or report contains a stored value")
		}
	}
}

// Review round 1: PoolConfig.SecretEnvelopeMode reaches the ServicePool
// field that initializeEntry passes to buildServiceSet ("" would compile
// and mean migrate). The integration test
// TestServicePool_BindsStoresInConfiguredMode covers the rest of the path
// through Get.
func TestNewServicePool_CarriesSecretEnvelopeMode(t *testing.T) {
	for _, mode := range []store.SecretEnvelopeMode{store.SecretEnvelopeStrict, store.SecretEnvelopeDowngrade} {
		p := NewServicePool(context.Background(), &multiConfigResolver{}, PoolConfig{SecretEnvelopeMode: mode, IdleTimeout: time.Minute})
		got := p.secretEnvelopeMode
		p.Close()
		if got != mode {
			t.Fatalf("PoolConfig.SecretEnvelopeMode %q not carried into the pool: %q", mode, got)
		}
	}
}
