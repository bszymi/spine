package artifact

// INIT-023 TASK-004: internal pins for the worktreefs-backed helpers that
// the acceptance/successor read path and every write use.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/worktreefs"
)

// TestReadRepoFile_ConfinedAndCapped covers the acceptance/successor read
// path (setAcceptance, addLinkToArtifact): a leaf symlink is refused (400)
// and a file over the worktree read cap is refused (413).
func TestReadRepoFile_ConfinedAndCapped(t *testing.T) {
	svc := newBareService(t)
	if err := os.WriteFile(filepath.Join(svc.repo, "real.md"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.md", filepath.Join(svc.repo, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.repo, "big.md"), make([]byte, worktreefs.DefaultMaxReadBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if b, err := svc.readRepoFile(ctx, "real.md"); err != nil || string(b) != "real" {
		t.Fatalf("readRepoFile(real.md) = %q, %v", b, err)
	}

	b, err := svc.readRepoFile(ctx, "link.md")
	var se *domain.SpineError
	if b != nil || !errors.As(err, &se) || se.Code != domain.ErrInvalidParams || !errors.Is(err, worktreefs.ErrUnsafePath) {
		t.Fatalf("readRepoFile(link.md) = %q, %v; want ErrInvalidParams/ErrUnsafePath", b, err)
	}

	b, err = svc.readRepoFile(ctx, "big.md")
	if b != nil || !errors.As(err, &se) || se.Code != domain.ErrPayloadTooLarge || !errors.Is(err, worktreefs.ErrTooLarge) {
		t.Fatalf("readRepoFile(big.md) = %d bytes, %v; want ErrPayloadTooLarge", len(b), err)
	}
}

// TestCleanPath_ArtifactsDirTraversal pins the design-check amendment to
// §2: the caller's artifact-relative path is cleaned before
// repoRelativePath's filepath.Join can fold ".." away.
//
// Mutation check: calling worktreefs.Clean only after repoRelativePath
// accepts "a/../../x.md" under artifacts_dir "spine" as "x.md".
func TestCleanPath_ArtifactsDirTraversal(t *testing.T) {
	svc := &Service{repo: t.TempDir(), artifactsDir: "spine"}
	for _, p := range []string{"a/../../x.md", "../x.md", "a/./x.md", ".git/config", "a//x.md"} {
		if rel, err := svc.cleanPath(p); err == nil {
			t.Errorf("cleanPath(%q) accepted as %q", p, rel)
		}
	}
	rel, err := svc.cleanPath("governance/charter.md")
	if err != nil || rel != "spine/governance/charter.md" {
		t.Fatalf("cleanPath = %q, %v", rel, err)
	}
}
