package git_test

// INIT-024 TASK-004 AC 4/5 (review finding N-10): a Rev-class read of a
// bare name in the reserved spine namespace, or of the tree's home
// branch, never falls back to git's DWIM when refs/heads/<n> is missing.
// Before, a deleted run branch's name passed through to git, which
// resolved a same-named tag or refs/<n> instead: ReadFile, RefSHA, Diff,
// ListFiles, TreeEntryType, MergeBase and Log{Since} then read content no
// review saw. Ported from the review's residual check, extended to every
// Rev-class method and to the home branch. Every other bare name keeps the
// passthrough (AC 5), so public ?ref= reads of tags, abbreviated ids and
// remote-tracking names work as before.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// deletedBranchFixture builds main and the branch `name` (f.md "reviewed"),
// deletes the branch, and points each of shadows at a commit whose f.md
// is "shadow". It returns the repo, main's id and the shadow commit's id.
func deletedBranchFixture(t *testing.T, name string, shadows ...string) (repo, mainOID, shadow string) {
	t.Helper()
	testutil.HermeticGit(t)
	repo = testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, "f.md", "base\n")
	runGit(t, repo, "add", "f.md")
	runGit(t, repo, "commit", "-qm", "base")
	mainOID = runGit(t, repo, "rev-parse", "refs/heads/main")
	runGit(t, repo, "branch", name)
	runGit(t, repo, "switch", "-q", name)
	testutil.WriteFile(t, repo, "f.md", "reviewed\n")
	runGit(t, repo, "commit", "-qam", "reviewed")
	testutil.WriteFile(t, repo, "f.md", "shadow\n")
	runGit(t, repo, "commit", "-qam", "shadow")
	shadow = runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "switch", "-q", "main")
	runGit(t, repo, "branch", "-D", name)
	for _, ref := range shadows {
		runGit(t, repo, "update-ref", ref, shadow)
	}
	return repo, mainOID, shadow
}

func wantNotFound(t *testing.T, label string, err error) {
	t.Helper()
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound {
		t.Errorf("%s: %v, want ErrKindNotFound (never the shadow)", label, err)
	}
}

