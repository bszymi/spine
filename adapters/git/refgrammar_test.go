package git_test

// INIT-023 TASK-002: the ref grammar of CLIClient and EnterBranch.
// Test names start with RefGrammar so the git-floor CI job (git 2.39.5)
// selects them.

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

const (
	sha1Hex   = "0123456789abcdef0123456789abcdef01234567"
	sha256Hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	logFormat = "--format=%H%n%an%n%ae%n%aI%n%B%n---END---"
)

// grammarRepo builds a hermetic repository with main, a branch "feat"
// one commit ahead, a lightweight tag "t1" and a bare origin, and
// returns the client plus the object ids of main and feat.
func grammarRepo(t *testing.T) (c *git.CLIClient, repo, origin, mainOID, featOID string) {
	t.Helper()
	testutil.HermeticGit(t)
	repo = testutil.NewTempRepo(t)
	origin = filepath.Join(t.TempDir(), "origin.git")
	runGit(t, repo, "init", "-q", "--bare", origin)
	runGit(t, repo, "remote", "add", "origin", origin)
	testutil.WriteFile(t, repo, "f.txt", "base\n")
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-qm", "base")
	runGit(t, repo, "branch", "feat")
	runGit(t, repo, "switch", "-q", "feat")
	testutil.WriteFile(t, repo, "f.txt", "feat\n")
	runGit(t, repo, "commit", "-qam", "feat")
	runGit(t, repo, "switch", "-q", "main")
	runGit(t, repo, "tag", "t1", "feat")
	mainOID = runGit(t, repo, "rev-parse", "refs/heads/main")
	featOID = runGit(t, repo, "rev-parse", "refs/heads/feat")
	return git.NewCLIClient(repo), repo, origin, mainOID, featOID
}

// runGit runs a setup git command and returns its trimmed stdout.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("git %v: %v\n%s%s", args, err, out, stderr)
	}
	return strings.TrimSpace(string(out))
}

// assertRefused checks the grammar refusal contract: ErrInvalidRef,
// a Permanent *GitError, the "invalid git ref" text, and no git process.
func assertRefused(t *testing.T, rec *testutil.GitRecorder, label string, err error) {
	t.Helper()
	if !errors.Is(err, git.ErrInvalidRef) {
		t.Errorf("%s: err = %v, want ErrInvalidRef", label, err)
		return
	}
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindPermanent {
		t.Errorf("%s: err = %#v, want a Permanent *GitError", label, err)
	}
	if !strings.Contains(err.Error(), "invalid git ref") {
		t.Errorf("%s: error %q lacks \"invalid git ref\"", label, err)
	}
	if strings.ContainsAny(err.Error(), "\x00\n") {
		t.Errorf("%s: error carries a raw control byte: %q", label, err)
	}
	if calls := rec.Calls(t); len(calls) != 0 {
		t.Errorf("%s: refused call started git: %q", label, calls)
	}
}

// grammarInputs is the AC 5 input-class table. Columns: Branch class,
// Commit class (merge source), Commit class plus HEAD (CreateBranch
// base), Rev class. "Accepted" means the grammar lets the value through
// to resolution (it may still fail there, e.g. as ErrKindNotFound); an
// abbreviated id is a syntactically valid branch name, so the Commit
// class treats it as a branch and never as an object (exact lookup, no
// DWIM) — see TestRefGrammar_AbbreviatedIDIsNeverAnObject.
var grammarInputs = []struct {
	in                             string
	branch, commit, commitHEAD, rv bool
}{
	{"feat", true, true, true, true},
	{"refs/heads/feat", true, true, true, true},
	{sha1Hex, false, true, true, true},
	{sha256Hex, false, true, true, true},
	{"HEAD", false, false, true, true}, // also "HEAD used as a branch"
	{"abc1234", true, true, true, true},
	{"refs/tags/x", false, false, false, true},
	{"refs/remotes/origin/x", false, false, false, true},
	{"", false, false, false, false},
	{"-x", false, false, false, false},
	{"--force", false, false, false, false},
	{"a~1", false, false, false, false},
	{"a^", false, false, false, false},
	{"a:b", false, false, false, false},
	{"a..b", false, false, false, false},
	{"a@{1}", false, false, false, true},
	{"a\x00b", false, false, false, false},
	{"a\nb", false, false, false, false},
	{"a b", false, false, false, false},
	{"refs/heads/../x", false, false, false, false},
}

