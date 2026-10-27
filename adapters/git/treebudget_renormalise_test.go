package git_test

// INIT-024 TASK-003 review round 4: renormalising a work tree checked out
// with conversions before the neutral line — by content, keeping real
// changes, retrying on a torn or cancelled HEAD, marking every worktree,
// and adding the line only under the tree's hold.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// rnClone is a repository whose tree was checked out with conversions
// (`* text eol=crlf`, `*.md ident`) and has no neutral line yet. f.txt and
// g.md are converted; real.txt is converted too (the tests edit it);
// plain.bin has no newline, so it is written as stored. nonRacy dates the
// files an hour back and refreshes the index, as in any long-lived clone:
// git then trusts the stat data and never re-reads them.
func rnClone(t *testing.T, nonRacy bool) string {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, ".gitattributes", "* text eol=crlf\n*.md ident\n")
	testutil.WriteFile(t, repo, "f.txt", "a\nb\n")
	testutil.WriteFile(t, repo, "g.md", "$Id$\nx\n")
	testutil.WriteFile(t, repo, "real.txt", "one\n")
	testutil.WriteFile(t, repo, "plain.bin", "abc")
	tbGit(t, repo, nil, "add", "-A")
	tbGit(t, repo, nil, "commit", "-q", "-m", "files")
	tbGit(t, repo, nil, "checkout", "-q", "-b", "feature")
	testutil.WriteFile(t, repo, "f.txt", "a\nb\nc\n")
	tbGit(t, repo, nil, "commit", "-q", "-am", "c")
	tbGit(t, repo, nil, "checkout", "-q", "main")
	for _, f := range []string{"f.txt", "g.md", "real.txt"} {
		if err := os.Remove(filepath.Join(repo, f)); err != nil {
			t.Fatal(err)
		}
	}
	tbGit(t, repo, nil, "checkout", "--", ".")
	if b := rnRead(t, repo, "f.txt"); b != "a\r\nb\r\n" {
		t.Fatalf("fixture: f.txt not converted: %q", b)
	}
	if b := rnRead(t, repo, "g.md"); !strings.HasPrefix(b, "$Id: ") {
		t.Fatalf("fixture: g.md ident not expanded: %q", b)
	}
	if nonRacy {
		old := time.Now().Add(-time.Hour)
		for _, f := range []string{".gitattributes", "f.txt", "g.md", "real.txt", "plain.bin"} {
			if err := os.Chtimes(filepath.Join(repo, f), old, old); err != nil {
				t.Fatal(err)
			}
		}
		tbGit(t, repo, nil, "update-index", "-q", "--refresh")
	}
	return repo
}

func rnRead(t *testing.T, repo, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func rnMarked(t *testing.T, dir string) bool {
	t.Helper()
	gd := tbGit(t, dir, nil, "rev-parse", "--absolute-git-dir")
	_, err := os.Lstat(filepath.Join(gd, git.RenormaliseMarkerName))
	return err == nil
}

// rnStored asserts f.txt and g.md hold their stored bytes.
func rnStored(t *testing.T, dir string) {
	t.Helper()
	if b := rnRead(t, dir, "f.txt"); b != "a\nb\n" {
		t.Errorf("f.txt = %q, want its stored bytes", b)
	}
	if b := rnRead(t, dir, "g.md"); b != "$Id$\nx\n" {
		t.Errorf("g.md = %q, want its stored bytes", b)
	}
}

func rnLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &logs
}

