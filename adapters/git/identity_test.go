package git_test

// INIT-024 TASK-010 (GitHub CI after the push): the client's commits and
// merges never depend on the host's git identity. Every test here runs with
// no identity at all (testutil.NoGitIdentity: no system or global config,
// no GIT_AUTHOR_*/GIT_COMMITTER_*, user.useConfigOnly so git guesses
// nothing), the environment of GitHub's runners and of Spine's standalone
// image; before identity.go each of the fallback cases failed "Committer
// identity unknown".

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

const (
	spineIdent = "Spine <spine@spine.local>"
	aliceIdent = "alice <alice@example.test>"
)

var alice = git.Author{Name: "alice", Email: "alice@example.test"}

// idGit runs a fixture git command; extra is appended to the environment
// (fixture commits carry their own identity, so the client's are the only
// commits that need the fallback).
func idGit(t *testing.T, dir string, extra []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extra...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

var fixtureIdent = []string{"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.test",
	"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.test"}

// idRepo: main with one commit, and feature and other each one commit
// ahead of it (so merging either into main makes a merge commit once the
// other is in). The repository has no user.name or user.email.
func idRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	idGit(t, dir, nil, "init", "-q", "-b", "main")
	idWrite(t, dir, "base.md", "base\n")
	idGit(t, dir, nil, "add", "-A")
	idGit(t, dir, fixtureIdent, "commit", "-q", "-m", "base")
	for _, b := range []string{"feature", "other"} {
		idGit(t, dir, nil, "checkout", "-q", "-b", b, "main")
		idWrite(t, dir, b+".md", b+"\n")
		idGit(t, dir, nil, "add", "-A")
		idGit(t, dir, fixtureIdent, "commit", "-q", "-m", b)
	}
	idGit(t, dir, nil, "checkout", "-q", "main")
	return dir
}

func idWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil { //nolint:gosec // G306: test fixture
		t.Fatal(err)
	}
}

// idents returns "author|committer" of rev.
func idents(t *testing.T, dir, rev string) string {
	t.Helper()
	return idGit(t, dir, nil, "log", "-1", "--format=%an <%ae>|%cn <%ce>", rev)
}

