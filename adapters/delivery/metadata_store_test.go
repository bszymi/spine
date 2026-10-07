package delivery_test

// INIT-023 TASK-010 AC5: the v2 opt-in survives the real storage path.
// BootstrapInternalSubscription writes the row through the real
// PostgresStore (backed by an in-memory fake querier — no database),
// and the real StoreSubscriptionResolver reads it back as
// SubscriptionDetail.SignatureVersion == "v2". The fake-querier pattern
// is the one the audit's at-rest-secrets proof of concept used.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// subscriptionsDB holds runtime.event_subscriptions rows as the column
// values PostgresStore wrote, in the SELECT column order.
type subscriptionsDB struct {
	mu    sync.Mutex
	rows  map[string][]any
	order []string
}

func newSubscriptionsDB() *subscriptionsDB { return &subscriptionsDB{rows: map[string][]any{}} }

func (f *subscriptionsDB) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("no tx") }
func (f *subscriptionsDB) Ping(context.Context) error            { return nil }

func (f *subscriptionsDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := strings.TrimSpace(sql)
	switch {
	case strings.HasPrefix(s, "INSERT INTO runtime.event_subscriptions"):
		// (id, workspace_id, name, target_type, target_url, event_types,
		//  signing_secret, status, metadata, created_by, created_at, updated_at)
		id := args[0].(string)
		f.rows[id] = append([]any(nil), args...)
		f.order = append(f.order, id)
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	case strings.HasPrefix(s, "UPDATE runtime.event_subscriptions"):
		return pgconn.CommandTag{}, fmt.Errorf("fake: update not expected in this test")
	}
	return pgconn.CommandTag{}, fmt.Errorf("fake: unsupported exec %q", s)
}

func (f *subscriptionsDB) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]any
	for _, id := range f.order {
		out = append(out, f.rows[id])
	}
	return &fakeRows{data: out, i: -1}, nil
}

func (f *subscriptionsDB) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[args[0].(string)]
	if !ok {
		return fakeRow{err: pgx.ErrNoRows}
	}
	return fakeRow{vals: r}
}

// assign copies vals into the Scan destinations, converting the few
// shapes PostgresStore writes (a *string workspace id) into what it
// reads back.
func assign(dest []any, vals []any) error {
	if len(dest) != len(vals) {
		return fmt.Errorf("scan arity %d != %d", len(dest), len(vals))
	}
	for i := range dest {
		dv := reflect.ValueOf(dest[i]).Elem()
		vv := reflect.ValueOf(vals[i])
		if !vv.IsValid() {
			dv.Set(reflect.Zero(dv.Type()))
			continue
		}
		if vv.Type().AssignableTo(dv.Type()) {
			dv.Set(vv)
			continue
		}
		if vv.Kind() == reflect.Pointer && vv.Elem().Type().AssignableTo(dv.Type()) {
			dv.Set(vv.Elem())
			continue
		}
		if dv.Kind() == reflect.Pointer && vv.Type().AssignableTo(dv.Type().Elem()) {
			p := reflect.New(dv.Type().Elem())
			p.Elem().Set(vv)
			dv.Set(p)
			continue
		}
		return fmt.Errorf("scan column %d: cannot assign %s to %s", i, vv.Type(), dv.Type())
	}
	return nil
}

type fakeRow struct {
	vals []any
	err  error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return assign(dest, r.vals)
}

type fakeRows struct {
	data [][]any
	i    int
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Next() bool                                   { r.i++; return r.i < len(r.data) }
func (r *fakeRows) Scan(dest ...any) error                       { return assign(dest, r.data[r.i]) }
func (r *fakeRows) Values() ([]any, error)                       { return r.data[r.i], nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }

func TestStoreBackedResolverReportsV2(t *testing.T) {
	ctx := context.Background()
	db := newSubscriptionsDB()
	pgs := store.NewPostgresStoreWithQuerier(db)

	if err := delivery.BootstrapInternalSubscription(ctx, pgs, delivery.BootstrapConfig{
		EventURL: "https://smp.invalid/internal/step-events/ws-x", WorkspaceID: "ws-x",
		Token: "per-workspace-credential", SignatureVersion: delivery.SignatureVersionV2,
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if len(db.order) != 1 {
		t.Fatalf("bootstrap wrote %d rows, want 1", len(db.order))
	}
	id := db.order[0]
	if md := string(db.rows[id][8].([]byte)); md != `{"source":"bootstrap","signature_version":"v2"}` {
		t.Fatalf("persisted metadata = %s", md)
	}

	detail, err := delivery.NewStoreSubscriptionResolver(pgs).GetSubscription(ctx, id)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if detail.SignatureVersion != delivery.SignatureVersionV2 {
		t.Fatalf("SubscriptionDetail.SignatureVersion = %q, want v2", detail.SignatureVersion)
	}
	if detail.SigningSecret != "per-workspace-credential" {
		t.Fatalf("signing secret did not round-trip")
	}

	// Second run with the same config: no write (the fake refuses UPDATE).
	if err := delivery.BootstrapInternalSubscription(ctx, pgs, delivery.BootstrapConfig{
		EventURL: "https://smp.invalid/internal/step-events/ws-x", WorkspaceID: "ws-x",
		Token: "per-workspace-credential", SignatureVersion: delivery.SignatureVersionV2,
	}); err != nil {
		t.Fatalf("second bootstrap (must be a no-op): %v", err)
	}

	// A deleted (absent) row resolves to a domain not-found error, which
	// the dispatcher dead-letters rather than retrying.
	if _, err := delivery.NewStoreSubscriptionResolver(pgs).GetSubscription(ctx, "missing"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing row: err = %v", err)
	}
}
