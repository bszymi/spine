package git_test

// Regression tests for INIT-023 TASK-004 (security audit 2026-10-06,
// finding F5, the merge-cascade sink CLIClient.WriteAndStageFile). On
// Spine 61994fe the method did filepath.Join + os.MkdirAll + os.WriteFile
// with no validation at all; each TestF5_* fails there except the mode
// guard, which passes before and after.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/worktreefs"
	"github.com/bszymi/spine/testutil"
)

func wsGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=a@x", "-c", "user.name=a"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestF5_WriteAndStageFile_SymlinkLeafToExistingOutside_Refused(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.md")
	if err := os.WriteFile(victim, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(repo, "task.md")); err != nil {
		t.Fatal(err)
	}
	wsGit(t, repo, "add", "-A")
	wsGit(t, repo, "commit", "-q", "-m", "plant")
	past := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(victim, past, past); err != nil {
		t.Fatal(err)
	}

	err := git.NewCLIClient(repo).WriteAndStageFile(context.Background(), "task.md", "status: Completed\n")
	b, _ := os.ReadFile(victim)
	fi, _ := os.Stat(victim)
	if string(b) != "orig" || !fi.ModTime().Equal(past) {
		t.Errorf("write followed the symlink to %s (content=%q)", victim, b)
	}
	if !errors.Is(err, worktreefs.ErrUnsafePath) {
		t.Fatalf("want a worktreefs.ErrUnsafePath refusal, got %v", err)
	}
	if st := wsGit(t, repo, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Fatalf("refusal must not stage or change anything:\n%s", st)
	}
}

func TestF5_WriteAndStageFile_TraversalAndDotGit_Refused(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	parent := filepath.Dir(repo)
	for _, p := range []string{"../f5-escape.md", ".git/info/attributes", "a/../../f5-escape2.md", ".GIT/info/attributes"} {
		err := git.NewCLIClient(repo).WriteAndStageFile(context.Background(), p, "* merge=union\n")
		if !errors.Is(err, worktreefs.ErrUnsafePath) {
			t.Errorf("%s: want a worktreefs.ErrUnsafePath refusal, got %v", p, err)
		}
	}
	for _, leak := range []string{filepath.Join(parent, "f5-escape.md"), filepath.Join(parent, "f5-escape2.md"), filepath.Join(repo, ".git", "info", "attributes")} {
		if _, err := os.Lstat(leak); err == nil {
			t.Errorf("file left at %s", leak)
			_ = os.Remove(leak)
		}
	}
}

// A symlinked directory above the leaf is refused as well (on 61994fe the
// write landed in the link target and `git add` then failed "beyond a
// symbolic link").
func TestF5_WriteAndStageFile_SymlinkedParentDir_Refused(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "tasks")); err != nil {
		t.Fatal(err)
	}
	err := git.NewCLIClient(repo).WriteAndStageFile(context.Background(), "tasks/t.md", "x")
	if !errors.Is(err, worktreefs.ErrUnsafePath) {
		t.Fatalf("want a worktreefs.ErrUnsafePath refusal, got %v", err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatal("file written through the symlinked directory")
	}
}

// Guard (passes before and after): overwriting a regular tracked file keeps
// working and keeps its executable bit (the fix replaces via rename), and a
// new file in a new directory is created and staged.
func TestF5_WriteAndStageFile_RegularFile_ContentAndModePreserved(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	p := filepath.Join(repo, "tool.md")
	if err := os.WriteFile(p, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	wsGit(t, repo, "add", "-A")
	wsGit(t, repo, "commit", "-q", "-m", "exec file")
	client := git.NewCLIClient(repo)
	if err := client.WriteAndStageFile(context.Background(), "tool.md", "v2"); err != nil {
		t.Fatalf("WriteAndStageFile: %v", err)
	}
	b, _ := os.ReadFile(p)
	fi, _ := os.Lstat(p)
	if string(b) != "v2" || fi.Mode().Perm() != 0o755 || !fi.Mode().IsRegular() {
		t.Errorf("content=%q mode=%v", b, fi.Mode())
	}
	if err := client.WriteAndStageFile(context.Background(), "new/dir/n.md", "n"); err != nil {
		t.Fatalf("WriteAndStageFile (new): %v", err)
	}
	staged := wsGit(t, repo, "diff", "--cached", "--name-only")
	for _, want := range []string{"tool.md", "new/dir/n.md"} {
		if !strings.Contains(staged, want) {
			t.Errorf("%s not staged:\n%s", want, staged)
		}
	}
}

// A path that contains pathspec magic is staged literally
// (--literal-pathspecs, go-coding-guidelines §4.2).
func TestWriteAndStageFile_LiteralPathspec(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	for _, name := range []string{"a1.md", "a2.md"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("orig"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := git.NewCLIClient(repo).WriteAndStageFile(context.Background(), "a*.md", "glob"); err != nil {
		t.Fatalf("WriteAndStageFile: %v", err)
	}
	staged := strings.Fields(wsGit(t, repo, "diff", "--cached", "--name-only"))
	if len(staged) != 1 || staged[0] != "a*.md" {
		t.Fatalf("want only the literal \"a*.md\" staged, got %v", staged)
	}
}
