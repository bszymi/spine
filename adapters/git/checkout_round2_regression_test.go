package git_test

// INIT-024 TASK-011 delta review (round 2), P3-A: with an in-flight
// ceiling, the worktree was filled at the weighed commit and then attached
// to the branch with `git switch`, which checks the branch's CURRENT tip
// out. A branch that moved after the post-admission re-read (a push) was
// therefore written to the temporary disk — a tree nobody weighed — before
// the post-condition refused it and removed the worktree. The test uses
// only API that existed at 7064711a, where it fails.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
)

// installMoveAndProbe puts a git wrapper first on PATH that moves ref of
// repo to target once, as the first `git worktree add` starts (after the
// tip was re-read: the add fills the worktree at the weighed commit), and
// that records, after every git command run inside a scope's worktree,
// the regular-file bytes the worktree then holds (".git" excluded). It
// returns the recorded sizes.
func installMoveAndProbe(t *testing.T, repo, ref, target string) func() []int64 {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	if realGit, err = filepath.Abs(realGit); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "sizes.log")
	script := `#!/bin/sh
if [ "$1" = worktree ] && [ "$2" = add ] && [ ! -e "$PROBE_MOVED" ]; then
  : > "$PROBE_MOVED"
  "$PROBE_REAL" -C "$PROBE_REPO" update-ref "$PROBE_REF" "$PROBE_TARGET"
fi
"$PROBE_REAL" "$@"
rc=$?
case "$(pwd -P)" in
*/spine-wt-parent-*/wt|*/spine-wt-parent-*/wt/*)
  n=$(find . -type f ! -path ./.git -exec cat {} + 2>/dev/null | wc -c)
  printf '%s\n' "$n" >> "$PROBE_LOG" ;;
esac
exit $rc
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: the wrapper must be executable
		t.Fatal(err)
	}
	t.Setenv("PROBE_REAL", realGit)
	t.Setenv("PROBE_MOVED", filepath.Join(dir, "moved"))
	t.Setenv("PROBE_REPO", repo)
	t.Setenv("PROBE_REF", ref)
	t.Setenv("PROBE_TARGET", target)
	t.Setenv("PROBE_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []int64 {
		data, err := os.ReadFile(log) //nolint:gosec // G304: the test's own log
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		var out []int64
		for _, f := range strings.Fields(string(data)) {
			n, err := strconv.ParseInt(f, 10, 64)
			if err != nil {
				t.Fatalf("probe log %q: %v", data, err)
			}
			out = append(out, n)
		}
		return out
	}
}

// TestEnterBranch_MoveAfterTheReReadIsNeverWritten (P3-A, the delta
// review's TestSkD_MoveAfterReReadIsWrittenUnderTheOldWeight): the branch
// moves from a 10 MiB tree to a 40 MiB one after the tip was re-read,
// while the worktree is filled at the weighed commit. The entry is
// refused with the retryable ErrBranchMoved and nothing is left — and at
// no point does the worktree hold more than the tree weighed: the moved
// tip is never written. At 7064711a `switch` wrote the 40 MiB tree before
// the refusal.
func TestEnterBranch_MoveAfterTheReReadIsNeverWritten(t *testing.T) {
	b := git.DefaultTreeBudget()
	for _, mode := range []struct {
		name   string
		limits func(ceiling int64) git.CheckoutLimits
	}{
		{"serve limits", func(c int64) git.CheckoutLimits {
			return git.CheckoutLimits{Budget: &b, MutateTimeout: git.DefaultMutateTimeout, InflightMaxBytes: c}
		}},
		{"ceiling only", func(c int64) git.CheckoutLimits { return git.CheckoutLimits{InflightMaxBytes: c} }},
	} {
		t.Run(mode.name, func(t *testing.T) {
			repo, run := claimRepo(t) // 40 x 256 KiB
			small := tbGit(t, repo, nil, "rev-parse", run)
			big := tbBomb(t, repo, small, 160, 256<<10) // 160 x 256 KiB
			tbGit(t, repo, nil, "branch", "big-ref", big)
			weight := treeWeight(t, repo, run)
			bigBytes := int64(160 * 256 << 10)
			limits := mode.limits(8 * weight)
			tmp := isolateTempDir(t)

			sizes := installMoveAndProbe(t, repo, "refs/heads/"+run, big)
			watch := watchPeak(tmp)
			var heldMax int64
			stop := make(chan struct{})
			sampled := make(chan struct{})
			go func() {
				defer close(sampled)
				for {
					if h := git.InflightHeldForTest(); h > heldMax {
						heldMax = h
					}
					select {
					case <-stop:
						return
					case <-time.After(time.Millisecond):
					}
				}
			}()
			s, err := git.EnterBranchWithLimits(boundedCtx(t, 30*time.Second), repo, run, nil, limits)
			if err == nil {
				s.Cleanup()
			}
			peak := watch.Stop()
			close(stop)
			<-sampled

			if !errors.Is(err, git.ErrBranchMoved) {
				t.Errorf("err = %v, want ErrBranchMoved (the branch moved after the tip was re-read)", err)
			}
			got := sizes()
			if len(got) == 0 {
				t.Fatal("no git command ran inside the worktree: the probe saw nothing")
			}
			for _, n := range got {
				if n > weight {
					t.Errorf("the worktree held %d bytes after a git command, more than the %d weighed (the moved tip is %d bytes): an unweighed tree was written", n, weight, bigBytes)
					break
				}
			}
			if peak > weight {
				t.Errorf("peak bytes under TMPDIR %d > the weight held %d (%.1fx; gate peak %d)", peak, weight, float64(peak)/float64(weight), heldMax)
			}
			assertEmptyDir(t, tmp)
			if h := git.InflightHeldForTest(); h != 0 {
				t.Errorf("gate holds %d, want 0", h)
			}
			if n := worktreeCount(t, repo); n != 1 {
				t.Errorf("worktree list has %d entries, want 1", n)
			}
		})
	}
}