// TestRefGrammar_InputClassTable is AC 5: every method and ref-like
// parameter of the grammar, over every input class.
func TestRefGrammar_InputClassTable(t *testing.T) {
	c, repo, _, mainOID, _ := grammarRepo(t)
	rec := testutil.InstallGitRecorder(t)
	ctx := context.Background()

	type call struct {
		name  string
		class string // "branch", "commit", "commitHEAD", "rev"
		run   func(v string) error
	}
	calls := []call{
		{"Checkout", "branch", func(v string) error { return c.Checkout(ctx, v) }},
		{"Merge.Target", "branch", func(v string) error {
			_, err := c.Merge(ctx, git.MergeOpts{Source: mainOID, Target: v})
			return err
		}},
		{"CreateBranch.name", "branch", func(v string) error { return c.CreateBranch(ctx, v, "HEAD") }},
		{"DeleteBranch", "branch", func(v string) error { return c.DeleteBranch(ctx, v) }},
		{"Push.ref", "branch", func(v string) error { return c.Push(ctx, "origin", v) }},
		{"PushBranch.branch", "branch", func(v string) error { return c.PushBranch(ctx, "origin", v) }},
		{"DeleteRemoteBranch.branch", "branch", func(v string) error { return c.DeleteRemoteBranch(ctx, "origin", v) }},
		{"EnterBranch", "branch", func(v string) error {
			scope, err := git.EnterBranch(ctx, repo, v, nil)
			if err == nil {
				scope.Cleanup()
			}
			return err
		}},
		{"Merge.Source", "commit", func(v string) error {
			_, err := c.Merge(ctx, git.MergeOpts{Source: v})
			return err
		}},
		{"CreateBranch.base", "commitHEAD", func(v string) error { return c.CreateBranch(ctx, "grammar-new", v) }},
		{"ReadFile.ref", "rev", func(v string) error { _, err := c.ReadFile(ctx, v, "f.txt"); return err }},
		{"ListFiles.ref", "rev", func(v string) error { _, err := c.ListFiles(ctx, v, ""); return err }},
		{"Diff.from", "rev", func(v string) error { _, err := c.Diff(ctx, v, "HEAD"); return err }},
		{"Diff.to", "rev", func(v string) error { _, err := c.Diff(ctx, "HEAD", v); return err }},
		{"MergeBase.a", "rev", func(v string) error { _, err := c.MergeBase(ctx, v, "HEAD"); return err }},
		{"MergeBase.b", "rev", func(v string) error { _, err := c.MergeBase(ctx, "HEAD", v); return err }},
		{"RefSHA.ref", "rev", func(v string) error { _, err := c.RefSHA(ctx, v); return err }},
		{"Log.Since", "rev", func(v string) error { _, err := c.Log(ctx, git.LogOpts{Since: v}); return err }},
	}
	for _, cl := range calls {
		for _, in := range grammarInputs {
			want := map[string]bool{"branch": in.branch, "commit": in.commit, "commitHEAD": in.commitHEAD, "rev": in.rv}[cl.class]
			if in.in == "" && (cl.name == "Log.Since" || cl.name == "EnterBranch" || cl.name == "Merge.Target") {
				// Not a ref argument: an empty Since means "no lower
				// bound", an empty EnterBranch branch is the documented
				// unbranched scope (the main repository), and an empty
				// MergeOpts.Target means "the current branch" (audit
				// F8, INIT-023 TASK-012).
				continue
			}
			label := cl.name + "(" + strings.ReplaceAll(strings.ReplaceAll(in.in, "\x00", `\x00`), "\n", `\n`) + ")"
			rec.Reset(t)
			err := cl.run(in.in)
			if want {
				if errors.Is(err, git.ErrInvalidRef) {
					t.Errorf("%s: refused by the grammar (%v), want accepted", label, err)
				}
				continue
			}
			assertRefused(t, rec, label, err)
		}
	}

	// Remote class.
	for _, tc := range []struct {
		remote string
		ok     bool
	}{
		{"origin", true},
		{"upstream/mirror", true},
		{"", false},
		{"-v", false},
		{"--receive-pack=touch /tmp/x", false},
		{"https://evil/x", false},
		{"ext::sh", false},
		{"a..b", false},
		{"origin b", false},
	} {
		for name, run := range map[string]func() error{
			"Push":               func() error { return c.Push(ctx, tc.remote, "main") },
			"PushBranch":         func() error { return c.PushBranch(ctx, tc.remote, "main") },
			"DeleteRemoteBranch": func() error { return c.DeleteRemoteBranch(ctx, tc.remote, "no-such-branch") },
		} {
			label := name + ".remote(" + tc.remote + ")"
			rec.Reset(t)
			err := run()
			if tc.ok {
				if errors.Is(err, git.ErrInvalidRef) {
					t.Errorf("%s: refused by the grammar (%v), want accepted", label, err)
				}
				continue
			}
			assertRefused(t, rec, label, err)
		}
	}
}

