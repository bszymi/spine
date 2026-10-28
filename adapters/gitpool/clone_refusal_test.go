package gitpool_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/domain"
)

// TestClient_CheckoutBudgetRefusalIsRememberedForATime (INIT-024 TASK-003
// review round 1, P3): a clone the checkout budget refuses is a permanent
// error (413-class, matching git.ErrTreeTooLarge), and is not retried —
// the remote fetched in full again — on the next access. The refusal
// expires after its TTL (a remote fixed upstream is cloned again), and a
// changed budget or another remote is a fresh attempt.
func TestClient_CheckoutBudgetRefusalIsRememberedForATime(t *testing.T) {
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	base := t.TempDir()
	repo := &repository.Repository{ID: "big", Status: "active",
		LocalPath: filepath.Join(base, "big"), CloneURL: "https://git.example/big.git"}
	resolver := &stubResolver{lookups: map[string]lookupResult{"big": {repo: repo}}}
	refusal := &git.GitError{Kind: git.ErrKindPermanent, Op: "clone", Message: git.TreeTooLargeMessage,
		Err: &git.TreeTooLargeError{Limit: git.TreeLimitEntries, Max: 10, Seen: 11}}
	cloner := &fakeCloner{failWith: refusal}
	now := time.Unix(1_000_000, 0)
	pool := newPool(t, &stubClient{}, resolver, func(string, gitpool.Credential) git.GitClient { return &stubClient{} },
		gitpool.WithCloner(cloner), gitpool.WithRepoBase(base), gitpool.WithClock(func() time.Time { return now }))

	for i := 0; i < 3; i++ {
		_, err := pool.Client(context.Background(), "big")
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrPayloadTooLarge || !errors.Is(err, git.ErrTreeTooLarge) {
			t.Fatalf("access %d: err = %v, want a permanent payload-too-large refusal", i, err)
		}
	}
	if n := cloner.callCount(); n != 1 {
		t.Fatalf("clone attempts = %d, want 1 (the refusal is remembered)", n)
	}

	now = now.Add(11 * time.Minute)
	_, _ = pool.Client(context.Background(), "big")
	if n := cloner.callCount(); n != 2 {
		t.Fatalf("after the TTL: clone attempts = %d, want 2", n)
	}

	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{MaxEntries: 1 << 20}})
	_, _ = pool.Client(context.Background(), "big")
	if n := cloner.callCount(); n != 3 {
		t.Fatalf("after a budget change: clone attempts = %d, want 3", n)
	}

	repo.CloneURL = "https://git.example/fixed.git"
	cloner.failWith = nil
	if _, err := pool.Client(context.Background(), "big"); err != nil {
		t.Fatalf("another remote: %v", err)
	}
	if n := cloner.callCount(); n != 4 {
		t.Fatalf("another remote: clone attempts = %d, want 4", n)
	}
}

// TestClient_ReusedCloneGetsTheNeutralAttributes (INIT-024 TASK-003
// review round 2): in a process with a checkout budget (`spine serve`
// installs it before any pool is used) an existing clone is given the
// neutral checkout attributes on first access — the budget's measure
// refuses without them; without a budget it is left alone.
func TestClient_ReusedCloneGetsTheNeutralAttributes(t *testing.T) {
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	access := func(t *testing.T) string {
		base := t.TempDir()
		local := filepath.Join(base, "code")
		if out, err := exec.Command("git", "init", "-q", local).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		repo := &repository.Repository{ID: "code", Status: "active", LocalPath: local, CloneURL: "https://git.example/code.git"}
		resolver := &stubResolver{lookups: map[string]lookupResult{"code": {repo: repo}}}
		pool := newPool(t, &stubClient{}, resolver, func(string, gitpool.Credential) git.GitClient { return &stubClient{} },
			gitpool.WithCloner(&fakeCloner{}), gitpool.WithRepoBase(base))
		if _, err := pool.Client(context.Background(), "code"); err != nil {
			t.Fatal(err)
		}
		return local
	}
	if ok, _ := git.HasNeutralCheckoutAttributes(context.Background(), access(t)); ok {
		t.Fatal("written without a budget")
	}
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{}})
	if ok, err := git.HasNeutralCheckoutAttributes(context.Background(), access(t)); !ok || err != nil {
		t.Fatalf("a reused clone in a budgeted process lacks the line: %v %v", ok, err)
	}
}

