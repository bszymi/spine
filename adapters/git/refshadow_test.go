package git_test

// INIT-023 TASK-002 regression tests for audit F10 (a pushed tag or ref
// shadows a run branch or main) at the adapter level, plus the Checkout,
// EnterBranch and CurrentBranch rules. Every test builds hermetic
// repositories (testutil.HermeticGit) and plants shadow refs with plain
// git, so they do not depend on push admission (TASK-016). Test names
// match the git-floor CI job's -run filter (git 2.39.5).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// shadowFixture is an engine-style repository with a bare origin: main
// at base, a reviewed run branch one commit ahead, and an unreferenced
// attacker commit (also on base) that changes f.txt and adds evil.txt.
type shadowFixture struct {
	repo, origin string
	branch       string
	base         string
	reviewed     string
	evil         string
	blob         string
}

func newShadowFixture(t *testing.T) *shadowFixture {
	t.Helper()
	testutil.HermeticGit(t)
	root := t.TempDir()
	f := &shadowFixture{
		repo:   filepath.Join(root, "engine"),
		origin: filepath.Join(root, "origin.git"),
		branch: "spine/run/task-001-shadow-0a1b2c3d",
	}
	runGit(t, root, "init", "-q", "--bare", f.origin)
	runGit(t, root, "init", "-q", "-b", "main", f.repo)
	runGit(t, f.repo, "remote", "add", "origin", f.origin)
	testutil.WriteFile(t, f.repo, "f.txt", "base\n")
	runGit(t, f.repo, "add", "f.txt")
	runGit(t, f.repo, "commit", "-qm", "base")
	f.base = runGit(t, f.repo, "rev-parse", "HEAD")
	runGit(t, f.repo, "push", "-q", "origin", "refs/heads/main:refs/heads/main")

	runGit(t, f.repo, "switch", "-q", "-c", f.branch)
	testutil.WriteFile(t, f.repo, "f.txt", "reviewed\n")
	runGit(t, f.repo, "commit", "-qam", "reviewed")
	f.reviewed = runGit(t, f.repo, "rev-parse", "HEAD")

	runGit(t, f.repo, "switch", "-q", "-c", "tmp-attacker", "main")
	testutil.WriteFile(t, f.repo, "f.txt", "EVIL\n")
	testutil.WriteFile(t, f.repo, "evil.txt", "EVIL\n")
	runGit(t, f.repo, "add", "f.txt", "evil.txt")
	runGit(t, f.repo, "commit", "-qm", "attacker")
	f.evil = runGit(t, f.repo, "rev-parse", "HEAD")
	runGit(t, f.repo, "switch", "-q", "main")
	runGit(t, f.repo, "branch", "-q", "-D", "tmp-attacker")

	blobFile := filepath.Join(root, "blob.txt")
	if err := os.WriteFile(blobFile, []byte("not a commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.blob = runGit(t, f.repo, "hash-object", "-w", blobFile)
	return f
}

func (f *shadowFixture) client() *git.CLIClient { return git.NewCLIClient(f.repo) }

func (f *shadowFixture) plant(t *testing.T, ref, oid string) {
	t.Helper()
	runGit(t, f.repo, "update-ref", ref, oid)
}

func wantKind(t *testing.T, label string, err error, kind git.GitErrorKind) {
	t.Helper()
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != kind {
		t.Errorf("%s: err = %v, want a *GitError of kind %s", label, err, kind)
	}
}

// TestRefShadow_ReadsResolveBranchFirst is AC 4 (the audit's bare-name
// read PoC, inverted): with refs/tags/<b> and refs/<b> on an attacker
// commit, every read of the bare branch name returns the branch; every
// other input resolves exactly as before.
func TestRefShadow_ReadsResolveBranchFirst(t *testing.T) {
	f := newShadowFixture(t)
	f.plant(t, "refs/tags/"+f.branch, f.evil)
	f.plant(t, "refs/"+f.branch, f.evil)
	c := f.client()
	ctx := context.Background()
	b := f.branch

	if got, err := c.ReadFile(ctx, b, "f.txt"); err != nil || string(got) != "reviewed\n" {
		t.Errorf("ReadFile(%s) = %q, %v; want the branch content", b, got, err)
	}
	if got, err := c.RefSHA(ctx, b); err != nil || got != f.reviewed {
		t.Errorf("RefSHA(%s) = %q, %v; want %s", b, got, err, f.reviewed)
	}
	if got, err := c.MergeBase(ctx, b, f.reviewed); err != nil || got != f.reviewed {
		t.Errorf("MergeBase(%s, reviewed) = %q, %v; want %s", b, got, err, f.reviewed)
	}
	if got, err := c.Diff(ctx, "main", b); err != nil || !reflect.DeepEqual(got, []git.FileDiff{{Path: "f.txt", Status: "modified"}}) {
		t.Errorf("Diff(main, %s) = %+v, %v; want only f.txt modified", b, got, err)
	}
	if got, err := c.ListFiles(ctx, b, ""); err != nil || !reflect.DeepEqual(got, []string{"f.txt"}) {
		t.Errorf("ListFiles(%s) = %q, %v; want [f.txt]", b, got, err)
	}

	// Unchanged resolution for everything that is not an existing branch.
	f.plant(t, "refs/tags/rel", f.evil)
	f.plant(t, "refs/remotes/origin/"+b, f.evil)
	for _, rev := range []string{"rel", f.evil[:8], "refs/tags/" + b, f.evil, "refs/remotes/origin/" + b} {
		if got, err := c.ReadFile(ctx, rev, "f.txt"); err != nil || string(got) != "EVIL\n" {
			t.Errorf("ReadFile(%s) = %q, %v; want the attacker commit's content (unchanged resolution)", rev, got, err)
		}
	}
	if got, err := c.ReadFile(ctx, "HEAD", "f.txt"); err != nil || string(got) != "base\n" {
		t.Errorf("ReadFile(HEAD) = %q, %v", got, err)
	}
	if got, err := c.RefSHA(ctx, f.evil); err != nil || got != f.evil {
		t.Errorf("RefSHA(full oid) = %q, %v; a full object id must pass through unqualified", got, err)
	}

	// DC-6: only HEAD skips branch-first resolution; a branch whose name
	// merely ends in HEAD is still a branch.
	runGit(t, f.repo, "branch", "HOTFIXHEAD", f.reviewed)
	f.plant(t, "refs/tags/HOTFIXHEAD", f.evil)
	if got, err := c.ReadFile(ctx, "HOTFIXHEAD", "f.txt"); err != nil || string(got) != "reviewed\n" {
		t.Errorf("ReadFile(HOTFIXHEAD) = %q, %v; want the branch", got, err)
	}
}

// doublyQualifiedShadows are the names git's DWIM tries for a missing
// refs/heads/<n> (DC-2, DC-3).
func doublyQualifiedShadows(n string) []string {
	return []string{"refs/refs/heads/" + n, "refs/tags/refs/heads/" + n, "refs/heads/refs/heads/" + n}
}

// TestRefShadow_MissingBranchNeverFollowsShadow is AC 4's DC-2 addition
// and the CLIClient half of AC 1's third case: with the branch deleted
// and a doubly-qualified ref planted on the attacker commit, a qualified
// read is ErrKindNotFound, a bare read never returns the shadow, and
// Merge never merges it.
func TestRefShadow_MissingBranchNeverFollowsShadow(t *testing.T) {
	for i, name := range doublyQualifiedShadows("<branch>") {
		t.Run(name, func(t *testing.T) {
			f := newShadowFixture(t)
			shadow := doublyQualifiedShadows(f.branch)[i]
			runGit(t, f.repo, "branch", "-q", "-D", f.branch)
			f.plant(t, shadow, f.evil)
			c := f.client()
			ctx := context.Background()
			q := "refs/heads/" + f.branch

			_, err := c.ReadFile(ctx, q, "f.txt")
			wantKind(t, "ReadFile(qualified)", err, git.ErrKindNotFound)
			_, err = c.RefSHA(ctx, q)
			wantKind(t, "RefSHA(qualified)", err, git.ErrKindNotFound)
			_, err = c.ListFiles(ctx, q, "")
			wantKind(t, "ListFiles(qualified)", err, git.ErrKindNotFound)
			_, err = c.Diff(ctx, "main", q)
			wantKind(t, "Diff(qualified)", err, git.ErrKindNotFound)
			_, err = c.MergeBase(ctx, q, "main")
			wantKind(t, "MergeBase(qualified)", err, git.ErrKindNotFound)

			if got, err := c.ReadFile(ctx, f.branch, "f.txt"); err == nil && string(got) == "EVIL\n" {
				t.Errorf("ReadFile(bare) returned the shadow's content")
			}
			if got, _ := c.RefSHA(ctx, f.branch); got == f.evil {
				t.Errorf("RefSHA(bare) returned the shadow's object")
			}
			if got, _ := c.ListFiles(ctx, f.branch, ""); len(got) == 2 {
				t.Errorf("ListFiles(bare) listed the shadow's tree: %q", got)
			}
			if got, _ := c.Diff(ctx, "main", f.branch); len(got) == 2 {
				t.Errorf("Diff(bare) diffed against the shadow: %+v", got)
			}
			if got, _ := c.MergeBase(ctx, f.branch, f.evil); got == f.evil {
				t.Errorf("MergeBase(bare) resolved the shadow")
			}

			rec := testutil.InstallGitRecorder(t)
			for _, src := range []string{f.branch, q} {
				rec.Reset(t)
				_, err := c.Merge(ctx, git.MergeOpts{Source: src, Target: "main", Strategy: "merge-commit", Message: "Merge run"})
				wantKind(t, "Merge(Source: "+src+")", err, git.ErrKindNotFound)
				var ge *git.GitError
				if errors.As(err, &ge) && ge.IsRetryable() {
					t.Errorf("Merge(Source: %s): a missing branch must not be retryable", src)
				}
				for _, call := range rec.Calls(t) {
					if call[0] == "merge" || call[0] == "switch" {
						t.Errorf("Merge(Source: %s) ran %q for a missing source branch", src, call)
					}
				}
			}
			if got := runGit(t, f.repo, "rev-parse", "refs/heads/main"); got != f.base {
				t.Errorf("main moved to %s", got)
			}
			err = c.CreateBranch(ctx, "from-missing", f.branch)
			wantKind(t, "CreateBranch(base: missing branch)", err, git.ErrKindNotFound)
			err = c.Push(ctx, "origin", f.branch)
			wantKind(t, "Push(missing branch)", err, git.ErrKindPermanent)
		})
	}
}

// TestRefShadow_PushIgnoresLocalShadows is the adapter half of AC 2:
// Push(origin, main) succeeds and lands the local main with each local
// shadow present — a same-named tag (the original F10 denial of
// service), each doubly-qualified name (DC-1), and a refs/tags/<oid>
// that would substitute a raw object-id source.
func TestRefShadow_PushIgnoresLocalShadows(t *testing.T) {
	shadows := []string{"refs/tags/main"}
	shadows = append(shadows, doublyQualifiedShadows("main")...)
	shadows = append(shadows, "refs/tags/<local main oid>")
	for _, shadow := range shadows {
		t.Run(shadow, func(t *testing.T) {
			f := newShadowFixture(t)
			testutil.WriteFile(t, f.repo, "g.txt", "advance\n")
			runGit(t, f.repo, "add", "g.txt")
			runGit(t, f.repo, "commit", "-qm", "advance main")
			local := runGit(t, f.repo, "rev-parse", "refs/heads/main")
			ref := shadow
			if strings.Contains(shadow, "<local main oid>") {
				ref = "refs/tags/" + local
			}
			f.plant(t, ref, f.evil)
			if err := f.client().Push(context.Background(), "origin", "main"); err != nil {
				t.Fatalf("Push(origin, main) with local %s: %v", ref, err)
			}
			if got := runGit(t, f.origin, "rev-parse", "refs/heads/main"); got != local {
				t.Fatalf("origin main = %s, want local main %s", got, local)
			}
		})
	}
	t.Run("PushBranch with refs/tags/<branch>", func(t *testing.T) {
		f := newShadowFixture(t)
		f.plant(t, "refs/tags/"+f.branch, f.evil)
		if err := f.client().PushBranch(context.Background(), "origin", f.branch); err != nil {
			t.Fatalf("PushBranch: %v", err)
		}
		if got := runGit(t, f.origin, "rev-parse", "refs/heads/"+f.branch); got != f.reviewed {
			t.Fatalf("origin %s = %s, want the reviewed tip %s", f.branch, got, f.reviewed)
		}
		// owner question 4: no upstream tracking is written.
		cmd := runGitAllowFail(f.repo, "config", "--get", "branch."+f.branch+".merge")
		if cmd != "" {
			t.Errorf("PushBranch configured upstream tracking: %q", cmd)
		}
	})
}

// runGitAllowFail returns trimmed stdout, or "" when git fails.
func runGitAllowFail(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestRefShadow_RemoteDoubleQualifiedPushResidual is AC 17. It pins a
// documented RESIDUAL, not a fix: a doubly-qualified ref on the REMOTE
// makes every push to the branch it shadows fail ("dst refspec
// refs/heads/main matches more than one"), whatever the client argv.
// Only push admission can close it — Spine INIT-023 TASK-016
// (standalone) and SMP INIT-027 EPIC-004 TASK-005 §7 must refuse such
// names. What this test guarantees is that the failure is NOT retryable
// (the engine must not loop on it) and leaves the local branch alone.
func TestRefShadow_RemoteDoubleQualifiedPushResidual(t *testing.T) {
	for _, shadow := range doublyQualifiedShadows("main") {
		t.Run(shadow, func(t *testing.T) {
			f := newShadowFixture(t)
			runGit(t, f.repo, "push", "-q", "origin", f.evil+":"+shadow)
			testutil.WriteFile(t, f.repo, "g.txt", "advance\n")
			runGit(t, f.repo, "add", "g.txt")
			runGit(t, f.repo, "commit", "-qm", "advance main")
			local := runGit(t, f.repo, "rev-parse", "refs/heads/main")

			err := f.client().Push(context.Background(), "origin", "main")
			var ge *git.GitError
			if !errors.As(err, &ge) {
				t.Fatalf("Push with remote %s = %v, want a *GitError (residual)", shadow, err)
			}
			if ge.IsRetryable() {
				t.Fatalf("Push with remote %s is retryable (%v); the engine would loop", shadow, err)
			}
			if got := runGit(t, f.repo, "rev-parse", "refs/heads/main"); got != local {
				t.Fatalf("local main changed: %s, want %s", got, local)
			}
			if got := runGit(t, f.origin, "rev-parse", "refs/heads/main"); got != f.base {
				t.Fatalf("origin main moved to %s", got)
			}
		})
	}
}

// TestCheckout_NeverDetaches is AC 8.
func TestCheckout_NeverDetaches(t *testing.T) {
	f := newShadowFixture(t)
	c := f.client()
	ctx := context.Background()
	f.plant(t, "refs/tags/rel", f.evil)
	if err := c.Checkout(ctx, "rel"); err == nil {
		t.Fatal("Checkout of a tag-only name succeeded")
	}
	if got := runGit(t, f.repo, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD = %s after a refused checkout, want refs/heads/main", got)
	}

	f.plant(t, "refs/tags/"+f.branch, f.evil)
	if err := c.Checkout(ctx, f.branch); err != nil {
		t.Fatalf("Checkout(branch with a same-named tag): %v", err)
	}
	if got := runGit(t, f.repo, "symbolic-ref", "HEAD"); got != "refs/heads/"+f.branch {
		t.Fatalf("HEAD = %s, want refs/heads/%s", got, f.branch)
	}
	if got := runGit(t, f.repo, "rev-parse", "HEAD"); got != f.reviewed {
		t.Fatalf("HEAD at %s, want the branch tip %s", got, f.reviewed)
	}
}

// isolateTempDir points os.TempDir (and so EnterBranch's worktree
// parent) at an empty directory, so a test can assert that a refused
// EnterBranch leaves no temporary parent behind.
func isolateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%s is not empty after a refused EnterBranch: %v", dir, entries)
	}
}

func worktreeCount(t *testing.T, repo string) int {
	t.Helper()
	return strings.Count(runGit(t, repo, "worktree", "list", "--porcelain"), "worktree ")
}

// TestEnterBranch_RefusesWithoutLeftovers is AC 9 (refusals): a
// tag-only name, a branch already checked out elsewhere, and a broken
// post-condition each return an error and leave exactly one worktree
// and no temporary parent.
func TestEnterBranch_RefusesWithoutLeftovers(t *testing.T) {
	ctx := context.Background()
	t.Run("tag-only name", func(t *testing.T) {
		f := newShadowFixture(t)
		f.plant(t, "refs/tags/rel", f.evil)
		tmp := isolateTempDir(t)
		if _, err := git.EnterBranch(ctx, f.repo, "rel", nil); err == nil {
			t.Fatal("EnterBranch on a tag-only name succeeded")
		}
		if n := worktreeCount(t, f.repo); n != 1 {
			t.Errorf("worktree list has %d entries, want 1", n)
		}
		assertEmptyDir(t, tmp)
	})
	t.Run("branch already checked out", func(t *testing.T) {
		f := newShadowFixture(t)
		runGit(t, f.repo, "switch", "-q", f.branch)
		tmp := isolateTempDir(t)
		if _, err := git.EnterBranch(ctx, f.repo, f.branch, nil); err == nil {
			t.Fatal("EnterBranch on a branch checked out in the main worktree succeeded")
		}
		if n := worktreeCount(t, f.repo); n != 1 {
			t.Errorf("worktree list has %d entries, want 1", n)
		}
		assertEmptyDir(t, tmp)
	})
	t.Run("post-condition", func(t *testing.T) {
		f := newShadowFixture(t)
		testutil.InstallGitRecorder(t)
		t.Setenv("SPINE_GIT_RECORDER_FAKE_SYMREF", "refs/heads/somewhere-else")
		tmp := isolateTempDir(t)
		if _, err := git.EnterBranch(ctx, f.repo, f.branch, nil); err == nil || !strings.Contains(err.Error(), "worktree is not on") {
			t.Fatalf("EnterBranch with a failed post-condition = %v, want the post-condition error", err)
		}
		if n := worktreeCount(t, f.repo); n != 1 {
			t.Errorf("worktree list has %d entries, want 1", n)
		}
		assertEmptyDir(t, tmp)
	})
}

// TestEnterBranch_AttachesDespiteShadows is AC 9 (attach): a same-named
// tag, and a refs/<n> or refs/tags/<n> pointing at a blob (which made
// `worktree add <dir> <n>` die with "invalid reference" at 61994fe),
// never stop EnterBranch from landing on refs/heads/<n> at its tip.
func TestEnterBranch_AttachesDespiteShadows(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ name, ref, oid string }{
		{"tag on attacker commit", "refs/tags/", "evil"},
		{"refs/<n> on a blob", "refs/", "blob"},
		{"refs/tags/<n> on a blob", "refs/tags/", "blob"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newShadowFixture(t)
			oid := f.evil
			if tc.oid == "blob" {
				oid = f.blob
			}
			f.plant(t, tc.ref+f.branch, oid)
			scope, err := git.EnterBranch(ctx, f.repo, f.branch, nil)
			if err != nil {
				t.Fatalf("EnterBranch: %v", err)
			}
			defer scope.Cleanup()
			if got := runGit(t, scope.RepoDir, "symbolic-ref", "HEAD"); got != "refs/heads/"+f.branch {
				t.Fatalf("worktree HEAD = %s, want refs/heads/%s", got, f.branch)
			}
			if got := runGit(t, scope.RepoDir, "rev-parse", "HEAD"); got != f.reviewed {
				t.Fatalf("worktree at %s, want the branch tip %s", got, f.reviewed)
			}
		})
	}
}

