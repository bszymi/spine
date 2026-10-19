package git_test

// INIT-023 wave 4 integration (codex round 2): TASK-006's MergeBases is a
// read method, so on a home-branch tree (TASK-012 §2.9) "HEAD" means the
// home branch there too, whatever a sequence has checked out — the same
// answer MergeBase gives. A tree with no home branch keeps the real HEAD.

import (
	"context"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

func TestWave4_MergeBasesResolvesHeadToTheHomeBranch(t *testing.T) {
	ctx := context.Background()
	repo := swFeatureRepo(t)
	mainOID := swGit(t, repo, "rev-parse", "main")
	shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	// A sequence has the run branch checked out.
	swGit(t, repo, "checkout", "-q", "feature")

	bases, err := shared.MergeBases(ctx, "HEAD", "feature")
	if err != nil || len(bases) != 1 || bases[0] != mainOID {
		t.Fatalf("MergeBases(HEAD, feature) = %v, %v; want [%s] (HEAD is main)", bases, err, mainOID)
	}
	if mb, err := shared.MergeBase(ctx, "HEAD", "feature"); err != nil || mb != bases[0] {
		t.Fatalf("MergeBase(HEAD, feature) = %s, %v; MergeBases said %v", mb, err, bases)
	}
	if bases, err := shared.MergeBases(ctx, "feature", "HEAD"); err != nil || len(bases) != 1 || bases[0] != mainOID {
		t.Fatalf("MergeBases(feature, HEAD) = %v, %v; want [%s]", bases, err, mainOID)
	}

	other := swFeatureRepo(t)
	swGit(t, other, "checkout", "-q", "feature")
	otherFeature := swGit(t, other, "rev-parse", "feature")
	if bases, err := git.NewCLIClient(other).MergeBases(ctx, "HEAD", "feature"); err != nil || len(bases) != 1 || bases[0] != otherFeature {
		t.Fatalf("no-home MergeBases(HEAD, feature) = %v, %v; want the real HEAD %s", bases, err, otherFeature)
	}
}
