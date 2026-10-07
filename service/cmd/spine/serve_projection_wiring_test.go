package main

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/testutil"
)

// INIT-023 TASK-011 AC 18: single-workspace `serve` pins its projection
// to the governed-merge target, and a repository without
// refs/heads/main logs one startup Warn instead of failing startup.
func TestBuildServerConfig_ProjectionFollowsAuthoritativeBranch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		repo     func(t *testing.T) string
		wantWarn int
	}{
		{"main", testutil.NewTempRepo, 0},
		{"no_main", masterOnlyServeRepo, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			repoPath := tc.repo(t)
			q := queue.NewMemoryQueue(16)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go q.Start(ctx)
			rt, err := buildServerConfig(ctx, serveDeps{
				Store:      stubStore{},
				RepoPath:   repoPath,
				SpineCfg:   &config.SpineConfig{ArtifactsDir: "/"},
				GitClient:  git.NewCLIClient(repoPath),
				Queue:      q,
				Events:     event.NewQueueRouter(q),
				DevMode:    true,
				RuntimeEnv: "development",
			})
			if err != nil {
				t.Fatalf("buildServerConfig must not fail: %v", err)
			}
			if rt.ProjSync == nil {
				t.Fatal("expected a projection service")
			}
			if got := rt.ProjSync.AuthoritativeBranch(); got != engine.AuthoritativeBranch {
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

func masterOnlyServeRepo(t *testing.T) string {
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
