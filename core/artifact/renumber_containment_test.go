package artifact_test

// INIT-023 TASK-004: the dormant renumber helpers are guarded rather than
// descoped (Decision 8). They refuse symbolic links instead of following
// them, and the link rewrite never enters .git or reads through a link.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/worktreefs"
)

func TestRenumberArtifact_SymlinkLeafRefused(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.md")
	if err := os.WriteFile(victim, []byte("---\nid: TASK-006\n---\n# TASK-006\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "tasks", "task-006-x.md")); err != nil {
		t.Fatal(err)
	}
	_, err := artifact.RenumberArtifact(dir, "tasks/task-006-x.md", "Task", "TASK-006", "TASK-007")
	if !errors.Is(err, worktreefs.ErrUnsafePath) {
		t.Fatalf("want a worktreefs refusal, got %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, "tasks", "task-007-x.md")); statErr == nil {
		t.Fatal("renumbered file written from a symlinked source")
	}
}

func TestRenumberArtifact_SymlinkedTargetDirRefused(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "epics", "epic-004-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "epics", "epic-004-x", "epic.md"), []byte("---\nid: EPIC-004\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The renamed directory already exists as a link out of the tree.
	if err := os.Symlink(outside, filepath.Join(dir, "epics", "epic-005-x")); err != nil {
		t.Fatal(err)
	}
	_, err := artifact.RenumberArtifact(dir, "epics/epic-004-x/epic.md", "Epic", "EPIC-004", "EPIC-005")
	if !errors.Is(err, worktreefs.ErrUnsafePath) {
		t.Fatalf("want a worktreefs refusal, got %v", err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatal("renumber wrote through a symlinked directory")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "epics", "epic-004-x", "epic.md")); statErr != nil {
		t.Fatalf("source removed after a refused write: %v", statErr)
	}
}

// UpdateLinksToRenamedArtifact over the repository root skips .git (on
// 61994fe filepath.Walk descended into it and rewrote any .md there) and
// never reads or writes through a symlinked .md.
func TestUpdateLinksToRenamedArtifact_SkipsGitDirAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	content := "---\nlinks:\n  - target: /tasks/task-001-old.md\n---\n"
	write := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "tasks", "child.md"))
	write(filepath.Join(dir, ".git", "notes.md"))
	victim := filepath.Join(outside, "victim.md")
	write(victim)
	past := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(victim, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "tasks", "link.md")); err != nil {
		t.Fatal(err)
	}

	for _, searchDir := range []string{"", ".", "/"} {
		if err := artifact.UpdateLinksToRenamedArtifact(dir, searchDir, "tasks/task-001-old.md", "tasks/task-001-new.md"); err != nil {
			t.Fatalf("UpdateLinksToRenamedArtifact(%q): %v", searchDir, err)
		}
	}
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read(filepath.Join(dir, "tasks", "child.md")); got == content {
		t.Fatal("regular artifact link was not rewritten")
	}
	if got := read(filepath.Join(dir, ".git", "notes.md")); got != content {
		t.Fatal("a file under .git was rewritten")
	}
	fi, _ := os.Stat(victim)
	if got := read(victim); got != content || !fi.ModTime().Equal(past) {
		t.Fatal("a symlink target outside the tree was rewritten")
	}
}