// TestRefGrammar_TwoPhase is AC 5's DC-4 addition: a call with a valid
// first argument and an invalid second one (and the reverse) is refused
// before any git process — the valid argument is not looked up first.
func TestRefGrammar_TwoPhase(t *testing.T) {
	c, _, _, _, _ := grammarRepo(t)
	rec := testutil.InstallGitRecorder(t)
	ctx := context.Background()
	bad := "--upload-pack=x"
	for name, run := range map[string]func() error{
		"Diff(valid, bad)":           func() error { _, err := c.Diff(ctx, "feat", bad); return err },
		"Diff(bad, valid)":           func() error { _, err := c.Diff(ctx, bad, "feat"); return err },
		"MergeBase(valid, bad)":      func() error { _, err := c.MergeBase(ctx, "feat", bad); return err },
		"MergeBase(bad, valid)":      func() error { _, err := c.MergeBase(ctx, bad, "feat"); return err },
		"Merge(source, bad target)":  func() error { _, err := c.Merge(ctx, git.MergeOpts{Source: "feat", Target: bad}); return err },
		"Merge(bad source, target)":  func() error { _, err := c.Merge(ctx, git.MergeOpts{Source: bad, Target: "main"}); return err },
		"CreateBranch(valid, bad)":   func() error { return c.CreateBranch(ctx, "new", bad) },
		"CreateBranch(bad, valid)":   func() error { return c.CreateBranch(ctx, bad, "feat") },
		"Push(remote, bad)":          func() error { return c.Push(ctx, "origin", bad) },
		"Push(bad, branch)":          func() error { return c.Push(ctx, bad, "feat") },
		"PushBranch(remote, bad)":    func() error { return c.PushBranch(ctx, "origin", bad) },
		"PushBranch(bad, branch)":    func() error { return c.PushBranch(ctx, bad, "feat") },
		"DeleteRemote(remote, bad)":  func() error { return c.DeleteRemoteBranch(ctx, "origin", bad) },
		"DeleteRemote(bad, branch)":  func() error { return c.DeleteRemoteBranch(ctx, bad, "feat") },
		"Log(since bad, path valid)": func() error { _, err := c.Log(ctx, git.LogOpts{Since: bad, Path: "f.txt"}); return err },
	} {
		rec.Reset(t)
		assertRefused(t, rec, name, run())
	}
}

// TestRefGrammar_ArgvInjectionRefusedBeforeExec is AC 7: option-shaped
// values never reach git as a branch, commit, rev or remote.
func TestRefGrammar_ArgvInjectionRefusedBeforeExec(t *testing.T) {
	c, repo, _, _, _ := grammarRepo(t)
	rec := testutil.InstallGitRecorder(t)
	ctx := context.Background()
	for _, v := range []string{"--upload-pack=x", "-f", "--force", "--output=/tmp/x"} {
		for name, run := range map[string]func() error{
			"Checkout":           func() error { return c.Checkout(ctx, v) },
			"CreateBranch.name":  func() error { return c.CreateBranch(ctx, v, "HEAD") },
			"CreateBranch.base":  func() error { return c.CreateBranch(ctx, "new", v) },
			"DeleteBranch":       func() error { return c.DeleteBranch(ctx, v) },
			"Merge.Source":       func() error { _, err := c.Merge(ctx, git.MergeOpts{Source: v}); return err },
			"Merge.Target":       func() error { _, err := c.Merge(ctx, git.MergeOpts{Source: sha1Hex, Target: v}); return err },
			"Push":               func() error { return c.Push(ctx, "origin", v) },
			"PushBranch":         func() error { return c.PushBranch(ctx, "origin", v) },
			"DeleteRemoteBranch": func() error { return c.DeleteRemoteBranch(ctx, "origin", v) },
			"EnterBranch":        func() error { _, err := git.EnterBranch(ctx, repo, v, nil); return err },
			"ReadFile":           func() error { _, err := c.ReadFile(ctx, v, "f.txt"); return err },
			"ListFiles":          func() error { _, err := c.ListFiles(ctx, v, ""); return err },
			"RefSHA":             func() error { _, err := c.RefSHA(ctx, v); return err },
			"Diff":               func() error { _, err := c.Diff(ctx, "HEAD", v); return err },
			"MergeBase":          func() error { _, err := c.MergeBase(ctx, "HEAD", v); return err },
			"Log.Since":          func() error { _, err := c.Log(ctx, git.LogOpts{Since: v}); return err },
		} {
			rec.Reset(t)
			assertRefused(t, rec, name+"("+v+")", run())
		}
	}
	for _, r := range []string{"--receive-pack=touch /tmp/x", "-v", "https://evil/x", "ext::sh"} {
		for name, run := range map[string]func() error{
			"Push":               func() error { return c.Push(ctx, r, "main") },
			"PushBranch":         func() error { return c.PushBranch(ctx, r, "main") },
			"DeleteRemoteBranch": func() error { return c.DeleteRemoteBranch(ctx, r, "main") },
		} {
			rec.Reset(t)
			assertRefused(t, rec, name+".remote("+r+")", run())
		}
	}
}