// TestCurrentBranch_ShadowDetachedUnborn is AC 9 (CurrentBranch).
func TestCurrentBranch_ShadowDetachedUnborn(t *testing.T) {
	ctx := context.Background()
	f := newShadowFixture(t)
	runGit(t, f.repo, "switch", "-q", f.branch)
	f.plant(t, "refs/tags/"+f.branch, f.evil)
	if got, err := git.CurrentBranch(ctx, f.repo); err != nil || got != f.branch {
		t.Errorf("CurrentBranch with a same-named tag = %q, %v; want %q (not heads/...)", got, err, f.branch)
	}
	runGit(t, f.repo, "switch", "-q", "--detach")
	if got, err := git.CurrentBranch(ctx, f.repo); err != nil || got != "HEAD" {
		t.Errorf("CurrentBranch detached = %q, %v; want HEAD", got, err)
	}
	unborn := filepath.Join(t.TempDir(), "unborn")
	runGit(t, filepath.Dir(unborn), "init", "-q", "-b", "fresh", unborn)
	if got, err := git.CurrentBranch(ctx, unborn); err != nil || got != "fresh" {
		t.Errorf("CurrentBranch on an unborn branch = %q, %v; want fresh", got, err)
	}
	if _, err := git.CurrentBranch(ctx, t.TempDir()); err == nil {
		t.Error("CurrentBranch outside a repository returned no error")
	}
}