func TestResolveRev_DeletedRunBranchNeverResolvesShadow(t *testing.T) {
	ctx := context.Background()
	const run = "spine/run/x"
	for _, tc := range []struct {
		name    string
		shadows []string
	}{
		{"tag", []string{"refs/tags/" + run}},
		{"refs/<n>", []string{"refs/" + run}},
		{"tag and refs/<n>", []string{"refs/tags/" + run, "refs/" + run}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mainOID, shadow := deletedBranchFixture(t, run, tc.shadows...)
			c := git.NewCLIClient(repo)
			// The fixture's precondition: git itself resolves the shadow.
			if got := runGit(t, repo, "rev-parse", "--verify", run); got != shadow {
				t.Fatalf("fixture: git rev-parse %s = %s, want the shadow %s", run, got, shadow)
			}

			data, err := c.ReadFile(ctx, run, "f.md")
			if string(data) == "shadow\n" {
				t.Errorf("ReadFile(%s) read the shadow's content", run)
			}
			wantNotFound(t, "ReadFile", err)
			sha, err := c.RefSHA(ctx, run)
			if sha == shadow {
				t.Errorf("RefSHA(%s) = the shadow's id", run)
			}
			wantNotFound(t, "RefSHA", err)
			_, err = c.Diff(ctx, mainOID, run)
			wantNotFound(t, "Diff(main, run)", err)
			_, err = c.Diff(ctx, run, mainOID)
			wantNotFound(t, "Diff(run, main)", err)
			_, err = c.ListFiles(ctx, run, "")
			wantNotFound(t, "ListFiles", err)
			_, err = c.TreeEntryType(ctx, run, "f.md")
			wantNotFound(t, "TreeEntryType", err)
			_, err = c.MergeBase(ctx, "main", run)
			wantNotFound(t, "MergeBase", err)
			_, err = c.Log(ctx, git.LogOpts{Since: run})
			wantNotFound(t, "Log(since)", err)
		})
	}

	// The reserved namespace is the refadmit rule: the first component
	// folds to "spine" (a case variant is reserved too), whatever follows.
	t.Run("every reserved shape", func(t *testing.T) {
		for _, name := range []string{"spine/plan/p-1a2b", "spine/init", "spine/run-0a1b2c3d", "spine/r1/d1/b1", "SPINE/run/y"} {
			repo, _, _ := deletedBranchFixture(t, "keep/"+strings.ReplaceAll(name, "/", "-"))
			shadow := runGit(t, repo, "rev-parse", "HEAD")
			runGit(t, repo, "update-ref", "refs/tags/"+name, shadow)
			c := git.NewCLIClient(repo)
			_, err := c.ReadFile(ctx, name, "f.md")
			wantNotFound(t, "ReadFile("+name+")", err)
			_, err = c.RefSHA(ctx, name)
			wantNotFound(t, "RefSHA("+name+")", err)
		}
	})

	// The home branch of a shared tree: a missing home branch is not
	// substituted by a same-named tag either.
	t.Run("home branch", func(t *testing.T) {
		repo, mainOID, shadow := deletedBranchFixture(t, "trunk", "refs/tags/trunk")
		c := git.NewCLIClient(repo, git.WithHomeBranch("trunk"))
		_, err := c.ReadFile(ctx, "trunk", "f.md")
		wantNotFound(t, "ReadFile(home)", err)
		if sha, err := c.RefSHA(ctx, "trunk"); sha == shadow || err == nil {
			t.Errorf("RefSHA(home) = %s, %v; want NotFound, never the shadow", sha, err)
		}
		_, err = c.Diff(ctx, mainOID, "trunk")
		wantNotFound(t, "Diff(home)", err)
		// Another client of the same tree (the home branch is per tree).
		_, err = git.NewCLIClient(repo).ReadFile(ctx, "trunk", "f.md")
		wantNotFound(t, "ReadFile(home) through a second client", err)
	})

	// An existing branch still wins over every shadow, as before.
	t.Run("existing branch", func(t *testing.T) {
		repo, _, shadow := deletedBranchFixture(t, "keep", "refs/tags/"+run)
		runGit(t, repo, "branch", run, "main")
		c := git.NewCLIClient(repo)
		if data, err := c.ReadFile(ctx, run, "f.md"); err != nil || string(data) != "base\n" {
			t.Errorf("ReadFile(existing %s) = %q, %v; want the branch's content", run, data, err)
		}
		if sha, err := c.RefSHA(ctx, run); err != nil || sha == shadow {
			t.Errorf("RefSHA(existing %s) = %s, %v; want the branch", run, sha, err)
		}
	})
}

// AC 5: every other bare name still resolves as git resolves it.
func TestResolveRev_TagsAndAbbrevStillResolve(t *testing.T) {
	ctx := context.Background()
	c, repo, _, _, featOID := grammarRepo(t)
	runGit(t, repo, "tag", "v1.0", "feat")
	runGit(t, repo, "push", "-q", "origin", "main", "feat")
	runGit(t, repo, "fetch", "-q", "origin")
	runGit(t, repo, "tag", "spine-tools", "feat") // not the namespace: the first component is "spine-tools"
	runGit(t, repo, "tag", "release/r1", "feat")
	originMain := runGit(t, repo, "rev-parse", "refs/remotes/origin/main")
	for _, tc := range []struct{ rev, want string }{
		{"v1.0", featOID},
		{featOID[:10], featOID},
		{"origin/main", originMain},
		{"spine-tools", featOID},
		{"release/r1", featOID},
		{"t1", featOID},
	} {
		sha, err := c.RefSHA(ctx, tc.rev)
		if err != nil || sha != tc.want {
			t.Errorf("RefSHA(%s) = %s, %v; want %s", tc.rev, sha, err, tc.want)
		}
		if _, err := c.ReadFile(ctx, tc.rev, "f.txt"); err != nil {
			t.Errorf("ReadFile(%s): %v", tc.rev, err)
		}
		if _, err := c.ListFiles(ctx, tc.rev, ""); err != nil {
			t.Errorf("ListFiles(%s): %v", tc.rev, err)
		}
	}
	// A missing name outside the namespace is git's own miss, as before.
	_, err := c.ReadFile(ctx, "nope", "f.txt")
	wantNotFound(t, "ReadFile(nope)", err)
}