// TestClient_ReusedConvertedCloneIsRenormalised (INIT-024 TASK-003 review
// round 3, P3): a code-repository clone checked out before the upgrade
// with conversions on (a `* text eol=crlf` attribute, or the clone's own
// core.autocrlf) looks modified once the neutral line turns them off, and
// code-repository clones have no home branch, so no restore resets them:
// the first merge after the upgrade was refused ("local changes would be
// overwritten"). The pool's first access now brings the clone to its
// stored bytes, and the merge succeeds.
func TestClient_ReusedConvertedCloneIsRenormalised(t *testing.T) {
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	for _, tc := range []struct {
		name     string
		attrs    string
		autocrlf bool
	}{
		{"text eol=crlf", "* text eol=crlf\n", false},
		{"core.autocrlf", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			git.SetDefaultCheckoutLimits(git.CheckoutLimits{})
			base := t.TempDir()
			local := filepath.Join(base, "code")
			g := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", local}, args...)...)
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
					"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x.invalid", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x.invalid")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
			}
			if out, err := exec.Command("git", "init", "-q", "-b", "main", local).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v %s", err, out)
			}
			if tc.attrs != "" {
				if err := os.WriteFile(filepath.Join(local, ".gitattributes"), []byte(tc.attrs), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			g("add", "-A")
			g("commit", "-q", "-m", "files")
			g("checkout", "-q", "-b", "feature")
			if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			g("commit", "-q", "-am", "three")
			g("checkout", "-q", "main")
			if tc.autocrlf {
				g("config", "core.autocrlf", "true")
			}
			if err := os.Remove(filepath.Join(local, "a.txt")); err != nil {
				t.Fatal(err)
			}
			g("checkout", "--", "a.txt") // checked out with the conversion

			// The upgrade: spine serve installs a budget before any access.
			git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{}})
			repo := &repository.Repository{ID: "code", Status: "active", LocalPath: local, CloneURL: "https://git.example/code.git"}
			resolver := &stubResolver{lookups: map[string]lookupResult{"code": {repo: repo}}}
			pool := newPool(t, &stubClient{}, resolver, func(p string, _ gitpool.Credential) git.GitClient { return git.NewCLIClient(p) },
				gitpool.WithCloner(&fakeCloner{}), gitpool.WithRepoBase(base))
			client, err := pool.Client(context.Background(), "code")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Merge(context.Background(), git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit", Message: "m"}); err != nil {
				t.Fatalf("first merge after the upgrade: %v", err)
			}
		})
	}
}

// TestClient_ReusedCloneKeepsItsRealChanges (INIT-024 TASK-003 review
// round 4, P3): renormalising a reused clone quarantined and reset EVERY
// tracked change, a real uncommitted edit included. Now only the
// conversion-only file goes to its stored bytes: the edit, a staged
// file and an untracked file survive byte for byte, and the clone is no
// longer marked.
func TestClient_ReusedCloneKeepsItsRealChanges(t *testing.T) {
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{})
	base := t.TempDir()
	local := filepath.Join(base, "code")
	g := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", local}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x.invalid", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(local, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(local, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", local).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	write(".gitattributes", "* text eol=crlf\n")
	write("a.txt", "one\ntwo\n")
	write("real.txt", "real\n")
	g("add", "-A")
	g("commit", "-q", "-m", "files")
	for _, f := range []string{"a.txt", "real.txt"} {
		if err := os.Remove(filepath.Join(local, f)); err != nil {
			t.Fatal(err)
		}
	}
	g("checkout", "--", ".") // checked out with the conversion
	write("real.txt", "real\nuncommitted edit\n")
	write("staged.txt", "staged\n")
	g("add", "staged.txt")
	write("untracked.txt", "mine\r\n")

	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{}})
	repo := &repository.Repository{ID: "code", Status: "active", LocalPath: local, CloneURL: "https://git.example/code.git"}
	resolver := &stubResolver{lookups: map[string]lookupResult{"code": {repo: repo}}}
	pool := newPool(t, &stubClient{}, resolver, func(p string, _ gitpool.Credential) git.GitClient { return git.NewCLIClient(p) },
		gitpool.WithCloner(&fakeCloner{}), gitpool.WithRepoBase(base))
	if _, err := pool.Client(context.Background(), "code"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"a.txt": "one\ntwo\n", "real.txt": "real\nuncommitted edit\n", "staged.txt": "staged\n", "untracked.txt": "mine\r\n",
	} {
		if got := read(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if staged := g("diff", "--cached", "--name-only"); staged != "staged.txt\n" {
		t.Errorf("staged = %q, want staged.txt kept staged", staged)
	}
	if _, err := os.Lstat(filepath.Join(local, ".git", "spine-renormalise-pending")); err == nil {
		t.Error("the clone is still marked")
	}
}