// TestRefShadow_CloneNoTagsAndErrorKinds is AC 10.
func TestRefShadow_CloneNoTagsAndErrorKinds(t *testing.T) {
	f := newShadowFixture(t)
	runGit(t, f.repo, "tag", "v1", f.base)
	runGit(t, f.repo, "tag", "-a", "-m", "annotated", "v2", f.base)
	ctx := context.Background()

	dst := filepath.Join(t.TempDir(), "clone")
	if err := git.NewCLIClient(dst).Clone(ctx, f.repo, dst); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if tags := runGit(t, dst, "for-each-ref", "refs/tags"); tags != "" {
		t.Errorf("clone imported tags: %q", tags)
	}
	if opt := runGit(t, dst, "config", "--get", "remote.origin.tagOpt"); opt != "--no-tags" {
		t.Errorf("remote.origin.tagOpt = %q, want --no-tags", opt)
	}
	runGit(t, dst, "fetch", "-q", "origin")
	if tags := runGit(t, dst, "for-each-ref", "refs/tags"); tags != "" {
		t.Errorf("a later fetch imported tags: %q", tags)
	}

	c := f.client()
	_, err := c.RefSHA(ctx, "no-such-ref")
	wantKind(t, "RefSHA(unknown)", err, git.ErrKindNotFound)
	_, err = c.RefSHA(ctx, "refs/tags/no-such-tag")
	wantKind(t, "RefSHA(unknown qualified tag)", err, git.ErrKindNotFound)
	_, err = c.ReadFile(ctx, "no-such-ref", "f.txt")
	wantKind(t, "ReadFile(unknown)", err, git.ErrKindNotFound)
	_, err = c.RefSHA(ctx, "refs/heads/no-such-branch")
	wantKind(t, "RefSHA(missing branch, exact lookup)", err, git.ErrKindNotFound)

	rec := testutil.InstallGitRecorder(t)
	rec.Reset(t)
	if _, err := c.ReadFile(ctx, "", "f.txt"); !errors.Is(err, git.ErrInvalidRef) {
		t.Errorf("ReadFile(\"\") = %v, want ErrInvalidRef (it used to read the index)", err)
	}
	if calls := rec.Calls(t); len(calls) != 0 {
		t.Errorf("ReadFile(\"\") ran git: %q", calls)
	}

	// A lookup that fails for another reason is returned as classified,
	// and nothing falls back to DWIM: only the lookup ran.
	notRepo := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(notRepo))
	rec.Reset(t)
	_, err = git.NewCLIClient(notRepo).ReadFile(ctx, "main", "f.txt")
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindPermanent || ge.Message != "not a git repository" {
		t.Errorf("ReadFile outside a repository = %v, want the classified \"not a git repository\"", err)
	}
	calls := rec.Calls(t)
	if len(calls) != 1 || calls[0][0] != "show-ref" {
		t.Errorf("a failed lookup was followed by %q; want the lookup only", calls)
	}
}
