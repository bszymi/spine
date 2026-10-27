package workspace_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
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