// TestNeutralAttributes_StatCleanConvertedTreeIsRenormalised (review
// round 4, P2): in a long-lived clone git trusts the index's stat data,
// so once the line is added a converted file still looks clean; the
// marker was cleared and the tree kept its converted bytes (a later
// read-modify-write commits them). Renormalising now compares content:
// the converted files go to their stored bytes, a file that was never
// converted is not rewritten, and the marker is cleared.
func TestNeutralAttributes_StatCleanConvertedTreeIsRenormalised(t *testing.T) {
	ctx := context.Background()
	repo := rnClone(t, true)
	if st := tbGit(t, repo, nil, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Fatalf("fixture: not clean under the old attributes:\n%s", st)
	}
	before, err := os.Stat(filepath.Join(repo, "plain.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := git.NewCLIClient(repo).EnsureNeutralWorkTree(ctx); err != nil {
		t.Fatal(err)
	}
	rnStored(t, repo)
	if rnMarked(t, repo) {
		t.Error("still marked")
	}
	after, err := os.Stat(filepath.Join(repo, "plain.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, after) {
		t.Error("plain.bin, never converted, was rewritten")
	}
	if st := tbGit(t, repo, nil, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("not clean after renormalising:\n%s", st)
	}
}

// TestNeutralAttributes_RealChangesAreLeftAsTheyWere (review round 4,
// P3): renormalising rewrote every tracked difference (quarantined, but
// gone from the tree). Now an unstaged edit, a staged change, an edit
// over a staged one, an untracked file and an edit git is told to ignore
// (assume-unchanged) survive byte for byte; only the
// conversion-only files go to their stored bytes; one Warn gives the
// count; the marker is cleared. Racy and long-lived clones alike.
func TestNeutralAttributes_RealChangesAreLeftAsTheyWere(t *testing.T) {
	for _, nonRacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "racy", true: "long-lived"}[nonRacy], func(t *testing.T) {
			ctx := context.Background()
			repo := rnClone(t, nonRacy)
			logs := rnLogs(t)
			testutil.WriteFile(t, repo, "real.txt", "one\nREAL EDIT\n")
			testutil.WriteFile(t, repo, "staged.txt", "staged\n")
			tbGit(t, repo, nil, "add", "staged.txt")
			testutil.WriteFile(t, repo, "g.md", "$Id$\nstaged edit\n")
			tbGit(t, repo, nil, "add", "g.md")
			testutil.WriteFile(t, repo, "g.md", "$Id$\nstaged edit\nand more\n")
			testutil.WriteFile(t, repo, "untracked.txt", "mine\r\n")
			testutil.WriteFile(t, repo, "hidden.txt", "hidden\n")
			tbGit(t, repo, nil, "add", "hidden.txt")
			tbGit(t, repo, nil, "commit", "-q", "-m", "hidden", "--", "hidden.txt")
			tbGit(t, repo, nil, "update-index", "--assume-unchanged", "hidden.txt")
			testutil.WriteFile(t, repo, "hidden.txt", "hidden edit\n")
			if err := git.NewCLIClient(repo).EnsureNeutralWorkTree(ctx); err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{
				"real.txt": "one\nREAL EDIT\n", "staged.txt": "staged\n",
				"g.md": "$Id$\nstaged edit\nand more\n", "untracked.txt": "mine\r\n",
				"hidden.txt": "hidden edit\n", // assume-unchanged: trusted as git trusts it
			} {
				if got := rnRead(t, repo, name); got != want {
					t.Errorf("%s = %q, want it left as %q", name, got, want)
				}
			}
			if b := rnRead(t, repo, "f.txt"); b != "a\nb\n" {
				t.Errorf("f.txt = %q, want its stored bytes", b)
			}
			if staged := tbGit(t, repo, nil, "diff", "--cached", "--name-only"); staged != "g.md\nstaged.txt" {
				t.Errorf("staged paths = %q, want g.md and staged.txt kept", staged)
			}
			if rnMarked(t, repo) {
				t.Error("still marked")
			}
			if n := strings.Count(logs.String(), "left_as_they_were=3"); n != 1 {
				t.Errorf("want one Warn counting the 3 recorded paths, got %d:\n%s", n, logs.String())
			}
		})
	}
}

