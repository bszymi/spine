package main

// INIT-024 TASK-003 (review finding N-1): `spine git-hook pre-receive`,
// run in-process the way git runs it — in $GIT_DIR, GIT_DIR set, the ref
// updates on stdin. Fixtures are a few tiny objects.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func ghGit(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x.invalid", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x.invalid")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// ghRepo is a bare repository holding a small commit and a bomb commit
// (50 entries of one 4 KiB blob: 200 KiB when written).
func ghRepo(t *testing.T) (dir, small, bomb, blob string) {
	t.Helper()
	dir = t.TempDir()
	ghGit(t, dir, nil, "init", "-q", "--bare")
	blob = ghGit(t, dir, bytes.Repeat([]byte("x"), 4<<10), "hash-object", "-w", "--stdin")
	smallTree := ghGit(t, dir, []byte("100644 blob "+blob+"\tone.md\n"), "mktree")
	small = ghGit(t, dir, nil, "commit-tree", smallTree, "-m", "small")
	var mk strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&mk, "100644 blob %s\tf%04d.md\n", blob, i)
	}
	inner := ghGit(t, dir, []byte(mk.String()), "mktree")
	root := ghGit(t, dir, []byte("040000 tree "+inner+"\tdocs\n"), "mktree")
	bomb = ghGit(t, dir, nil, "commit-tree", root, "-p", small, "-m", "bomb")
	return dir, small, bomb, blob
}

// runPreReceive runs `spine git-hook pre-receive <flags>` as git would.
func runPreReceive(t *testing.T, gitDir, stdin string, flags ...string) (string, error) {
	t.Helper()
	t.Chdir(gitDir)
	t.Setenv("GIT_DIR", ".")
	cmd := gitHookCmd()
	var stderr bytes.Buffer
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetErr(&stderr)
	cmd.SetOut(&stderr)
	cmd.SetArgs(append([]string{"pre-receive"}, flags...))
	err := cmd.Execute()
	return stderr.String(), err
}

const zero40 = "0000000000000000000000000000000000000000"

var ghBudget = []string{"--max-entries=1000", "--max-total-bytes=65536", "--max-path-bytes=1048576", "--timeout=30s"}

// TestGitHookPreReceive_RefusesOverBudgetTip: an over-budget new tip is
// refused (non-zero) with a line naming the limit and its variable.
func TestGitHookPreReceive_RefusesOverBudgetTip(t *testing.T) {
	dir, small, bomb, _ := ghRepo(t)
	in := small + " " + bomb + " refs/heads/run\n"
	out, err := runPreReceive(t, dir, in, ghBudget...)
	if err == nil {
		t.Fatalf("over-budget tip admitted:\n%s", out)
	}
	for _, want := range []string{"spine: push refused", `"refs/heads/run"`, "MaxTotalBytes", "SPINE_GIT_TREE_MAX_BYTES"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q:\n%s", want, out)
		}
	}
	// Entries, through the same flags.
	out, err = runPreReceive(t, dir, in, "--max-entries=10", "--max-total-bytes=1073741824")
	if err == nil || !strings.Contains(out, "SPINE_GIT_TREE_MAX_ENTRIES") {
		t.Fatalf("entry limit: %v\n%s", err, out)
	}
}

// TestGitHookPreReceive_AdmitsWithinBudget: tips within the budget, a
// delete and a repeated tip all pass (exit zero, no output).
func TestGitHookPreReceive_AdmitsWithinBudget(t *testing.T) {
	dir, small, _, _ := ghRepo(t)
	in := zero40 + " " + small + " refs/heads/a\n" +
		zero40 + " " + small + " refs/heads/b\n" +
		small + " " + zero40 + " refs/heads/gone\n"
	if out, err := runPreReceive(t, dir, in, ghBudget...); err != nil || out != "" {
		t.Fatalf("within budget: %v\n%s", err, out)
	}
	// No flags: the default budget.
	if out, err := runPreReceive(t, dir, in); err != nil {
		t.Fatalf("default budget: %v\n%s", err, out)
	}
	if out, err := runPreReceive(t, dir, ""); err != nil {
		t.Fatalf("empty input: %v\n%s", err, out)
	}
}

// TestGitHookPreReceive_RefusesUnmeasurable: a tip that names no tree-ish
// (a blob), a missing object, a malformed line or bad flags are all
// refused — fail closed.
func TestGitHookPreReceive_RefusesUnmeasurable(t *testing.T) {
	dir, small, _, blob := ghRepo(t)
	missing := strings.Repeat("ab", 20)
	for _, tc := range []struct {
		name, in string
		flags    []string
	}{
		{"blob tip", zero40 + " " + blob + " refs/heads/x\n", ghBudget},
		{"missing object", zero40 + " " + missing + " refs/heads/x\n", ghBudget},
		{"malformed line", small + " refs/heads/x\n", ghBudget},
		{"abbreviated id", zero40 + " " + small[:12] + " refs/heads/x\n", ghBudget},
		{"bad flag", zero40 + " " + small + " refs/heads/x\n", []string{"--max-entries=lots"}},
		{"stray argument", zero40 + " " + small + " refs/heads/x\n", []string{"extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runPreReceive(t, dir, tc.in, tc.flags...)
			if err == nil {
				t.Fatalf("admitted:\n%s", out)
			}
			if !strings.Contains(out, "spine: push refused") {
				t.Fatalf("stderr:\n%s", out)
			}
		})
	}
}

// TestGitHookPreReceive_IsHidden: the command group is internal.
func TestGitHookPreReceive_IsHidden(t *testing.T) {
	cmd := gitHookCmd()
	if !cmd.Hidden {
		t.Fatal("git-hook is listed in the help")
	}
}
