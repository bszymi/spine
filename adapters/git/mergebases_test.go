package git_test

// INIT-023 TASK-006: CLIClient.MergeBases (`git merge-base --all`), the
// optional capability the governance-path guard diffs from (ADR-016 §3).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// mbGit runs git in dir with pinned dates so the history is reproducible.
func mbGit(t *testing.T, dir, date string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestMergeBases_CrissCross builds the design-check history: a run branch
// merged another run's branch (r2) and r2 then landed on main, so main and
// the run branch have two merge bases. MergeBase reports one of them;
// MergeBases reports both.
func TestMergeBases_CrissCross(t *testing.T) {
	testutil.HermeticGit(t)
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	gc := git.NewCLIClient(repo)
	commit := func(date, msg string) string {
		mbGit(t, repo, date, "add", "-A")
		mbGit(t, repo, date, "commit", "-qm", msg)
		return mbGit(t, repo, date, "rev-parse", "HEAD")
	}
	testutil.WriteFile(t, repo, "a.txt", "seed\n")
	commit("2026-02-01T00:00:00Z", "seed")
	testutil.GitCheckout(t, repo, "-b", "r2")
	testutil.WriteFile(t, repo, "r2.txt", "r2\n")
	r2 := commit("2026-02-02T00:00:00Z", "r2")
	testutil.GitCheckout(t, repo, "main")
	testutil.WriteFile(t, repo, "b.txt", "b\n")
	x1 := commit("2026-02-03T00:00:00Z", "main moves")
	testutil.GitCheckout(t, repo, "-b", "attack")
	testutil.WriteFile(t, repo, "feat.txt", "f\n")
	commit("2026-02-04T00:00:00Z", "work")
	mbGit(t, repo, "2026-02-05T00:00:00Z", "merge", "--no-ff", "-qm", "merge r2", "r2")
	testutil.GitCheckout(t, repo, "main")
	mbGit(t, repo, "2026-02-06T00:00:00Z", "merge", "--no-ff", "-qm", "merge r2 to main", "r2")

	got, err := gc.MergeBases(ctx, "main", "attack")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{r2, x1}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeBases = %v, want both bases %v", got, want)
	}
	one, err := gc.MergeBase(ctx, "main", "attack")
	if err != nil || (one != r2 && one != x1) {
		t.Fatalf("MergeBase = %q, %v; want one of the two bases", one, err)
	}

	// A linear history has one base; a full object id is accepted as is.
	attack := mbGit(t, repo, "2026-02-06T00:00:00Z", "rev-parse", "attack")
	lin, err := gc.MergeBases(ctx, x1, attack)
	if err != nil || !reflect.DeepEqual(lin, []string{x1}) {
		t.Fatalf("MergeBases(x1, attack) = %v, %v; want [x1]", lin, err)
	}
}

// TestMergeBases_RefusesAndFailsClosed: malformed revisions are refused
// before any git process; a missing revision is an error, never an empty
// answer; unrelated histories are an empty answer with no error (review
// round 1: the guard refuses them permanently instead of retrying for
// good); a tag named like the branch does not answer for it.
func TestMergeBases_RefusesAndFailsClosed(t *testing.T) {
	testutil.HermeticGit(t)
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	gc := git.NewCLIClient(repo)
	testutil.WriteFile(t, repo, "a.txt", "a\n")
	testutil.GitAdd(t, repo, ".", "one")
	main := mbGit(t, repo, "2026-02-01T00:00:00Z", "rev-parse", "main")

	for _, bad := range []string{"", "--upload-pack=x", "-x", "a..b", "main\x00"} {
		if _, err := gc.MergeBases(ctx, bad, "main"); err == nil {
			t.Errorf("MergeBases(%q, main) accepted a malformed revision", bad)
		} else if !errors.Is(err, git.ErrInvalidRef) {
			t.Errorf("MergeBases(%q, main) = %v, want ErrInvalidRef", bad, err)
		}
	}
	if _, err := gc.MergeBases(ctx, "main", "no-such-branch"); err == nil {
		t.Error("MergeBases with a missing branch returned no error")
	}

	// Unrelated history: an orphan branch.
	mbGit(t, repo, "2026-02-02T00:00:00Z", "checkout", "-q", "--orphan", "orphan")
	testutil.WriteFile(t, repo, "o.txt", "o\n")
	mbGit(t, repo, "2026-02-02T00:00:00Z", "add", "-A")
	mbGit(t, repo, "2026-02-02T00:00:00Z", "commit", "-qm", "orphan")
	if bases, err := gc.MergeBases(ctx, "main", "orphan"); err != nil || bases == nil || len(bases) != 0 {
		t.Errorf("MergeBases of unrelated histories = %#v, %v; want an empty list and no error", bases, err)
	}

	// refs/tags/main pointing at the orphan commit must not answer for main.
	orphan := mbGit(t, repo, "2026-02-02T00:00:00Z", "rev-parse", "orphan")
	mbGit(t, repo, "2026-02-02T00:00:00Z", "tag", "main", orphan)
	mbGit(t, repo, "2026-02-02T00:00:00Z", "checkout", "-q", "main")
	bases, err := gc.MergeBases(ctx, "main", main)
	if err != nil || !reflect.DeepEqual(bases, []string{main}) {
		t.Fatalf("MergeBases(main, main-sha) with a shadow tag = %v, %v; want [%s]", bases, err, main)
	}
}