// TestNeutralAttributes_MarkerWithoutARecordQuarantinesFirst (review
// round 4, P3 fallback): a marker that carries no record — one written
// by the round-3 build — cannot tell real changes from conversions, so
// the tracked state is quarantined (its SHA logged) before every
// unstaged difference is rewritten; a path with staged changes is still
// never rewritten.
func TestNeutralAttributes_MarkerWithoutARecordQuarantinesFirst(t *testing.T) {
	ctx := context.Background()
	repo := rnClone(t, false)
	logs := rnLogs(t)
	testutil.WriteFile(t, repo, "real.txt", "one\nREAL EDIT\n")
	testutil.WriteFile(t, repo, "g.md", "$Id$\nstaged\n")
	tbGit(t, repo, nil, "add", "g.md")
	testutil.WriteFile(t, repo, "g.md", "$Id$\nstaged\nunstaged\n")
	// The state the round-3 build leaves after a failed renormalisation.
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "attributes"), []byte(git.NeutralCheckoutAttributes+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", git.RenormaliseMarkerName),
		[]byte("the work tree is brought to its stored bytes by the next neutral-attributes call\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := git.NewCLIClient(repo).EnsureNeutralWorkTree(ctx); err != nil {
		t.Fatal(err)
	}
	if b := rnRead(t, repo, "f.txt"); b != "a\nb\n" {
		t.Errorf("f.txt = %q, want its stored bytes", b)
	}
	if b := rnRead(t, repo, "real.txt"); b != "one\n" {
		t.Errorf("real.txt = %q, want it rewritten (quarantined) in the fallback", b)
	}
	if b := rnRead(t, repo, "g.md"); b != "$Id$\nstaged\nunstaged\n" {
		t.Errorf("g.md (staged) = %q, want it never rewritten", b)
	}
	if rnMarked(t, repo) {
		t.Error("still marked")
	}
	i := strings.Index(logs.String(), "quarantine_sha=")
	if i < 0 {
		t.Fatalf("no quarantine SHA logged:\n%s", logs.String())
	}
	sha := strings.Fields(logs.String()[i+len("quarantine_sha="):])[0]
	if got := tbGit(t, repo, nil, "show", sha+":real.txt"); got != "one\nREAL EDIT" {
		t.Errorf("quarantine %s holds real.txt = %q, want the edit", sha, got)
	}
}

// TestNeutralAttributes_TornHEADKeepsTheMark (review round 4, P3): any
// failure to read HEAD was taken for an unborn HEAD and cleared the
// marker for good. A torn branch ref now returns the error and keeps the
// mark; once the ref is repaired the next call renormalises.
func TestNeutralAttributes_TornHEADKeepsTheMark(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"names no object", strings.Repeat("0", 39) + "1\n"},
		{"garbage", "garbage\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := rnClone(t, false)
			tight := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxEntries: 1}))
			if err := tight.EnsureNeutralWorkTree(ctx); !errors.Is(err, git.ErrRenormalisePending) {
				t.Fatalf("first call (HEAD over the budget): %v", err)
			}
			ref := filepath.Join(repo, ".git", "refs", "heads", "main")
			head, err := os.ReadFile(ref)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ref, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{}))
			if err := c.EnsureNeutralWorkTree(ctx); !errors.Is(err, git.ErrRenormalisePending) {
				t.Fatalf("torn HEAD: want ErrRenormalisePending, got %v", err)
			}
			if !rnMarked(t, repo) {
				t.Fatal("a torn HEAD cleared the mark")
			}
			if err := os.WriteFile(ref, head, 0o644); err != nil { //nolint:gosec // G703: the test's own temp repository
				t.Fatal(err)
			}
			if err := c.EnsureNeutralWorkTree(ctx); err != nil {
				t.Fatalf("after the repair: %v", err)
			}
			rnStored(t, repo)
			if rnMarked(t, repo) {
				t.Error("still marked after the repair")
			}
		})
	}
}

