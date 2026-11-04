package gitpool_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/testutil"
)

// TestClient_CodeRepoCloneMergesWithNoHostIdentity (INIT-024 TASK-010,
// GitHub CI after the push): a code-repository clone the pool makes with
// the production cloner and client factory has no identity of its own, so
// on a host with none — Spine's standalone image, a GitHub runner — a
// governed merge into it was refused "Committer identity unknown". The
// merge now succeeds, committed by Spine.
func TestClient_CodeRepoCloneMergesWithNoHostIdentity(t *testing.T) {
	testutil.NoGitIdentity(t)
	ctx := context.Background()
	fixture := []string{"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.test",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.test"}
	g := func(dir string, env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	upstream := t.TempDir()
	g(upstream, nil, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(upstream, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g(upstream, nil, "add", "-A")
	g(upstream, fixture, "commit", "-q", "-m", "one")
	g(upstream, nil, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(upstream, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g(upstream, nil, "add", "-A")
	g(upstream, fixture, "commit", "-q", "-m", "two")
	feature := g(upstream, nil, "rev-parse", "feature")
	g(upstream, nil, "checkout", "-q", "main")

	base := t.TempDir()
	repo := &repository.Repository{ID: "code", Status: "active", LocalPath: filepath.Join(base, "code"), CloneURL: upstream}
	resolver := &stubResolver{lookups: map[string]lookupResult{"code": {repo: repo}}}
	pool := newPool(t, &stubClient{}, resolver, gitpool.NewCLIClientFactory(),
		gitpool.WithCloner(gitpool.NewCLICloner()), gitpool.WithRepoBase(base))
	client, err := pool.Client(ctx, "code")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo.LocalPath, "config", "--get", "user.name").CombinedOutput(); err == nil {
		t.Fatalf("the clone has an identity of its own (%s); the test needs one without", out)
	}
	res, err := client.Merge(ctx, git.MergeOpts{Source: feature, Target: "main", Strategy: "merge-commit", Message: "merge feature"})
	if err != nil {
		t.Fatalf("merge into a code-repository clone with no host identity: %v", err)
	}
	if got := g(repo.LocalPath, nil, "log", "-1", "--format=%an <%ae>|%cn <%ce>", res.SHA); got != "Spine <spine@spine.local>|Spine <spine@spine.local>" {
		t.Fatalf("merge commit author|committer = %q, want Spine for both", got)
	}
}
