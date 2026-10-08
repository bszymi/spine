package artifact

// Regression for INIT-023 TASK-004 (security audit 2026-10-06, finding F5),
// ported from the audit PoCs poc/git/safepath_dangling_test.go and
// poc/parsing/safepath_dangling_test.go. On 61994fe safePathIn accepted a
// dangling final-component symlink: filepath.EvalSymlinks failed on the
// leaf, the helper resolved only the parent and re-appended the unresolved
// leaf, and the prefix check passed.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/worktreefs"
)

func TestF5_SafePathIn_DanglingSymlinkLeaf_Rejected(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ link, target string }{
		{"a.md", filepath.Join(outside, "created.md")},
		{"dir/a.md", filepath.Join(outside, "not-yet.md")},
		{"dir/rel.md", "../../outside/rel.md"},
	} {
		if err := os.Symlink(c.target, filepath.Join(root, c.link)); err != nil {
			t.Fatal(err)
		}
		s := &Service{repo: root, artifactsDir: "/"}
		got, err := s.safePathIn(root, c.link)
		if err == nil {
			t.Errorf("safePathIn(%q) accepted a dangling symlink leaf: %q", c.link, got)
			continue
		}
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrInvalidParams || !errors.Is(err, worktreefs.ErrUnsafePath) {
			t.Errorf("safePathIn(%q) = %v, want ErrInvalidParams wrapping worktreefs.ErrUnsafePath", c.link, err)
		}
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("validation must not touch the filesystem; outside has %d entries", len(ents))
	}
}

// TestSafePath_SymlinkErrorDoesNotLeakFilesystemPaths extends INIT-008
// TASK-023's contract to the symlink refusal (AC10): the message names the
// repo-relative component only.
func TestSafePath_SymlinkErrorDoesNotLeakFilesystemPaths(t *testing.T) {
	svc := newBareService(t)
	outside := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "x.md"), filepath.Join(svc.repo, "evil.md")); err != nil {
		t.Fatal(err)
	}
	_, err := svc.safePath("evil.md")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	for _, leak := range []string{outside, svc.repo, "/tmp/", "/var/", "/home/", "/Users/", "/private/"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("safePath error leaked filesystem detail %q: %q", leak, msg)
		}
	}
	if !strings.Contains(msg, `"evil.md"`) {
		t.Fatalf("message should name the refused component: %q", msg)
	}
}