// TestNeutralAttributes_CancelledHEADProbeKeepsTheMark (review round 4,
// P3): a request cancelled while renormalise looks at HEAD is not an
// unborn HEAD: the error is returned, the mark kept, the next call
// renormalises.
func TestNeutralAttributes_CancelledHEADProbeKeepsTheMark(t *testing.T) {
	repo := rnClone(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	git.SetRenormaliseHeadProbeHookForTest(cancel)
	t.Cleanup(func() { git.SetRenormaliseHeadProbeHookForTest(nil) })
	err := git.NewCLIClient(repo).EnsureNeutralWorkTree(ctx)
	if !errors.Is(err, git.ErrRenormalisePending) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled at the HEAD probe: want ErrRenormalisePending and context.Canceled, got %v", err)
	}
	if !rnMarked(t, repo) {
		t.Fatal("a cancelled request cleared the mark")
	}
	git.SetRenormaliseHeadProbeHookForTest(nil)
	if err := git.NewCLIClient(repo).EnsureNeutralWorkTree(context.Background()); err != nil {
		t.Fatal(err)
	}
	rnStored(t, repo)
	if rnMarked(t, repo) {
		t.Error("still marked")
	}
}

// TestNeutralAttributes_UnbornHEADClearsTheMark (review round 4, P3): a
// real unborn HEAD (no commit yet) is still recognised: the line is
// added, nothing is checked out, the mark is cleared, an untracked file
// is left alone.
func TestNeutralAttributes_UnbornHEADClearsTheMark(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	tbGit(t, repo, nil, "init", "-q", "-b", "main")
	testutil.WriteFile(t, repo, "draft.txt", "draft\r\n")
	if err := git.NewCLIClient(repo).EnsureNeutralWorkTree(ctx); err != nil {
		t.Fatal(err)
	}
	if has, err := git.HasNeutralCheckoutAttributes(ctx, repo); !has || err != nil {
		t.Fatalf("line: %v %v", has, err)
	}
	if rnMarked(t, repo) {
		t.Error("an unborn HEAD left the mark")
	}
	if b := rnRead(t, repo, "draft.txt"); b != "draft\r\n" {
		t.Errorf("draft.txt = %q", b)
	}
}

// TestNeutralAttributes_LinkedWorktreesAreMarked (review round 4, P4):
// the line lives in the common directory, so it changes every worktree,
// but only the calling one was marked. Adding it now records and marks
// every worktree; a call on a linked worktree then renormalises it,
// keeping its recorded edit and quarantining what it cannot prove.
func TestNeutralAttributes_LinkedWorktreesAreMarked(t *testing.T) {
	ctx := context.Background()
	repo := rnClone(t, false)
	wt := filepath.Join(t.TempDir(), "wt")
	tbGit(t, repo, nil, "worktree", "add", "-q", "-b", "side", wt)
	if b := rnRead(t, wt, "f.txt"); b != "a\r\nb\r\n" {
		t.Fatalf("fixture: the linked worktree is not converted: %q", b)
	}
	testutil.WriteFile(t, wt, "real.txt", "one\nlinked edit\n")
	if err := git.NewCLIClient(repo).EnsureNeutralWorkTree(ctx); err != nil {
		t.Fatal(err)
	}
	rnStored(t, repo)
	if !rnMarked(t, wt) {
		t.Fatal("the linked worktree was not marked when the line was added")
	}
	// Its record was never proven by a comparison under the line (that
	// call renormalised only its own tree), so an edit made after the
	// line cannot be told from a conversion: quarantined first.
	logs := rnLogs(t)
	testutil.WriteFile(t, wt, "plain.bin", "abc after the line")
	if err := git.NewCLIClient(wt).EnsureNeutralWorkTree(ctx); err != nil {
		t.Fatal(err)
	}
	rnStored(t, wt)
	if b := rnRead(t, wt, "real.txt"); b != "one\nlinked edit\n" {
		t.Errorf("the linked worktree's recorded edit = %q, want it kept", b)
	}
	if rnMarked(t, wt) {
		t.Error("the linked worktree is still marked")
	}
	i := strings.Index(logs.String(), "quarantine_sha=")
	if i < 0 {
		t.Fatalf("no quarantine for the unproven linked worktree:\n%s", logs.String())
	}
	sha := strings.Fields(logs.String()[i+len("quarantine_sha="):])[0]
	if got := tbGit(t, wt, nil, "show", sha+":plain.bin"); got != "abc after the line" {
		t.Errorf("quarantine %s holds plain.bin = %q, want the later edit", sha, got)
	}
}

