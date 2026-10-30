package gateway_test

// INIT-024 integration A: on /api/v1 in shared mode the request context
// carries the workspace id (workspaceMiddleware), and observe.Logger adds
// it to every line. TASK-007's pre-authentication lines and TASK-008's
// pool build lines (logged on the Get caller's context) added it again, so
// each named the workspace twice. Each line names it once.

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/workspace"
)

var workspaceIDAttr = regexp.MustCompile(`(?:^| )workspace_id=(\S+)`)

func TestPreauthLog_NamesTheWorkspaceOnce(t *testing.T) {
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	fs := newFakeStore()
	fs.actors["admin-1"] = &domain.Actor{ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin", Role: domain.RoleAdmin, Status: domain.ActorStatusActive}
	authSvc := auth.NewService(fs)
	valid, _, err := authSvc.CreateToken(context.Background(), "admin-1", "ok", nil)
	if err != nil {
		t.Fatal(err)
	}
	hang := make(chan struct{})
	var releaseOnce sync.Once
	ids := []string{"ws-ok", "ws-panic", "ws-fail", "ws-noauth", "ws-hang"}
	table := func() *countingResolver {
		known := map[string]workspace.Config{}
		for _, id := range ids {
			known[id] = workspace.Config{ID: id, RepoPath: t.TempDir(), Status: workspace.StatusActive}
		}
		return &countingResolver{calls: map[string]int{}, known: known, errs: map[string]error{}}
	}
	pool := workspace.NewServicePool(context.Background(), table(), workspace.PoolConfig{
		InitTimeout:       200 * time.Millisecond,
		IdleCheckInterval: -1,
		Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
			switch ss.Config.ID {
			case "ws-panic":
				panic("integration A: a panicking workspace build")
			case "ws-fail":
				return errors.New("connect to workspace database: connection refused")
			case "ws-hang":
				<-hang
			case "ws-noauth":
				ss.Store = fs
				return nil
			}
			ss.Store = fs
			ss.Auth = authSvc
			return nil
		},
	})
	t.Cleanup(pool.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(hang) }) })
	srv := gateway.NewServer(":0", gateway.ServerConfig{WorkspaceResolver: table(), ServicePool: pool})
	env := &preauthEnv{ts: httptest.NewServer(srv.Handler())}
	t.Cleanup(env.ts.Close)

	for _, c := range []struct{ ws, authz string }{
		{"ws-ok", "Bearer " + valid}, // the "initialized" line
		{"ws-ok", "Bearer junk"},
		{"ws-panic", "Bearer junk"},
		{"ws-fail", "Bearer junk"},
		{"ws-noauth", "Bearer junk"},
		{"ws-hang", "Bearer junk"},
	} {
		env.do(t, "/api/v1/system/metrics", c.ws, c.authz)
	}

	// The lines this test is about, each with the workspace it names.
	want := map[string]string{
		"workspace service set initialized":                              "", // ws-ok and ws-noauth
		"workspace service set build panicked":                           "ws-panic",
		"could not be built for a request that is not yet authenticated": "",
		"workspace has no auth service":                                  "ws-noauth",
		"workspace service set initialization timed out":                 "ws-hang",
	}
	seen := map[string]int{}
	for _, line := range strings.Split(buf.String(), "\n") {
		for msg, ws := range want {
			if !strings.Contains(line, msg) {
				continue
			}
			seen[msg]++
			m := workspaceIDAttr.FindAllStringSubmatch(line, -1)
			if len(m) != 1 {
				t.Errorf("%q: %d workspace_id attributes, want 1:\n%s", msg, len(m), line)
				continue
			}
			if ws != "" && m[0][1] != ws {
				t.Errorf("%q: workspace_id=%s, want %s:\n%s", msg, m[0][1], ws, line)
			}
		}
	}
	for msg := range want {
		if seen[msg] == 0 {
			t.Errorf("no %q line was logged:\n%s", msg, buf.String())
		}
	}
	// The pre-authentication Warn is logged for the panicking, failing and
	// hung builds.
	if n := seen["could not be built for a request that is not yet authenticated"]; n != 3 {
		t.Errorf("pre-authentication build-failure Warn lines: %d, want 3 (panic, failure, timeout)", n)
	}
}