// TestRefGrammar_ArgvShape is AC 6 (amended by DC-1, DC-2, DC-5): the
// exact argv of every method, including --end-of-options before the
// first ref, the trailing "--" on diff/show/log, exact branch lookups,
// object ids instead of branch names, and "<oid>^{commit}" push sources
// and CreateBranch start points (review round 1: `git branch`
// re-resolves a bare start point by DWIM).
func TestRefGrammar_ArgvShape(t *testing.T) {
	c, repo, origin, mainOID, featOID := grammarRepo(t)
	runGit(t, repo, "branch", "wt-branch", "feat")
	// A remote-tracking ref no step pushes or deletes (DeleteRemoteBranch
	// prunes refs/remotes/origin/feat).
	runGit(t, repo, "update-ref", "refs/remotes/origin/tracked", featOID)
	rec := testutil.InstallGitRecorder(t)
	ctx := context.Background()
	lookup := func(n string) []string { return []string{"show-ref", "--verify", "--hash", "refs/heads/" + n} }
	// probe is ReadFile's size probe (INIT-023 TASK-003), which precedes
	// the unchanged `git show`; a failed probe still runs the show, so a
	// miss keeps the show's error.
	probe := func(spec string) []string { return []string{"cat-file", "-s", "--end-of-options", spec} }
	cloneDst := filepath.Join(t.TempDir(), "clone")
	abbrev := featOID[:12]
	// notFound keeps a step whose expected result is ErrKindNotFound
	// green while still failing on any other result.
	notFound := func(err error) error {
		var ge *git.GitError
		if errors.As(err, &ge) && ge.Kind == git.ErrKindNotFound {
			return nil
		}
		return errors.Join(errors.New("want ErrKindNotFound"), err)
	}

	steps := []struct {
		name string
		run  func() error
		want [][]string
	}{
		{"Clone", func() error { return git.NewCLIClient(cloneDst).Clone(ctx, origin, cloneDst) },
			[][]string{{"clone", "--no-tags", "--", origin, cloneDst}}},
		{"Push main", func() error { return c.Push(ctx, "origin", "main") },
			[][]string{lookup("main"), {"push", "--end-of-options", "origin", mainOID + "^{commit}:refs/heads/main"}}},
		{"PushBranch feat", func() error { return c.PushBranch(ctx, "origin", "refs/heads/feat") },
			[][]string{lookup("feat"), {"push", "--end-of-options", "origin", featOID + "^{commit}:refs/heads/feat"}}},
		{"DeleteRemoteBranch feat", func() error { return c.DeleteRemoteBranch(ctx, "origin", "feat") },
			[][]string{{"push", "--delete", "--end-of-options", "origin", "refs/heads/feat"}}},
		{"Checkout feat", func() error { return c.Checkout(ctx, "feat") },
			[][]string{{"switch", "--no-guess", "--end-of-options", "feat"}}},
		{"Checkout refs/heads/main", func() error { return c.Checkout(ctx, "refs/heads/main") },
			[][]string{{"switch", "--no-guess", "--end-of-options", "main"}}},
		{"CreateBranch from HEAD", func() error { return c.CreateBranch(ctx, "n1", "HEAD") },
			[][]string{{"branch", "--end-of-options", "n1", "HEAD^{commit}"}}},
		{"CreateBranch from branch", func() error { return c.CreateBranch(ctx, "n2", "feat") },
			[][]string{lookup("feat"), {"branch", "--end-of-options", "n2", featOID + "^{commit}"}}},
		{"CreateBranch from oid", func() error { return c.CreateBranch(ctx, "n3", mainOID) },
			[][]string{{"branch", "--end-of-options", "n3", mainOID + "^{commit}"}}},
		{"DeleteBranch", func() error { return c.DeleteBranch(ctx, "n1") },
			[][]string{{"branch", "-D", "--end-of-options", "n1"}}},
		{"Diff branches", func() error { _, err := c.Diff(ctx, "main", "feat"); return err },
			[][]string{lookup("main"), lookup("feat"), {"diff", "--name-status", "-z", "--end-of-options", mainOID, featOID, "--"}}},
		{"Diff pass-through refs", func() error { _, err := c.Diff(ctx, "refs/tags/t1", "HEAD"); return err },
			[][]string{{"diff", "--name-status", "-z", "--end-of-options", "refs/tags/t1", "HEAD", "--"}}},
		{"Log since+path", func() error { _, err := c.Log(ctx, git.LogOpts{Limit: 2, Since: "main", Path: "f.txt"}); return err },
			[][]string{lookup("main"), {"log", logFormat, "-n2", "--end-of-options", mainOID + "..HEAD", "--", "f.txt"}}},
		{"Log plain", func() error { _, err := c.Log(ctx, git.LogOpts{}); return err },
			[][]string{{"log", logFormat, "--end-of-options", "--"}}},
		{"ReadFile branch", func() error { _, err := c.ReadFile(ctx, "feat", "f.txt"); return err },
			[][]string{lookup("feat"), probe(featOID + ":f.txt"), {"show", "--end-of-options", featOID + ":f.txt", "--"}}},
		{"ReadFile tag-only name", func() error { _, err := c.ReadFile(ctx, "t1", "f.txt"); return err },
			[][]string{lookup("t1"), probe("t1:f.txt"), {"show", "--end-of-options", "t1:f.txt", "--"}}},
		{"ReadFile HEAD", func() error { _, err := c.ReadFile(ctx, "HEAD", "f.txt"); return err },
			[][]string{probe("HEAD:f.txt"), {"show", "--end-of-options", "HEAD:f.txt", "--"}}},
		// The read-cost classes CHANGELOG and KNOWN-LIMITATIONS document
		// (review round 2): every bare name that is a valid branch name
		// pays one lookup whether or not the branch exists (tag, abbreviated
		// id, "origin/<n>", missing name), as does refs/heads/<n> (missing:
		// the lookup, then NotFound); a full object id, HEAD (above) and
		// any other refs/* pay none.
		{"ReadFile abbreviated id", func() error { _, err := c.ReadFile(ctx, abbrev, "f.txt"); return err },
			[][]string{lookup(abbrev), probe(abbrev + ":f.txt"), {"show", "--end-of-options", abbrev + ":f.txt", "--"}}},
		{"ReadFile remote-tracking short name", func() error { _, err := c.ReadFile(ctx, "origin/tracked", "f.txt"); return err },
			[][]string{lookup("origin/tracked"), probe("origin/tracked:f.txt"), {"show", "--end-of-options", "origin/tracked:f.txt", "--"}}},
		{"ReadFile missing bare name", func() error { _, err := c.ReadFile(ctx, "nope", "f.txt"); return notFound(err) },
			[][]string{lookup("nope"), probe("nope:f.txt"), {"show", "--end-of-options", "nope:f.txt", "--"}}},
		{"ReadFile refs/heads/ branch", func() error { _, err := c.ReadFile(ctx, "refs/heads/feat", "f.txt"); return err },
			[][]string{lookup("feat"), probe(featOID + ":f.txt"), {"show", "--end-of-options", featOID + ":f.txt", "--"}}},
		{"ReadFile missing refs/heads/ branch", func() error { _, err := c.ReadFile(ctx, "refs/heads/nope", "f.txt"); return notFound(err) },
			[][]string{lookup("nope")}},
		{"ReadFile object id", func() error { _, err := c.ReadFile(ctx, featOID, "f.txt"); return err },
			[][]string{probe(featOID + ":f.txt"), {"show", "--end-of-options", featOID + ":f.txt", "--"}}},
		{"ReadFile refs/tags/", func() error { _, err := c.ReadFile(ctx, "refs/tags/t1", "f.txt"); return err },
			[][]string{probe("refs/tags/t1:f.txt"), {"show", "--end-of-options", "refs/tags/t1:f.txt", "--"}}},
		{"ReadFile refs/remotes/", func() error { _, err := c.ReadFile(ctx, "refs/remotes/origin/tracked", "f.txt"); return err },
			[][]string{probe("refs/remotes/origin/tracked:f.txt"), {"show", "--end-of-options", "refs/remotes/origin/tracked:f.txt", "--"}}},
		{"ListFiles branch", func() error { _, err := c.ListFiles(ctx, "feat", ""); return err },
			[][]string{lookup("feat"), {"ls-tree", "-r", "--name-only", "-z", "--end-of-options", featOID}}},
		{"RefSHA branch", func() error { _, err := c.RefSHA(ctx, "feat"); return err },
			[][]string{lookup("feat")}},
		{"RefSHA HEAD", func() error { _, err := c.RefSHA(ctx, "HEAD"); return err },
			[][]string{{"rev-parse", "--verify", "--end-of-options", "HEAD"}}},
		{"RefSHA tag ref", func() error { _, err := c.RefSHA(ctx, "refs/tags/t1"); return err },
			[][]string{{"rev-parse", "--verify", "--end-of-options", "refs/tags/t1"}}},
		{"MergeBase", func() error { _, err := c.MergeBase(ctx, "main", "feat"); return err },
			[][]string{lookup("main"), lookup("feat"), {"merge-base", "--end-of-options", mainOID, featOID}}},
		{"Merge", func() error {
			_, err := c.Merge(ctx, git.MergeOpts{Source: "feat", Target: "main", Strategy: "merge-commit", Message: "m"})
			return err
		}, [][]string{lookup("feat"), {"switch", "--no-guess", "--end-of-options", "main"},
			{"merge", "--no-ff", "-m", "m", "--end-of-options", featOID}, {"rev-parse", "HEAD"}}},
		{"Merge oid fast-forward", func() error {
			_, err := c.Merge(ctx, git.MergeOpts{Source: mainOID, Target: "main", Strategy: "fast-forward"})
			return err
		}, [][]string{{"switch", "--no-guess", "--end-of-options", "main"},
			{"merge", "--ff-only", "--end-of-options", mainOID}, {"rev-parse", "HEAD"}}},
		{"Merge abort (strategy)", func() error {
			_, err := c.Merge(ctx, git.MergeOpts{Strategy: "abort", Source: "--abort"})
			_ = err // no merge in progress: git refuses, the argv is what matters
			return nil
		}, [][]string{{"merge", "--abort"}}},
		{"Merge abort (source sentinel)", func() error {
			_, _ = c.Merge(ctx, git.MergeOpts{Source: "--abort"})
			return nil
		}, [][]string{{"merge", "--abort"}}},
	}
	var all [][]string
	for _, st := range steps {
		rec.Reset(t)
		if err := st.run(); err != nil {
			t.Errorf("%s: %v", st.name, err)
		}
		got := rec.Calls(t)
		all = append(all, got...)
		if !reflect.DeepEqual(got, st.want) {
			t.Errorf("%s argv:\n got  %q\n want %q", st.name, got, st.want)
		}
	}

	// EnterBranch: exact lookup, detached add of the object id, switch,
	// post-condition; Cleanup removes the worktree.
	wtOID := runGit(t, repo, "rev-parse", "refs/heads/wt-branch")
	rec.Reset(t)
	scope, err := git.EnterBranch(ctx, repo, "wt-branch", nil)
	if err != nil {
		t.Fatalf("EnterBranch: %v", err)
	}
	got := rec.Calls(t)
	want := [][]string{
		lookup("wt-branch"),
		{"worktree", "add", "--detach", "--end-of-options", scope.RepoDir, wtOID},
		{"switch", "--no-guess", "--end-of-options", "wt-branch"},
		{"symbolic-ref", "-q", "HEAD"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnterBranch argv:\n got  %q\n want %q", got, want)
	}
	all = append(all, got...)
	rec.Reset(t)
	if b, err := git.CurrentBranch(ctx, scope.RepoDir); err != nil || b != "wt-branch" {
		t.Errorf("CurrentBranch in worktree = %q, %v", b, err)
	}
	if got := rec.Calls(t); !reflect.DeepEqual(got, [][]string{{"symbolic-ref", "-q", "HEAD"}}) {
		t.Errorf("CurrentBranch argv: %q", got)
	}
	scope.Cleanup()

	// No rev-parse anywhere is handed a refs/heads/ name: a qualified
	// name is DWIM-resolved by git when the branch is missing.
	for _, call := range all {
		if len(call) > 0 && call[0] == "rev-parse" {
			for _, a := range call[1:] {
				if strings.HasPrefix(a, "refs/heads/") {
					t.Errorf("rev-parse given a refs/heads/ name: %q", call)
				}
			}
		}
	}
}

// TestRefGrammar_AbbreviatedIDIsNeverAnObject pins the Commit-class rule
// for abbreviated ids: the value is a valid branch NAME, so it is looked
// up exactly as a branch and, with no such branch, refused as
// ErrKindNotFound — never resolved to the object it abbreviates.
func TestRefGrammar_AbbreviatedIDIsNeverAnObject(t *testing.T) {
	c, _, _, _, featOID := grammarRepo(t)
	rec := testutil.InstallGitRecorder(t)
	ctx := context.Background()
	abbrev := featOID[:8]
	rec.Reset(t)
	_, err := c.Merge(ctx, git.MergeOpts{Source: abbrev, Target: "main", Strategy: "fast-forward"})
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound {
		t.Fatalf("Merge(Source: abbreviated id) = %v, want ErrKindNotFound", err)
	}
	for _, call := range rec.Calls(t) {
		if call[0] == "merge" {
			t.Fatalf("a merge ran for an abbreviated id: %q", call)
		}
	}
	if err := c.CreateBranch(ctx, "from-abbrev", abbrev); !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound {
		t.Fatalf("CreateBranch(base: abbreviated id) = %v, want ErrKindNotFound", err)
	}
	// The Rev class still resolves it exactly as git does.
	if sha, err := c.RefSHA(ctx, abbrev); err != nil || sha != featOID {
		t.Fatalf("RefSHA(abbreviated id) = %q, %v; want %s", sha, err, featOID)
	}
}

// TestRefGrammar_ExportedPredicates pins the additive API.
func TestRefGrammar_ExportedPredicates(t *testing.T) {
	for _, s := range []string{sha1Hex, sha256Hex, strings.ToUpper(sha1Hex)} {
		if !git.IsFullObjectName(s) {
			t.Errorf("IsFullObjectName(%q) = false", s)
		}
	}
	for _, s := range []string{"", "abc1234", sha1Hex + "0", sha1Hex[:39], "g" + sha1Hex[1:]} {
		if git.IsFullObjectName(s) {
			t.Errorf("IsFullObjectName(%q) = true", s)
		}
	}
	for in, want := range map[string][2]string{
		"main":                  {"main", "refs/heads/main"},
		"refs/heads/main":       {"main", "refs/heads/main"},
		"spine/run/x-0a1b2c3d":  {"spine/run/x-0a1b2c3d", "refs/heads/spine/run/x-0a1b2c3d"},
		"refs/heads/release/v1": {"release/v1", "refs/heads/release/v1"},
	} {
		short, full, err := git.QualifyBranch(in)
		if err != nil || short != want[0] || full != want[1] {
			t.Errorf("QualifyBranch(%q) = %q, %q, %v; want %q, %q", in, short, full, err, want[0], want[1])
		}
	}
	for _, in := range []string{"", "HEAD", "@", "refs/heads/refs/heads/x", "refs/tags/v1", "refs/heads/", "-x", sha1Hex, "refs/heads/" + sha1Hex, "a..b"} {
		if _, _, err := git.QualifyBranch(in); err == nil {
			t.Errorf("QualifyBranch(%q) = nil error", in)
		}
		if err := git.ValidateBranchName(in); err == nil {
			t.Errorf("ValidateBranchName(%q) = nil error", in)
		}
		if err := git.ValidateShortBranchName(in); err == nil {
			t.Errorf("ValidateShortBranchName(%q) = nil error", in)
		}
	}
	// ValidateShortBranchName (stored configuration, review round 1):
	// the short name only; refs/heads/<n> is refused, not normalised.
	for _, in := range []string{"main", "release/2026.10", "spine/run/x-0a1b2c3d"} {
		if err := git.ValidateShortBranchName(in); err != nil {
			t.Errorf("ValidateShortBranchName(%q) = %v", in, err)
		}
	}
	for _, in := range []string{"refs/heads/main", "refs/heads/release/v1"} {
		if err := git.ValidateBranchName(in); err != nil {
			t.Errorf("ValidateBranchName(%q) = %v (the Branch class accepts refs/heads/<n>)", in, err)
		}
		if err := git.ValidateShortBranchName(in); err == nil {
			t.Errorf("ValidateShortBranchName(%q) = nil error, want the refs/heads/ form refused", in)
		}
	}
	for _, r := range []string{"origin", "upstream", "a.b-c_d", "x/y"} {
		if err := git.ValidateRemoteName(r); err != nil {
			t.Errorf("ValidateRemoteName(%q) = %v", r, err)
		}
	}
	for _, r := range []string{"", "-v", "https://evil/x", "ext::sh", "a..b", ".hidden", "a/", "a b", "/x"} {
		if err := git.ValidateRemoteName(r); err == nil {
			t.Errorf("ValidateRemoteName(%q) = nil", r)
		}
	}
}

// TestRefGrammar_LookupIgnoresHostLocale pins the codex review fix: the
// exact branch lookup recognises a missing branch by git's English
// "not a valid ref" marker, so it must run in the C locale. The shim
// below behaves like a translated git (it answers show-ref with a
// German message unless LC_ALL=C); with the lookup inheriting the host
// locale, every read of a tag-only name would fail closed.
func TestRefGrammar_LookupIgnoresHostLocale(t *testing.T) {
	c, _, _, _, featOID := grammarRepo(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	shim := "#!/bin/sh\n" +
		"if [ \"$1\" = show-ref ] && [ \"$LC_ALL\" != C ]; then\n" +
		"  echo \"fatal: '$4' - keine gueltige Referenz\" >&2\n" +
		"  exit 128\n" +
		"fi\n" +
		"exec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil { //nolint:gosec // G306: test shim must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")
	ctx := context.Background()
	if got, err := c.ReadFile(ctx, "t1", "f.txt"); err != nil || string(got) != "feat\n" {
		t.Fatalf("ReadFile(tag-only name) under a translated git = %q, %v", got, err)
	}
	if got, err := c.RefSHA(ctx, "feat"); err != nil || got != featOID {
		t.Fatalf("RefSHA(branch) under a translated git = %q, %v", got, err)
	}
	var ge *git.GitError
	if _, err := c.RefSHA(ctx, "refs/heads/no-such-branch"); !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound {
		t.Fatalf("RefSHA(missing qualified branch) under a translated git = %v, want ErrKindNotFound", err)
	}
}

// installFakeGitVersion puts a `git` on PATH that prints only a version
// line, for the CheckGitVersion tests.
func installFakeGitVersion(t *testing.T, line string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho '" + line + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: test shim must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRefGrammar_GitVersionFloor is AC 13: CheckGitVersion refuses git
// below 2.30 and accepts the shipped and common vendor formats.
func TestRefGrammar_GitVersionFloor(t *testing.T) {
	if git.MinGitVersion != "2.30" {
		t.Fatalf("MinGitVersion = %q, want 2.30", git.MinGitVersion)
	}
	for _, tc := range []struct {
		line string
		ok   bool
	}{
		{"git version 2.29.2", false},
		{"git version 1.99.0", false},
		{"git version 2.3.9", false},
		{"git version 2.30.0", true},
		{"git version 2.39.5", true},
		{"git version 2.54.0 (Apple Git-157)", true},
		{"git version 2.45.1.windows.1", true},
		{"git version 3.0.0", true},
		{"hub version 2.14.2", false},
		{"", false},
	} {
		installFakeGitVersion(t, tc.line)
		err := git.CheckGitVersion(context.Background())
		if tc.ok && err != nil {
			t.Errorf("CheckGitVersion(%q) = %v, want nil", tc.line, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("CheckGitVersion(%q) = nil, want an error", tc.line)
		}
	}
}

// TestRefGrammar_GitVersionOnThisHost runs the floor check against the
// real git, so a CI image below the floor fails loudly here.
func TestRefGrammar_GitVersionOnThisHost(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	if err := git.CheckGitVersion(context.Background()); err != nil {
		t.Fatalf("CheckGitVersion on this host: %v", err)
	}
}
