package workspace

// INIT-024 TASK-007 review round 1: a workspace connection string pgx
// cannot parse used to reach every log and error unredacted — pgconn's
// ParseConfigError quotes it and masks only a userinfo password, so a
// query-parameter password (and, with a bad sslmode, the userinfo one
// pgconn masks only on some paths) appeared in pool.Get's error, in
// MultiProjectionSync's Error log on every poll and in the shared-mode
// pre-authentication Warn.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/secrets"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// leakyDSNs are connection strings pgx refuses to parse, each with
// credentials only this test knows. want is a fragment of the reason that
// must survive.
//
// pgx v5.11.0 masks the query-parameter passwords of A2–A4 and A8, which
// v5.9.2 printed (INIT-024 TASK-013). It still prints a key/value password
// written with whitespace around the `=` (libpq syntax allows it), so A9–A11
// keep the precondition below true. The fixtures pgx now masks stay: Spine's
// withholding must hold for them too.
var leakyDSNs = []struct {
	name, dsn, want string
}{
	{"userinfo password, bad sslmode", "postgres://u:TOPSECRET-A1@127.0.0.1:1/db?sslmode=bogus", "sslmode"},
	{"query password, bad sslmode", "postgres://u@127.0.0.1:1/db?sslmode=bogus&password=TOPSECRET-A2", "sslmode"},
	{"query password, bad connect_timeout", "postgres://u@127.0.0.1:1/db?password=TOPSECRET-A3&connect_timeout=no", "connect_timeout"},
	{"query sslpassword, bad option", "postgres://u@127.0.0.1:1/db?sslpassword=TOPSECRET-A4&connect_timeout=no", "connect_timeout"},
	{"key=value password, bad option", "host=127.0.0.1 port=1 user=u password=TOPSECRET-A5 connect_timeout=no", "connect_timeout"},
	{"key=value quoted password, bad sslmode", "host=127.0.0.1 user=u password='TOPSECRET-A6' sslmode=bogus", "sslmode"},
	{"both passwords, bad pool option", "postgres://u:TOPSECRET-A7@127.0.0.1:1/db?password=TOPSECRET-A8&pool_max_conns=x", "pool_max_conns"},
	{"key=value password, spaces around =", "host=127.0.0.1 port=1 user=u password = TOPSECRET-A9 connect_timeout=no", "connect_timeout"},
	{"key=value password, space after =", "host=127.0.0.1 port=1 user=u password= TOPSECRET-A10 sslmode=bogus", "sslmode"},
	{"key=value sslpassword, tab before =", "host=127.0.0.1 port=1 user=u sslpassword\t=TOPSECRET-A11 connect_timeout=no", "connect_timeout"},
}

func TestBuildPgxpoolConfig_ParseErrorCarriesNoConnectionString(t *testing.T) {
	leaked := 0
	for _, tc := range leakyDSNs {
		t.Run(tc.name, func(t *testing.T) {
			if _, raw := pgxpool.ParseConfig(tc.dsn); raw != nil && strings.Contains(raw.Error(), "TOPSECRET") {
				leaked++
			}
			_, err := BuildPgxpoolConfig(tc.dsn, PoolPolicy{})
			if err == nil {
				t.Fatal("parse succeeded; the fixture must be refused")
			}
			if strings.Contains(err.Error(), "TOPSECRET") || strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("error carries the connection string: %v", err)
			}
			if !errors.Is(err, ErrInvalidDatabaseURL) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want ErrInvalidDatabaseURL naming %q", err, tc.want)
			}
		})
	}
	// The fixtures are meaningful only if pgx itself leaks some of them.
	// TestConnStringFreeError_WithholdsWhateverPgxMasks covers the guarantee
	// without depending on that.
	if leaked == 0 {
		t.Fatal("precondition: pgx no longer repeats any fixture's password; rework the fixtures")
	}
}

