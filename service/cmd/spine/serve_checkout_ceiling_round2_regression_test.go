package main

// INIT-024 TASK-011 delta review (round 2), P3-B: `spine serve`'s default
// in-flight ceiling was max(2 x the per-tree byte limit, one maximal
// tree's weight). When the entry allowance makes a maximal tree's weight
// the larger term (4096 x SPINE_GIT_TREE_MAX_ENTRIES >= the byte limit),
// the ceiling EQUALLED one maximal tree: one workspace's tree within the
// budget filled it (a repository holding nothing may take more than half),
// and every other workspace's write waited until it was refused. The test
// uses only API that existed at 7064711a, where it fails.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
)

// entryHeavyRepo is a budget-ready repository whose branch run holds one
// docs/ directory of files whose sizes are sizes.
func entryHeavyRepo(t *testing.T, run string, sizes []int) string {
	t.Helper()
	repo := t.TempDir()
	ceilGit(t, repo, nil, "init", "-q", "-b", "main")
	ceilGit(t, repo, nil, "commit", "-q", "--allow-empty", "-m", "base")
	if err := git.EnsureNeutralCheckoutAttributes(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	blobs := map[int]string{}
	var mk strings.Builder
	for i, sz := range sizes {
		b, ok := blobs[sz]
		if !ok {
			b = ceilGit(t, repo, make([]byte, sz), "hash-object", "-w", "--stdin")
			blobs[sz] = b
		}
		fmt.Fprintf(&mk, "100644 blob %s\tf%04d\n", b, i)
	}
	docs := ceilGit(t, repo, []byte(mk.String()), "mktree")
	root := ceilGit(t, repo, []byte("040000 tree "+docs+"\tdocs\n"), "mktree")
	base := ceilGit(t, repo, nil, "rev-parse", "main")
	ceilGit(t, repo, nil, "update-ref", "refs/heads/"+run, ceilGit(t, repo, nil, "commit-tree", root, "-p", base, "-m", run))
	return repo
}

// TestServeCeiling_AnEntryHeavyMaximalTreeLeavesRoomForOtherRepositories
// (P3-B, the delta review's TestSkD_ServeDefaultCeilingWithAnEntryHeavyBudget):
// with a 1 MiB per-tree byte limit and 1,000 entries — so a maximal tree
// weighs 1 MiB + 1,000 x 4 KiB, more than twice the byte limit — and the
// ceiling left to its default, one workspace's write of a maximal tree
// (999 files totalling 1 MiB plus their directory: 1,000 entries) leaves
// room for another workspace's 10-byte write.
func TestServeCeiling_AnEntryHeavyMaximalTreeLeavesRoomForOtherRepositories(t *testing.T) {
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	for v, val := range map[string]string{
		"SPINE_GIT_TREE_MAX_ENTRIES":    "1000",
		"SPINE_GIT_TREE_MAX_BYTES":      "1048576",
		"SPINE_GIT_TREE_MAX_PATH_BYTES": "",
		EnvGitMutateTimeout:             "",
		EnvGitCheckoutInflightMaxBytes:  "",
	} {
		t.Setenv(v, val)
	}
	if err := installCheckoutLimits(slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	sizes := make([]int, 999)
	for i := range sizes {
		sizes[i] = 1024
	}
	sizes[998] = 1048576 - 998*1024
	maximal := entryHeavyRepo(t, "spine/run/a", sizes)
	other := entryHeavyRepo(t, "spine/run/v", []int{10})
	t.Setenv("TMPDIR", t.TempDir())

	// The first entry waits in the gate only on a defect: 10 s then fails
	// the test by name instead of hanging it.
	actx, acancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer acancel()
	a, err := git.EnterBranch(actx, maximal, "spine/run/a", nil)
	if err != nil {
		t.Fatalf("a tree within the budget was refused: %v", err)
	}
	defer a.Cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	begun := time.Now()
	v, err := git.EnterBranch(ctx, other, "spine/run/v", nil)
	if err != nil {
		t.Fatalf("another repository's 10-byte write beside one entry-heavy maximal tree: %v (ErrCheckoutsInFlight=%v; ceiling %d)",
			err, errors.Is(err, git.ErrCheckoutsInFlight), git.DefaultCheckoutLimits().InflightMaxBytes)
	}
	v.Cleanup()
	if d := time.Since(begun); d > 2*time.Second {
		t.Errorf("another repository's write waited %s beside one maximal tree", d)
	}
}
