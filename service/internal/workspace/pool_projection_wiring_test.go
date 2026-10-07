//go:build integration

package workspace

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/testutil"
)

// INIT-023 TASK-011 AC 18: every per-workspace ServiceSet pins its
// projection to the governed-merge target, and a repository without
// refs/heads/main logs one startup Warn instead of failing the build.
func TestBuildServiceSet_ProjectionFollowsAuthoritativeBranch(t *testing.T) {
	dsn := store.TestDSN()
	if os.Getenv("SPINE_TEST_DATABASE_URL") == "" {
		t.Skip("SPINE_TEST_DATABASE_URL not set")
	}

	for _, tc := range []struct {
		name     string
		repo     func(t *testing.T) string
		wantWarn int
	}{
		{"main", testutil.NewTempRepo, 0},
		{"no_main", masterOnlyRepo, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			cfg := Config{ID: "ws-authref-" + tc.name, RepoPath: tc.repo(t), DatabaseURL: secrets.NewSecretValue([]byte(dsn))}
			ss, err := buildServiceSet(context.Background(), cfg, nil, nil, nil, PoolPolicy{}, "")
			if err != nil {
				t.Fatalf("buildServiceSet must not fail: %v", err)
			}
			defer ss.close("shutdown")
			if ss.ProjSync == nil {
				t.Fatal("expected a projection service with a database configured")
			}
			if got := ss.ProjSync.AuthoritativeBranch(); got != engine.AuthoritativeBranch {
				t.Fatalf("ProjSync.AuthoritativeBranch() = %q, want %q", got, engine.AuthoritativeBranch)
			}
			n := 0
			for _, l := range strings.Split(buf.String(), "\n") {
				if strings.Contains(l, "level=WARN") && strings.Contains(l, "authoritative branch does not resolve") && strings.Contains(l, "refs/heads/main") {
					n++
				}
			}
			if n != tc.wantWarn {
				t.Fatalf("startup Warns naming refs/heads/main = %d, want %d:\n%s", n, tc.wantWarn, buf.String())
			}
		})
	}
}

func masterOnlyRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "master"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}
