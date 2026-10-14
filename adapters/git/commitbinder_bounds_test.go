package git_test

// INIT-023 wave 3 integration (TASK-003 x TASK-005): the CommitBinder
// plumbing runs under TASK-003's read bounds. TASK-005 added its own
// runner (runWith) for the commands that need extra environment or stdin;
// before the integration it buffered stdout and stderr whole and ran in
// the host's locale, outside the bounds TASK-003 gives every other
// command, so a translated git turned a transient lock race into a
// refused binding and the "stderr keeps 64 KiB for every command"
// contract did not hold for these methods.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

// installGitWrapper puts a /bin/sh git wrapper first on PATH: body runs
// before the real git and may answer itself (exit) or fall through.
func installGitWrapper(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell wrapper")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\n" + body + "\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: an executable test wrapper
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// runWithSubcommands are the git subcommands only the CommitBinder
// methods' runner starts (plus rev-parse, which read commands share).
var runWithSubcommands = map[string]bool{
	"rev-parse": true, "merge-base": true, "rev-list": true, "read-tree": true, "ls-tree": true,
	"hash-object": true, "update-index": true, "write-tree": true, "log": true, "commit-tree": true,
	"update-ref": true, "symbolic-ref": true,
}

// TestCommitBinder_RunsInTheCLocale: every command the CommitBinder
// methods start runs with LC_ALL=C and an empty LANGUAGE, whatever the
// host's locale, and CommitFilesOnto's commit id does not depend on it.
func TestCommitBinder_RunsInTheCLocale(t *testing.T) {
	c, repo, _, mainOID, featOID := grammarRepo(t)
	ctx := context.Background()
	files := map[string]string{"f.txt": "cascade\n", "docs/new.md": "new\n"}
	opts := git.CommitOpts{Message: "cascade", Trailers: map[string]string{"Run-ID": "run-1", "Z-Extra": "z"}}
	want, err := c.CommitFilesOnto(ctx, featOID, files, opts)
	if err != nil {
		t.Fatalf("CommitFilesOnto (host locale unset): %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "env.log")
	installGitWrapper(t,
		"{ for a in \"$@\"; do printf '%s\\037' \"$a\"; done\n"+
			"  printf '\\036%s\\036%s\\n' \"${LC_ALL-unset}\" \"${LANGUAGE-unset}\"\n"+
			"} >> \""+logPath+"\"")
	t.Setenv("LC_ALL", "POSIX") // not C, and never translated
	t.Setenv("LANGUAGE", "t003")

	if _, err := c.ResolveBranchHead(ctx, "feat"); err != nil {
		t.Fatalf("ResolveBranchHead: %v", err)
	}
	if err := c.VerifyCommit(ctx, featOID); err != nil {
		t.Fatalf("VerifyCommit: %v", err)
	}
	if in, err := c.IsAncestor(ctx, mainOID, featOID); err != nil || !in {
		t.Fatalf("IsAncestor = %v, %v", in, err)
	}
	if _, err := c.CommitParents(ctx, featOID); err != nil {
		t.Fatalf("CommitParents: %v", err)
	}
	got, err := c.CommitFilesOnto(ctx, featOID, files, opts)
	if err != nil {
		t.Fatalf("CommitFilesOnto: %v", err)
	}
	if got.SHA != want.SHA {
		t.Fatalf("CommitFilesOnto under the C locale = %s, want the same commit %s", got.SHA, want.SHA)
	}
	res, err := c.MergeBound(ctx, git.MergeOpts{SourceSHA: featOID, Target: "main", Strategy: "merge-commit", Message: "Merge bound"})
	if err != nil {
		t.Fatalf("MergeBound: %v", err)
	}
	if err := c.ResetBranch(ctx, "main", mainOID, res.SHA); err != nil {
		t.Fatalf("ResetBranch: %v", err)
	}
	// Read the log before any setup git of the test's own runs through
	// the wrapper.
	raw, err := os.ReadFile(logPath) //nolint:gosec // G304: the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	if head := runGit(t, repo, "rev-parse", "refs/heads/main"); head != mainOID {
		t.Fatalf("main = %s after the take-back, want %s", head, mainOID)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		parts := strings.Split(line, "\x1e")
		if len(parts) != 3 {
			t.Fatalf("unparseable record %q", line)
		}
		args := strings.Split(strings.TrimSuffix(parts[0], "\x1f"), "\x1f")
		sub := ""
		for i := 0; i < len(args); i++ {
			if args[i] == "-c" {
				i++
				continue
			}
			if !strings.HasPrefix(args[i], "-") {
				sub = args[i]
				break
			}
		}
		if !runWithSubcommands[sub] {
			continue // switch / merge (mutating class) and show-ref are pinned by TASK-003's tests
		}
		seen[sub] = true
		if parts[1] != "C" || parts[2] != "" {
			t.Errorf("git %s (args %q) ran with LC_ALL=%q LANGUAGE=%q, want C and empty", sub, args, parts[1], parts[2])
		}
	}
	for sub := range runWithSubcommands {
		if !seen[sub] {
			t.Errorf("git %s never ran; the test no longer covers it", sub)
		}
	}
}

// TestCommitBinder_TranslatedLockRaceStaysRetryable: the engine retries a
// VerifyCommit failure only when it is retryable (merge binding: a
// transient failure keeps the run in committing, any other refuses the
// binding). git words a lock race in the host's language, so before the
// integration a translated host turned it into a permanent refusal.
func TestCommitBinder_TranslatedLockRaceStaysRetryable(t *testing.T) {
	c, _, _, _, featOID := grammarRepo(t)
	installGitWrapper(t,
		"if [ \"$1\" = rev-parse ] && [ \"$3\" = --quiet ]; then\n"+
			"  if [ \"$LC_ALL\" = C ]; then echo \"fatal: Unable to create '/r/.git/packed-refs.lock': File exists.\" >&2\n"+
			"  else echo \"Schwerwiegend: Konnte '/r/.git/packed-refs.sperre' nicht erstellen: Datei existiert.\" >&2; fi\n"+
			"  exit 128\n"+
			"fi")
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")

	err := c.VerifyCommit(context.Background(), featOID)
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindTransient || !ge.IsRetryable() {
		t.Fatalf("VerifyCommit during a lock race on a translated host = %v, want a retryable ErrKindTransient", err)
	}
}

// TestCommitBinder_StderrIsBounded: a failing CommitBinder command keeps
// at most the head and tail of its stderr, with the deciding last line.
func TestCommitBinder_StderrIsBounded(t *testing.T) {
	c, _, _, mainOID, featOID := grammarRepo(t)
	res := refusedMerge(t, c, featOID)
	const last = "fatal: update_ref failed for ref 'refs/heads/main': t003 permanent"
	installGitWrapper(t,
		"if [ \"$1\" = update-ref ]; then\n"+
			"  i=0; while [ $i -lt 20000 ]; do echo 'warning: t003 filler line for a bounded stderr buffer'; i=$((i+1)); done >&2\n"+
			"  echo \""+last+"\" >&2; exit 128\n"+
			"fi")

	err := c.ResetBranch(context.Background(), "main", mainOID, res.SHA)
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindPermanent {
		t.Fatalf("ResetBranch = %v, want a permanent *git.GitError", err)
	}
	if len(ge.Message) > git.MaxStderrBytesForTest {
		t.Fatalf("error message keeps %d bytes of stderr, want <= %d", len(ge.Message), git.MaxStderrBytesForTest)
	}
	if !strings.Contains(ge.Message, last) || !strings.Contains(ge.Message, "[truncated]") {
		t.Fatalf("kept stderr lost its tail or truncation marker: %q…", ge.Message[:min(len(ge.Message), 120)])
	}
}

// TestCommitBinder_StdoutIsBounded: a CommitBinder command whose output
// overruns the per-command limit fails with ErrOutputTooLarge instead of
// growing the heap (and is never parsed).
func TestCommitBinder_StdoutIsBounded(t *testing.T) {
	_, repo, _, _, featOID := grammarRepo(t)
	installGitWrapper(t,
		"if [ \"$1\" = rev-list ]; then\n"+
			"  i=0; while [ $i -lt 2000 ]; do echo "+featOID+"; i=$((i+1)); done; exit 0\n"+
			"fi")
	c := git.NewCLIClient(repo, git.WithMaxOutputBytes(4<<10))
	_, err := c.CommitParents(context.Background(), featOID)
	assertOutputTooLarge(t, "CommitParents", err, "commit_parents", 4<<10)
}