// TestNoHostIdentity_CommitPathsFallBackToSpine: every commit-creating
// path of the client succeeds with no identity anywhere. The committer is
// Spine; the author is the actor where the caller names one, Spine where
// it names none.
func TestNoHostIdentity_CommitPathsFallBackToSpine(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		// do runs the path in dir and returns the revision it made.
		do   func(t *testing.T, dir string) string
		want string // author|committer
	}{
		{"Commit with an author", func(t *testing.T, dir string) string {
			c := git.NewCLIClient(dir)
			if err := c.WriteAndStageFile(ctx, "a.md", "a\n"); err != nil {
				t.Fatal(err)
			}
			res, err := c.Commit(ctx, git.CommitOpts{Message: "a", Author: alice})
			if err != nil {
				t.Fatalf("Commit: %v", err)
			}
			return res.SHA
		}, aliceIdent + "|" + spineIdent},
		{"Commit without an author", func(t *testing.T, dir string) string {
			c := git.NewCLIClient(dir)
			if err := c.WriteAndStageFile(ctx, "a.md", "a\n"); err != nil {
				t.Fatal(err)
			}
			res, err := c.Commit(ctx, git.CommitOpts{Message: "a"})
			if err != nil {
				t.Fatalf("Commit: %v", err)
			}
			return res.SHA
		}, spineIdent + "|" + spineIdent},
		{"Merge, merge-commit strategy", func(t *testing.T, dir string) string {
			res, err := git.NewCLIClient(dir).Merge(ctx, git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit", Message: "m"})
			if err != nil {
				t.Fatalf("Merge: %v", err)
			}
			return res.SHA
		}, spineIdent + "|" + spineIdent},
		{"Merge, default strategy, diverged", func(t *testing.T, dir string) string {
			c := git.NewCLIClient(dir)
			if _, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main", Strategy: "fast-forward"}); err != nil {
				t.Fatalf("fast-forward needs no identity: %v", err)
			}
			res, err := c.Merge(ctx, git.MergeOpts{Source: "other", Target: "main"})
			if err != nil {
				t.Fatalf("Merge: %v", err)
			}
			return res.SHA
		}, spineIdent + "|" + spineIdent},
		{"MergeBound", func(t *testing.T, dir string) string {
			src := idGit(t, dir, nil, "rev-parse", "feature")
			res, err := git.NewCLIClient(dir).MergeBound(ctx, git.MergeOpts{SourceSHA: src, Target: "main", Strategy: "merge-commit", Message: "m"})
			if err != nil {
				t.Fatalf("MergeBound: %v", err)
			}
			return res.SHA
		}, spineIdent + "|" + spineIdent},
		{"CommitFilesOnto with an author", func(t *testing.T, dir string) string {
			base := idGit(t, dir, nil, "rev-parse", "main")
			res, err := git.NewCLIClient(dir).CommitFilesOnto(ctx, base, map[string]string{"c.md": "c\n"}, git.CommitOpts{Message: "c", Author: alice})
			if err != nil {
				t.Fatalf("CommitFilesOnto: %v", err)
			}
			return res.SHA
		}, aliceIdent + "|" + spineIdent},
		{"CommitFilesOnto without an author", func(t *testing.T, dir string) string {
			base := idGit(t, dir, nil, "rev-parse", "main")
			res, err := git.NewCLIClient(dir).CommitFilesOnto(ctx, base, map[string]string{"c.md": "c\n"}, git.CommitOpts{Message: "c"})
			if err != nil {
				t.Fatalf("CommitFilesOnto: %v", err)
			}
			return res.SHA
		}, spineIdent + "|" + spineIdent},
		{"StageAndCommit in a branch scope", func(t *testing.T, dir string) string {
			scope, err := git.EnterBranch(ctx, dir, "feature", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer scope.Cleanup()
			idWrite(t, scope.RepoDir, "s.md", "s\n")
			res, err := git.StageAndCommit(ctx, scope, "s.md", git.CommitOpts{Message: "s", Author: alice})
			if err != nil {
				t.Fatalf("StageAndCommit: %v", err)
			}
			return res.SHA
		}, aliceIdent + "|" + spineIdent},
		{"StageAndCommit in place, no author", func(t *testing.T, dir string) string {
			scope, err := git.EnterBranch(ctx, dir, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer scope.Cleanup()
			idWrite(t, scope.RepoDir, "s.md", "s\n")
			res, err := git.StageAndCommit(ctx, scope, "s.md", git.CommitOpts{Message: "s"})
			if err != nil {
				t.Fatalf("StageAndCommit: %v", err)
			}
			return res.SHA
		}, spineIdent + "|" + spineIdent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testutil.NoGitIdentity(t)
			dir := idRepo(t)
			sha := tc.do(t, dir)
			if got := idents(t, dir, sha); got != tc.want {
				t.Fatalf("author|committer = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNoHostIdentity_ConfiguredIdentityIsKept: the fallback is only a
// fallback. An identity git resolves — the repository's own config (how
// SMP's embedded scopes and Provision's repositories are set up), or the
// process environment — is used exactly as before, for the committer and
// for an author the caller does not name.
func TestNoHostIdentity_ConfiguredIdentityIsKept(t *testing.T) {
	ctx := context.Background()
	const ops = "Ops <ops@example.test>"
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"repo-local user.name and user.email", func(t *testing.T, dir string) {
			idGit(t, dir, nil, "config", "user.name", "Ops")
			idGit(t, dir, nil, "config", "user.email", "ops@example.test")
		}},
		{"repo-local committer.* and author.*", func(t *testing.T, dir string) {
			idGit(t, dir, nil, "config", "committer.name", "Ops")
			idGit(t, dir, nil, "config", "committer.email", "ops@example.test")
			idGit(t, dir, nil, "config", "author.name", "Ops")
			idGit(t, dir, nil, "config", "author.email", "ops@example.test")
		}},
		{"process environment", func(t *testing.T, _ string) {
			t.Setenv("GIT_AUTHOR_NAME", "Ops")
			t.Setenv("GIT_AUTHOR_EMAIL", "ops@example.test")
			t.Setenv("GIT_COMMITTER_NAME", "Ops")
			t.Setenv("GIT_COMMITTER_EMAIL", "ops@example.test")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.NoGitIdentity(t)
			dir := idRepo(t)
			tc.setup(t, dir)
			c := git.NewCLIClient(dir)
			if err := c.WriteAndStageFile(ctx, "a.md", "a\n"); err != nil {
				t.Fatal(err)
			}
			res, err := c.Commit(ctx, git.CommitOpts{Message: "a"})
			if err != nil {
				t.Fatalf("Commit: %v", err)
			}
			if got := idents(t, dir, res.SHA); got != ops+"|"+ops {
				t.Fatalf("Commit: author|committer = %q, want the configured %q for both", got, ops)
			}
			res, err = c.Commit(ctx, git.CommitOpts{Message: "b", Author: alice, AllowEmpty: true})
			if err != nil {
				t.Fatalf("Commit with an author: %v", err)
			}
			if got := idents(t, dir, res.SHA); got != aliceIdent+"|"+ops {
				t.Fatalf("Commit with an author: author|committer = %q, want %q", got, aliceIdent+"|"+ops)
			}
			m, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit", Message: "m"})
			if err != nil {
				t.Fatalf("Merge: %v", err)
			}
			if got := idents(t, dir, m.SHA); got != ops+"|"+ops {
				t.Fatalf("Merge: author|committer = %q, want %q", got, ops+"|"+ops)
			}
		})
	}
}

// TestNoHostIdentity_ResolvedIdentityRunsTheCommandUnchanged: when git
// resolves the identity the commit and merge processes get no identity
// variable from the client (their environment is what it was before
// identity.go), and when the environment alone names it no probe runs.
func TestNoHostIdentity_ResolvedIdentityRunsTheCommandUnchanged(t *testing.T) {
	ctx := context.Background()
	testutil.NoGitIdentity(t)
	dir := idRepo(t)
	idGit(t, dir, nil, "config", "user.name", "Ops")
	idGit(t, dir, nil, "config", "user.email", "ops@example.test")

	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "env.log")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// Records each command's subcommand and whether any identity variable
	// reached it.
	script := "#!/bin/sh\n" +
		"printf '%s %s\\n' \"$1\" \"${GIT_COMMITTER_NAME+id}${GIT_AUTHOR_NAME+id}${GIT_COMMITTER_EMAIL+id}${GIT_AUTHOR_EMAIL+id}\" >> \"$ID_ENVLOG\"\n" +
		"exec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: an executable test wrapper
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ID_ENVLOG", logPath)

	c := git.NewCLIClient(dir)
	if err := c.WriteAndStageFile(ctx, "a.md", "a\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(ctx, git.CommitOpts{Message: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit", Message: "m"}); err != nil {
		t.Fatal(err)
	}
	calls := readCalls(t, logPath)
	for _, sub := range []string{"commit", "merge"} {
		ran := false
		for _, call := range calls {
			if call[0] != sub {
				continue
			}
			ran = true
			if call[1] != "" {
				t.Errorf("git %s got an identity variable from the client; a resolved identity must run it unchanged", sub)
			}
		}
		if !ran {
			t.Errorf("git %s never ran: %q", sub, calls)
		}
	}
	if !hasCall(calls, "var") {
		t.Errorf("no identity probe ran for an identity only the config names: %q", calls)
	}

	// The environment names both identities: the client probes nothing.
	t.Setenv("GIT_AUTHOR_NAME", "Ops")
	t.Setenv("GIT_AUTHOR_EMAIL", "ops@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "Ops")
	t.Setenv("GIT_COMMITTER_EMAIL", "ops@example.test")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(ctx, git.CommitOpts{Message: "b", AllowEmpty: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Merge(ctx, git.MergeOpts{Source: "other", Target: "main", Strategy: "merge-commit", Message: "m2"}); err != nil {
		t.Fatal(err)
	}
	if calls := readCalls(t, logPath); hasCall(calls, "var") {
		t.Errorf("an identity the environment names was probed: %q", calls)
	}
}

// readCalls parses the wrapper's log: a [first argument, identity marks]
// pair per git process.
func readCalls(t *testing.T, path string) [][2]string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	var calls [][2]string
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		sub, marks, _ := strings.Cut(line, " ")
		calls = append(calls, [2]string{sub, marks})
	}
	return calls
}

func hasCall(calls [][2]string, sub string) bool {
	for _, call := range calls {
		if call[0] == sub {
			return true
		}
	}
	return false
}