// TestNeutralAttributes_LineWaitsForTheTreesHolder (review round 4, P4):
// the line was added outside the tree's hold, so a sequence already
// holding the tree ran on under changed attributes and its merge over the
// converted files was refused. The call now waits for the hold before it
// adds the line: the sequence's merge succeeds, then the tree is
// renormalised.
func TestNeutralAttributes_LineWaitsForTheTreesHolder(t *testing.T) {
	outer := context.Background()
	repo := rnClone(t, false)
	c := git.NewCLIClient(repo)
	ensured := make(chan error, 1)
	err := c.WithWorkingTree(outer, func(ctx context.Context) error {
		// Not the sequence's ctx: its ticket would re-enter the hold.
		go func() { ensured <- git.NewCLIClient(repo).EnsureNeutralWorkTree(outer) }()
		deadline := time.Now().Add(20 * time.Second)
		for {
			if has, _ := git.HasNeutralCheckoutAttributes(ctx, repo); has {
				return errors.New("the line was added while a sequence held the tree")
			}
			if git.TreeRefsForTest(repo) >= 2 {
				break // the call waits for the tree
			}
			if time.Now().After(deadline) {
				return errors.New("the call neither waited for the tree nor added the line")
			}
			time.Sleep(5 * time.Millisecond)
		}
		_, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit", Message: "m"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-ensured; err != nil {
		t.Fatal(err)
	}
	if b := rnRead(t, repo, "f.txt"); b != "a\nb\nc\n" {
		t.Errorf("f.txt = %q, want the merged stored bytes", b)
	}
	if st := tbGit(t, repo, nil, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("not clean:\n%s", st)
	}
	if rnMarked(t, repo) {
		t.Error("still marked")
	}
}

// TestNeutralAttributes_EditAfterTheRecordIsKept (codex review round 4,
// P1): the record holds the real changes as they were when the line was
// added; a renormalisation that failed then (HEAD over the budget) and
// is retried later must not take an edit made in between for a
// conversion. Both an edit to a file that was clean then and an edit to
// a converted file survive the retry byte for byte; the untouched
// converted file goes to its stored bytes.
func TestNeutralAttributes_EditAfterTheRecordIsKept(t *testing.T) {
	ctx := context.Background()
	repo := rnClone(t, false)
	logs := rnLogs(t)
	tight := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxEntries: 1}))
	if err := tight.EnsureNeutralWorkTree(ctx); !errors.Is(err, git.ErrRenormalisePending) {
		t.Fatalf("first call (HEAD over the budget): %v", err)
	}
	testutil.WriteFile(t, repo, "plain.bin", "abc edited later")
	testutil.WriteFile(t, repo, "f.txt", "a\r\nb\r\nedited later\r\n")
	if err := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{})).EnsureNeutralWorkTree(ctx); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if b := rnRead(t, repo, "plain.bin"); b != "abc edited later" {
		t.Errorf("plain.bin = %q, want the later edit kept", b)
	}
	if b := rnRead(t, repo, "f.txt"); b != "a\r\nb\r\nedited later\r\n" {
		t.Errorf("f.txt = %q, want the later edit kept", b)
	}
	if b := rnRead(t, repo, "g.md"); b != "$Id$\nx\n" {
		t.Errorf("g.md = %q, want its stored bytes", b)
	}
	if rnMarked(t, repo) {
		t.Error("still marked")
	}
	if !strings.Contains(logs.String(), "changed_since_recorded=2") {
		t.Errorf("want the two later edits counted:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "quarantine_sha=") {
		t.Errorf("a proven retry quarantined:\n%s", logs.String())
	}
}