// INIT-024 TASK-013: connStringFreeError's guarantee is Spine's, not
// pgconn's. pgconn's masking of the connection string is best effort and
// changes between releases, and it never masks the reason (msg, and the
// wrapped error). These parse errors carry the connection string and its
// password in the reason, so they leak whatever pgconn masks; the returned
// error must carry neither, and keep the reason's other text.
func TestConnStringFreeError_WithholdsWhateverPgxMasks(t *testing.T) {
	cases := []struct {
		name, dsn string
		err       error
		secrets   []string
		want      string
	}{
		{
			name:    "the string in the reason",
			dsn:     "postgres://u:TOPSECRET-D1@127.0.0.1:1/db?sslmode=bogus",
			err:     pgconn.NewParseConfigError("postgres://u:TOPSECRET-D1@127.0.0.1:1/db?sslmode=bogus", "bad sslmode in postgres://u:TOPSECRET-D1@127.0.0.1:1/db?sslmode=bogus", nil),
			secrets: []string{"TOPSECRET-D1", "127.0.0.1"},
			want:    "bad sslmode in xxxxx",
		},
		{
			name:    "a query password in the wrapped error",
			dsn:     "postgres://u@127.0.0.1:1/db?password=TOPSECRET-D2&connect_timeout=no",
			err:     pgconn.NewParseConfigError("postgres://u@127.0.0.1:1/db?password=TOPSECRET-D2&connect_timeout=no", "invalid connect_timeout", errors.New("near password=TOPSECRET-D2")),
			secrets: []string{"TOPSECRET-D2"},
			want:    "invalid connect_timeout (near password=xxxxx)",
		},
		{
			name:    "a percent-encoded password, decoded in the reason",
			dsn:     "postgres://u:TOP%2BSECRET-D3@127.0.0.1:1/db?sslmode=bogus",
			err:     fmt.Errorf("open: %w", pgconn.NewParseConfigError("postgres://u:TOP%2BSECRET-D3@127.0.0.1:1/db?sslmode=bogus", "password TOP+SECRET-D3 rejected", nil)),
			secrets: []string{"TOP+SECRET-D3", "TOP%2BSECRET-D3"},
			want:    "password xxxxx rejected",
		},
		{
			name:    "a key/value password",
			dsn:     "host=127.0.0.1 user=u password='TOPSECRET-D4' sslmode=bogus",
			err:     pgconn.NewParseConfigError("host=127.0.0.1 user=u password='TOPSECRET-D4' sslmode=bogus", "sslmode is invalid for TOPSECRET-D4", nil),
			secrets: []string{"TOPSECRET-D4"},
			want:    "sslmode is invalid for xxxxx",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.err.Error(), tc.secrets[0]) {
				t.Fatalf("precondition: the parse error must carry the password, or the case is vacuous: %v", tc.err)
			}
			got := connStringFreeError(tc.err, tc.dsn)
			for _, s := range tc.secrets {
				if strings.Contains(got.Error(), s) {
					t.Fatalf("error carries %q: %v", s, got)
				}
			}
			if !errors.Is(got, ErrInvalidDatabaseURL) {
				t.Fatalf("error = %v, want ErrInvalidDatabaseURL", got)
			}
			if want := ErrInvalidDatabaseURL.Error() + ": " + tc.want; got.Error() != want {
				t.Fatalf("error = %q, want %q", got, want)
			}
			var parseErr *pgconn.ParseConfigError
			if errors.As(got, &parseErr) {
				t.Fatal("the returned error still wraps the ParseConfigError, whose Error() is the leak")
			}
		})
	}
}

// A string that parses but cannot connect: the connect error names host,
// user and database, never the password.
func TestNewWorkspaceDBPool_ConnectErrorCarriesNoPassword(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u:TOPSECRET-B1@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",
		"postgres://u@127.0.0.1:1/db?sslmode=disable&password=TOPSECRET-B2&connect_timeout=2",
	} {
		_, err := NewWorkspaceDBPool(context.Background(), "ws-x", dsn, PoolPolicy{})
		if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
			t.Fatalf("connect error carries a password (or nil): %v", err)
		}
	}
}

type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The review's case end to end: a db-mode registry row whose
// database_url carries a password and an invalid sslmode. Neither
// pool.Get's error nor MultiProjectionSync's poll log may carry it.
func TestServicePool_BadDatabaseURLLeaksNoPassword(t *testing.T) {
	buf := &lockedLog{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var cfgs []Config
	for i, tc := range leakyDSNs {
		cfgs = append(cfgs, Config{
			ID:          "ws-leak-" + string(rune('a'+i)),
			DatabaseURL: secrets.NewSecretValue([]byte(tc.dsn)),
			RepoPath:    t.TempDir(),
			Status:      StatusActive,
		})
	}
	resolver := &fakeResolver{configs: cfgs}
	ctx := context.Background()
	pool := NewServicePool(ctx, resolver, PoolConfig{IdleCheckInterval: -1})
	defer pool.Close()

	for _, c := range cfgs {
		_, err := pool.Get(ctx, c.ID)
		if err == nil {
			t.Fatalf("%s: Get succeeded", c.ID)
		}
		if strings.Contains(err.Error(), "TOPSECRET") {
			t.Fatalf("%s: pool.Get error carries the password: %v", c.ID, err)
		}
		if !errors.Is(err, ErrInvalidDatabaseURL) {
			t.Errorf("%s: pool.Get error %v does not wrap ErrInvalidDatabaseURL", c.ID, err)
		}
	}

	NewMultiProjectionSync(pool, resolver, MultiProjectionSyncConfig{}).syncAll(ctx)
	out := buf.String()
	if strings.Contains(out, "TOPSECRET") {
		t.Fatalf("MultiProjectionSync logged a workspace database password:\n%s", out)
	}
	if n := strings.Count(out, "get workspace services for projection sync failed"); n != len(cfgs) {
		t.Fatalf("want one projection-sync Error per workspace (%d), got %d:\n%s", len(cfgs), n, out)
	}
}
