package gitpool_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/domain"
)

// TestClient_CheckoutBudgetRefusalIsRememberedForATime (INIT-024 TASK-003
// review round 1, P3): a clone the checkout budget refuses is a permanent
// error (413-class, matching git.ErrTreeTooLarge), and is not retried —
// the remote fetched in full again — on the next access. The refusal
// expires after its TTL (a remote fixed upstream is cloned again), and a
// changed budget or another remote is a fresh attempt.
func TestClient_CheckoutBudgetRefusalIsRememberedForATime(t *testing.T) {
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	base := t.TempDir()
	repo := &repository.Repository{ID: "big", Status: "active",
		LocalPath: filepath.Join(base, "big"), CloneURL: "https://git.example/big.git"}
	resolver := &stubResolver{lookups: map[string]lookupResult{"big": {repo: repo}}}
	refusal := &git.GitError{Kind: git.ErrKindPermanent, Op: "clone", Message: git.TreeTooLargeMessage,
		Err: &git.TreeTooLargeError{Limit: git.TreeLimitEntries, Max: 10, Seen: 11}}
	cloner := &fakeCloner{failWith: refusal}
	now := time.Unix(1_000_000, 0)
	pool := newPool(t, &stubClient{}, resolver, func(string, gitpool.Credential) git.GitClient { return &stubClient{} },
		gitpool.WithCloner(cloner), gitpool.WithRepoBase(base), gitpool.WithClock(func() time.Time { return now }))

	for i := 0; i < 3; i++ {
		_, err := pool.Client(context.Background(), "big")
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrPayloadTooLarge || !errors.Is(err, git.ErrTreeTooLarge) {
			t.Fatalf("access %d: err = %v, want a permanent payload-too-large refusal", i, err)
		}
	}
	if n := cloner.callCount(); n != 1 {
		t.Fatalf("clone attempts = %d, want 1 (the refusal is remembered)", n)
	}

	now = now.Add(11 * time.Minute)
	_, _ = pool.Client(context.Background(), "big")
	if n := cloner.callCount(); n != 2 {
		t.Fatalf("after the TTL: clone attempts = %d, want 2", n)
	}

	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{MaxEntries: 1 << 20}})
	_, _ = pool.Client(context.Background(), "big")
	if n := cloner.callCount(); n != 3 {
		t.Fatalf("after a budget change: clone attempts = %d, want 3", n)
	}

	repo.CloneURL = "https://git.example/fixed.git"
	cloner.failWith = nil
	if _, err := pool.Client(context.Background(), "big"); err != nil {
		t.Fatalf("another remote: %v", err)
	}
	if n := cloner.callCount(); n != 4 {
		t.Fatalf("another remote: clone attempts = %d, want 4", n)
	}
}

// TestClient_ReusedCloneGetsTheNeutralAttributes (INIT-024 TASK-003
// review round 2): in a process with a checkout budget (`spine serve`
// installs it before any pool is used) an existing clone is given the
// neutral checkout attributes on first access — the budget's measure
// refuses without them; without a budget it is left alone.
func TestClient_ReusedCloneGetsTheNeutralAttributes(t *testing.T) {
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	access := func(t *testing.T) string {
		base := t.TempDir()
		local := filepath.Join(base, "code")
		if out, err := exec.Command("git", "init", "-q", local).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		repo := &repository.Repository{ID: "code", Status: "active", LocalPath: local, CloneURL: "https://git.example/code.git"}
		resolver := &stubResolver{lookups: map[string]lookupResult{"code": {repo: repo}}}
		pool := newPool(t, &stubClient{}, resolver, func(string, gitpool.Credential) git.GitClient { return &stubClient{} },
			gitpool.WithCloner(&fakeCloner{}), gitpool.WithRepoBase(base))
		if _, err := pool.Client(context.Background(), "code"); err != nil {
			t.Fatal(err)
		}
		return local
	}
	if ok, _ := git.HasNeutralCheckoutAttributes(context.Background(), access(t)); ok {
		t.Fatal("written without a budget")
	}
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{}})
	if ok, err := git.HasNeutralCheckoutAttributes(context.Background(), access(t)); !ok || err != nil {
		t.Fatalf("a reused clone in a budgeted process lacks the line: %v %v", ok, err)
	}
}
