package main

// INIT-024 TASK-011 review round 1 (P3-1, the review's "one maximal tree
// blocks every other write" probe): with the ceiling `spine serve` derives
// by default, one workspace's branch-scoped write of a tree at the
// per-tree limit must leave room for another workspace's write. At
// e92b45c4 the default ceiling was the per-tree byte limit, so a maximal
// tree filled it and every other repository's write waited and was
// refused. The test uses only API that existed then.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
)

func ceilGit(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x.invalid",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x.invalid", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// ceilRepo is a budget-ready repository whose branch run holds files
// entries naming one blob of size bytes.
func ceilRepo(t *testing.T, run string, files, size int) string {
	t.Helper()
	repo := t.TempDir()
	ceilGit(t, repo, nil, "init", "-q", "-b", "main")
	ceilGit(t, repo, nil, "commit", "-q", "--allow-empty", "-m", "base")
	if err := git.EnsureNeutralCheckoutAttributes(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	blob := ceilGit(t, repo, make([]byte, size), "hash-object", "-w", "--stdin")
	var mk strings.Builder
	for i := 0; i < files; i++ {
		fmt.Fprintf(&mk, "100644 blob %s\tf%03d\n", blob, i)
	}
	docs := ceilGit(t, repo, []byte(mk.String()), "mktree")
	root := ceilGit(t, repo, []byte("040000 tree "+docs+"\tdocs\n"), "mktree")
	base := ceilGit(t, repo, nil, "rev-parse", "main")
	ceilGit(t, repo, nil, "update-ref", "refs/heads/"+run, ceilGit(t, repo, nil, "commit-tree", root, "-p", base, "-m", run))
	return repo
}

func TestServeCeiling_OneMaximalTreeLeavesRoomForOtherRepositories(t *testing.T) {
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	// An operator's 8 MiB per-tree byte limit and 1,000 entries; the
	// ceiling is left to its default.
	for v, val := range map[string]string{
		"SPINE_GIT_TREE_MAX_ENTRIES":    "1000",
		"SPINE_GIT_TREE_MAX_BYTES":      "8388608",
		"SPINE_GIT_TREE_MAX_PATH_BYTES": "",
		EnvGitMutateTimeout:             "",
		EnvGitCheckoutInflightMaxBytes:  "",
	} {
		t.Setenv(v, val)
	}
	if err := installCheckoutLimits(slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	maximal := ceilRepo(t, "spine/run/a", 2, 4<<20) // exactly the 8 MiB limit
	other := ceilRepo(t, "spine/run/v", 1, 10)
	t.Setenv("TMPDIR", t.TempDir())

	// The first entry waits in the gate only on a defect: 10 s then fails
	// the test by name instead of hanging it.
	actx, acancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer acancel()
	a, err := git.EnterBranch(actx, maximal, "spine/run/a", nil)
	if err != nil {
		t.Fatalf("a tree at the per-tree limit was refused: %v", err)
	}
	defer a.Cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	begun := time.Now()
	v, err := git.EnterBranch(ctx, other, "spine/run/v", nil)
	if err != nil {
		t.Fatalf("another repository's 10-byte write beside one maximal tree: %v (ErrCheckoutsInFlight=%v)",
			err, errors.Is(err, git.ErrCheckoutsInFlight))
	}
	v.Cleanup()
	if d := time.Since(begun); d > 2*time.Second {
		t.Errorf("another repository's write waited %s beside one maximal tree", d)
	}
}
