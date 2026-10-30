package workspace_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

func cbGit(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x.invalid",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x.invalid", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestWarnIfOverCheckoutBudget (INIT-024 TASK-003 review round 1,
// security P3-3): a workspace tree already over the budget at start — an
// upgrade, a lowered limit — is named in one Warn with the limit, its
// variable and the remedy; a tree within the budget, or a client with no
// budget, logs nothing.
func TestWarnIfOverCheckoutBudget(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	if err := git.EnsureNeutralCheckoutAttributes(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	var mk strings.Builder
	blob := cbGit(t, repo, []byte("x"), "hash-object", "-w", "--stdin")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&mk, "100644 blob %s\tf%02d.md\n", blob, i)
	}
	tree := cbGit(t, repo, []byte(mk.String()), "mktree")
	commit := cbGit(t, repo, nil, "commit-tree", tree, "-p", "main", "-m", "thirty")
	cbGit(t, repo, nil, "update-ref", "refs/heads/main", commit)

	for _, tc := range []struct {
		name string
		opts []git.CLIOption
		want string
	}{
		{"over", []git.CLIOption{git.WithHomeBranch("main"), git.WithTreeBudget(git.TreeBudget{MaxEntries: 10})}, "SPINE_GIT_TREE_MAX_ENTRIES"},
		{"within", []git.CLIOption{git.WithHomeBranch("main"), git.WithTreeBudget(git.TreeBudget{})}, ""},
		{"no budget", []git.CLIOption{git.WithHomeBranch("main")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			workspace.WarnIfOverCheckoutBudget(context.Background(), git.NewCLIClient(repo, tc.opts...), "ws-1", log)
			got := buf.String()
			if tc.want == "" {
				if got != "" {
					t.Fatalf("unexpected log: %s", got)
				}
				return
			}
			for _, w := range []string{"over the git checkout budget", tc.want, "workspace_id=ws-1", "a merge whose result fits"} {
				if !strings.Contains(got, w) {
					t.Errorf("log lacks %q:\n%s", w, got)
				}
			}
		})
	}
}

// TestWarnIfOverCheckoutBudget_BeforeTheLine (confirming review of
// integration A phase 4, P4): on the upgrade the warning is for — a tree
// already over the budget when the neutral line would first be added —
// the measure ran through the line, so it only logged at Debug that it had
// not measured, and the refusals that followed named the limit but not the
// variable or the restart. It measures what HEAD stores now: one Warn
// names the variable, the remedy and the refused pushes.
func TestWarnIfOverCheckoutBudget_BeforeTheLine(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	var mk strings.Builder
	blob := cbGit(t, repo, []byte("x"), "hash-object", "-w", "--stdin")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&mk, "100644 blob %s\tf%02d.md\n", blob, i)
	}
	tree := cbGit(t, repo, []byte(mk.String()), "mktree")
	commit := cbGit(t, repo, nil, "commit-tree", tree, "-p", "main", "-m", "thirty")
	cbGit(t, repo, nil, "update-ref", "refs/heads/main", commit)
	if has, _ := git.HasNeutralCheckoutAttributes(context.Background(), repo); has {
		t.Fatal("fixture: the line is already there")
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	workspace.WarnIfOverCheckoutBudget(context.Background(),
		git.NewCLIClient(repo, git.WithHomeBranch("main"), git.WithTreeBudget(git.TreeBudget{MaxEntries: 10})), "ws-1", log)
	got := buf.String()
	for _, w := range []string{"over the git checkout budget", "env=SPINE_GIT_TREE_MAX_ENTRIES",
		"raise SPINE_GIT_TREE_MAX_ENTRIES above", "restart spine serve", "pushes are refused too", "workspace_id=ws-1"} {
		if !strings.Contains(got, w) {
			t.Errorf("log lacks %q:\n%s", w, got)
		}
	}
}

// TestBuildServiceSet_OverBudgetHEADLogNamesTheVariable (same P4): the
// build's Error for a HEAD over the budget before the line names the
// variable and the restart.
func TestBuildServiceSet_OverBudgetHEADLogNamesTheVariable(t *testing.T) {
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{MaxEntries: 1}})
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, "a.txt", "a\n")
	testutil.WriteFile(t, repo, "b.txt", "b\n")
	cbGit(t, repo, nil, "add", "-A")
	cbGit(t, repo, nil, "commit", "-q", "-m", "two")
	var buf syncBuf
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	pool := workspace.NewServicePool(context.Background(), staticResolver{cfg: workspace.Config{ID: "ws-1", RepoPath: repo, Status: workspace.StatusActive}},
		workspace.PoolConfig{IdleCheckInterval: -1})
	t.Cleanup(pool.Close)
	if _, err := pool.Get(context.Background(), "ws-1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	pool.Release("ws-1")
	got := buf.String()
	for _, w := range []string{"neutral checkout attributes not set", "env=SPINE_GIT_TREE_MAX_ENTRIES", "restart spine serve"} {
		if !strings.Contains(got, w) {
			t.Errorf("log lacks %q:\n%s", w, got)
		}
	}
}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// staticResolver resolves one workspace.
type staticResolver struct{ cfg workspace.Config }

func (r staticResolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	if id != r.cfg.ID {
		return nil, workspace.ErrWorkspaceNotFound
	}
	c := r.cfg
	return &c, nil
}

func (r staticResolver) List(context.Context) ([]workspace.Config, error) {
	return []workspace.Config{r.cfg}, nil
}
