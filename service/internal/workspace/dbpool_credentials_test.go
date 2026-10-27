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
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/secrets"
	"github.com/jackc/pgx/v5/pgxpool"
)

// leakyDSNs are connection strings pgx refuses to parse, each with
// credentials only this test knows. want is a fragment of the reason that
// must survive.
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
	if leaked == 0 {
		t.Fatal("precondition: pgx no longer repeats any fixture's password; rework the fixtures")
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
