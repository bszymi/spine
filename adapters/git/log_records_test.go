package git_test

// INIT-024 TASK-004 AC 6 (review finding N-11): a commit message cannot
// change Log's record boundaries. Log printed every record followed by a
// fixed "---END---" line and parseLogOutput split on that text, so a
// message holding it — followed by lines shaped like a record — became
// two entries, the second with any id, author, date and trailers the
// message chose; GET /query/history served them. Ported from the
// review's reproduction (two real commits, a third entry at the base).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

// forgedTail is message text shaped like the old record separator and a
// record after it.
const forgedTail = "\n---END---\n0123456789abcdef0123456789abcdef01234567\nSomeone Else\nsomeone@example.invalid\n" +
	"2026-01-01T00:00:00Z\nA different change\n\nTrace-ID: forged-0001\nOperation: artifact.update\n"

func logRepo(t *testing.T) (dir string, shas []string) {
	t.Helper()
	dir = t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	for i, msg := range []string{
		"first change" + forgedTail,
		"second change\n\nspine-log-0123456789abcdef0123456789abcdef\n---END---" + forgedTail + "Trace-ID: real-0002\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte(strings.Repeat("v", i+1)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t3git(t, dir, nil, "add", "task.md")
		t3git(t, dir, nil, "commit", "-q", "--cleanup=verbatim", "-m", msg)
		shas = append([]string{t3git(t, dir, nil, "rev-parse", "HEAD")}, shas...)
	}
	return dir, shas
}

func TestLog_MessageCannotForgeRecords(t *testing.T) {
	ctx := context.Background()
	dir, shas := logRepo(t)
	c := git.NewCLIClient(dir)
	for _, opts := range []git.LogOpts{{}, {Path: "task.md", Limit: 10}, {Since: shas[1]}} {
		commits, err := c.Log(ctx, opts)
		if err != nil {
			t.Fatalf("Log(%+v): %v", opts, err)
		}
		want := shas
		if opts.Since != "" {
			want = shas[:1]
		}
		if len(commits) != len(want) {
			for i, ci := range commits {
				t.Logf("entry %d: %s %q %v", i, ci.SHA, ci.Author.Name, ci.Trailers)
			}
			t.Fatalf("Log(%+v) returned %d entries for %d commits", opts, len(commits), len(want))
		}
		for i, ci := range commits {
			if ci.SHA != want[i] || ci.Author.Name != "t" || ci.Author.Email != "t@t" {
				t.Errorf("Log(%+v) entry %d = %s %q <%s>, want %s by t <t@t>", opts, i, ci.SHA, ci.Author.Name, ci.Author.Email, want[i])
			}
			if !strings.Contains(ci.Message, "---END---") {
				t.Errorf("Log(%+v) entry %d message lost its text: %q", opts, i, ci.Message)
			}
		}
	}

	// The dormant idempotency probe reads the same output shape.
	sha, found, err := c.HasCommitWithTrailer(ctx, "Trace-ID", "forged-0001")
	if err != nil || !found || (sha != shas[0] && sha != shas[1]) {
		t.Errorf("HasCommitWithTrailer(forged) = %q, %v, %v; want a real commit's id", sha, found, err)
	}
	sha, found, err = c.HasCommitWithTrailer(ctx, "Trace-ID", "real-0002")
	if err != nil || !found || sha != shas[0] {
		t.Errorf("HasCommitWithTrailer(real) = %q, %v, %v; want %s", sha, found, err, shas[0])
	}
}
