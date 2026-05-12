package harness

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/repository"
)

// stubGitClient records every call routed through it so the wrapper's
// passthrough and override behavior can be verified without standing up
// a real working tree. Only the methods touched by codeRepoBehavior's
// override paths (Merge, Push) need call-tracking; the rest are
// no-args stubs returning zero values, sufficient to satisfy the
// interface compile-time.
type stubGitClient struct {
	mergeCalls []git.MergeOpts
	pushCalls  int

	mergeResult git.MergeResult
	mergeErr    error
	pushErr     error
}

func (s *stubGitClient) Clone(_ context.Context, _, _ string) error { return nil }
func (s *stubGitClient) Commit(_ context.Context, _ git.CommitOpts) (git.CommitResult, error) {
	return git.CommitResult{}, nil
}
func (s *stubGitClient) Merge(_ context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	s.mergeCalls = append(s.mergeCalls, opts)
	if s.mergeErr != nil {
		return git.MergeResult{}, s.mergeErr
	}
	return s.mergeResult, nil
}
func (s *stubGitClient) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (s *stubGitClient) DeleteBranch(_ context.Context, _ string) error    { return nil }
func (s *stubGitClient) Diff(_ context.Context, _, _ string) ([]git.FileDiff, error) {
	return nil, nil
}
func (s *stubGitClient) MergeBase(_ context.Context, _, _ string) (string, error) { return "", nil }
func (s *stubGitClient) Log(_ context.Context, _ git.LogOpts) ([]git.CommitInfo, error) {
	return nil, nil
}
func (s *stubGitClient) ReadFile(_ context.Context, _, _ string) ([]byte, error)    { return nil, nil }
func (s *stubGitClient) ListFiles(_ context.Context, _, _ string) ([]string, error) { return nil, nil }
func (s *stubGitClient) Head(_ context.Context) (string, error)                     { return "", nil }
func (s *stubGitClient) RefSHA(_ context.Context, _ string) (string, error)         { return "", nil }
func (s *stubGitClient) Push(_ context.Context, _, _ string) error {
	s.pushCalls++
	return s.pushErr
}
func (s *stubGitClient) PushBranch(_ context.Context, _, _ string) error          { return nil }
func (s *stubGitClient) DeleteRemoteBranch(_ context.Context, _, _ string) error { return nil }

// TestCodeRepoBehaviorMergeOverride pins the single-shot semantics of
// the merge-failure queue: the queued error fires once on the next
// non-abort merge, and subsequent merges fall through to the
// underlying client. Any drift here would silently turn TASK-006's
// retry assertion into a tautology (the second merge would also fail).
func TestCodeRepoBehaviorMergeOverride(t *testing.T) {
	stub := &stubGitClient{mergeResult: git.MergeResult{SHA: "passthrough"}}
	b := &codeRepoBehavior{underlying: stub}

	want := errors.New("queued merge failure")
	b.queueMergeFailure(want)

	res, err := b.Merge(context.Background(), git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit"})
	if !errors.Is(err, want) {
		t.Fatalf("first Merge: err = %v, want %v", err, want)
	}
	if res.SHA != "" {
		t.Fatalf("first Merge: res.SHA = %q, want empty (override path returns zero result)", res.SHA)
	}
	if got := len(stub.mergeCalls); got != 0 {
		t.Fatalf("first Merge: underlying called %d times, want 0 (override consumed)", got)
	}

	// Second merge — queue is empty, fall through to underlying.
	res, err = b.Merge(context.Background(), git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit"})
	if err != nil {
		t.Fatalf("second Merge: err = %v, want nil (passthrough)", err)
	}
	if res.SHA != "passthrough" {
		t.Fatalf("second Merge: res.SHA = %q, want %q", res.SHA, "passthrough")
	}
	if got := len(stub.mergeCalls); got != 1 {
		t.Fatalf("second Merge: underlying called %d times, want 1", got)
	}
}

// TestCodeRepoBehaviorMergeAbortPassesThrough pins that
// MergeOpts.Strategy == "abort" — the cleanup call
// engine.attemptCodeRepositoryMerge issues after a failed merge —
// always reaches the underlying client even when a failure is queued.
// Without this carve-out a queued failure would itself abort the
// abort path, leaving MERGE_HEAD in place and breaking the next merge
// against the same gitpool entry.
func TestCodeRepoBehaviorMergeAbortPassesThrough(t *testing.T) {
	stub := &stubGitClient{}
	b := &codeRepoBehavior{underlying: stub}

	b.queueMergeFailure(errors.New("would-be-injected"))

	if _, err := b.Merge(context.Background(), git.MergeOpts{Source: "--abort", Strategy: "abort"}); err != nil {
		t.Fatalf("abort merge: err = %v, want nil (passthrough)", err)
	}
	if got := len(stub.mergeCalls); got != 1 {
		t.Fatalf("abort merge: underlying called %d times, want 1", got)
	}
	if stub.mergeCalls[0].Strategy != "abort" {
		t.Fatalf("abort merge: strategy = %q, want %q", stub.mergeCalls[0].Strategy, "abort")
	}
	// The queued failure must remain — abort did not consume it. The
	// next non-abort merge should still fire the queued error.
	_, err := b.Merge(context.Background(), git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit"})
	if err == nil {
		t.Fatalf("post-abort non-abort merge: expected queued failure to fire, got nil")
	}
}

// TestCodeRepoBehaviorPushOverride mirrors the merge-override pin for
// the push path so TASK-006's "merge succeeded but push failed"
// scenario can be driven.
func TestCodeRepoBehaviorPushOverride(t *testing.T) {
	stub := &stubGitClient{}
	b := &codeRepoBehavior{underlying: stub}

	want := errors.New("queued push failure")
	b.queuePushFailure(want)

	if err := b.Push(context.Background(), "origin", "main"); !errors.Is(err, want) {
		t.Fatalf("first Push: err = %v, want %v", err, want)
	}
	if stub.pushCalls != 0 {
		t.Fatalf("first Push: underlying called %d times, want 0 (override consumed)", stub.pushCalls)
	}

	if err := b.Push(context.Background(), "origin", "main"); err != nil {
		t.Fatalf("second Push: err = %v, want nil (passthrough)", err)
	}
	if stub.pushCalls != 1 {
		t.Fatalf("second Push: underlying called %d times, want 1", stub.pushCalls)
	}
}

// TestNewCodeRepoDirRenamesNonMainDefault pins that asking for a
// non-main default branch lands a working tree whose current branch
// matches the spec — testutil.NewTempRepo always initialises with
// `main`, so a spec like `DefaultBranch: "trunk"` is realised by
// renaming the initial branch in place. Without this rename the
// orchestrator's RefSHA(repo.DefaultBranch) lookup at run-start would
// fail and the partial-merge scenarios would never reach the failure
// modes they are trying to assert.
func TestNewCodeRepoDirRenamesNonMainDefault(t *testing.T) {
	dir := newCodeRepoDir(t, "trunk")

	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "trunk" {
		t.Errorf("HEAD branch = %q, want %q", got, "trunk")
	}

	// And the verify-quiet check on the renamed ref must pass — a fresh
	// `git branch -m` in the tempdir leaves the ref pointing at the
	// initial empty commit so RefSHA's downstream callers can resolve
	// it.
	verify := exec.Command("git", "-C", dir, "rev-parse", "--verify", "refs/heads/trunk")
	if out, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("rev-parse refs/heads/trunk: %v\n%s", err, out)
	}
	// The pre-rename "main" ref must no longer resolve — otherwise a
	// caller that incorrectly hard-coded "main" would silently succeed
	// against the wrong branch.
	stale := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", "refs/heads/main")
	if err := stale.Run(); err == nil {
		t.Errorf("post-rename: refs/heads/main still resolves; rename should remove it")
	}
}

// TestNewCodeRepoDirKeepsMainDefault is the symmetric case: an empty
// or explicit "main" spec leaves the testutil.NewTempRepo default
// alone (no rename needed and no surprise side effects).
func TestNewCodeRepoDirKeepsMainDefault(t *testing.T) {
	for _, branch := range []string{"", "main"} {
		dir := newCodeRepoDir(t, branch)
		cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("branch=%q: rev-parse HEAD: %v\n%s", branch, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != "main" {
			t.Errorf("branch=%q: HEAD = %q, want %q", branch, got, "main")
		}
	}
}

// TestCodeRepoBehaviorClearsWithNilError confirms that calling
// queueMergeFailure(nil) clears any previously-queued failure rather
// than installing a no-op override. Tests reset state between
// iterations through this path.
func TestCodeRepoBehaviorClearsWithNilError(t *testing.T) {
	stub := &stubGitClient{}
	b := &codeRepoBehavior{underlying: stub}

	b.queueMergeFailure(errors.New("staged"))
	b.queueMergeFailure(nil) // operator wipes the queue

	if _, err := b.Merge(context.Background(), git.MergeOpts{Source: "x", Target: "y", Strategy: "merge-commit"}); err != nil {
		t.Fatalf("Merge after clear: err = %v, want nil (queue should be empty)", err)
	}
	if got := len(stub.mergeCalls); got != 1 {
		t.Fatalf("Merge after clear: underlying called %d times, want 1", got)
	}
}

// TestFixedRepoResolverReturnsPrimary pins the post-TASK-031 invariant
// that the resolver installed by WithCodeRepos answers
// Lookup(PrimaryRepositoryID) with the primary record rather than
// ErrRepositoryNotFound. Without this, a task that explicitly declares
// `repositories: [spine, ...]` fails at engine.checkRepositoryPreconditions
// even though the primary working tree is always available via the
// scenario harness. The test exercises the resolver directly because
// the engine path is already covered by
// engine.TestRepoPrecondition_AllActiveSucceeds — what's specific to
// this helper is that the primary record is constructed from the
// harness's TestRepo (not the task frontmatter) and held alongside the
// code-repo map so it survives even when zero code specs are passed.
func TestFixedRepoResolverReturnsPrimary(t *testing.T) {
	primary := &repository.Repository{
		ID:            repository.PrimaryRepositoryID,
		Kind:          repository.KindSpine,
		DefaultBranch: "main",
		LocalPath:     "/tmp/primary-fixture",
	}
	billing := &repository.Repository{
		ID:            "billing",
		Kind:          repository.KindCode,
		Status:        "active",
		DefaultBranch: "main",
		LocalPath:     "/tmp/billing-fixture",
	}
	r := &fixedRepoResolver{
		primary: primary,
		repos:   map[string]*repository.Repository{"billing": billing},
	}

	got, err := r.Lookup(context.Background(), repository.PrimaryRepositoryID)
	if err != nil {
		t.Fatalf("Lookup(spine): err = %v, want nil", err)
	}
	if got != primary {
		t.Fatalf("Lookup(spine): returned %p, want %p (primary record installed at construction)", got, primary)
	}

	got, err = r.Lookup(context.Background(), "billing")
	if err != nil {
		t.Fatalf("Lookup(billing): err = %v, want nil", err)
	}
	if got != billing {
		t.Fatalf("Lookup(billing): returned %p, want %p (code-repo map miss)", got, billing)
	}

	if _, err := r.Lookup(context.Background(), "ghost"); !errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Fatalf("Lookup(ghost): err = %v, want ErrRepositoryNotFound", err)
	}
}

// TestFixedRepoGitClientsReturnsPrimary mirrors the resolver pin for
// the git-clients view: Client(PrimaryRepositoryID) returns the
// harness's primary git.GitClient rather than ErrRepositoryNotFound.
// The engine currently routes primary branch operations through
// Orchestrator.git directly, so the production hot path does not
// consult this method for the primary — the wiring is symmetric
// defence in depth so a future engine path that does consult it cannot
// silently degrade.
func TestFixedRepoGitClientsReturnsPrimary(t *testing.T) {
	primaryClient := &stubGitClient{}
	billingClient := &stubGitClient{}
	c := &fixedRepoGitClients{
		primary: primaryClient,
		clients: map[string]git.GitClient{"billing": billingClient},
	}

	got, err := c.Client(context.Background(), repository.PrimaryRepositoryID)
	if err != nil {
		t.Fatalf("Client(spine): err = %v, want nil", err)
	}
	if got != primaryClient {
		t.Fatalf("Client(spine): returned %p, want %p", got, primaryClient)
	}

	got, err = c.Client(context.Background(), "billing")
	if err != nil {
		t.Fatalf("Client(billing): err = %v, want nil", err)
	}
	if got != billingClient {
		t.Fatalf("Client(billing): returned %p, want %p", got, billingClient)
	}

	if _, err := c.Client(context.Background(), "ghost"); !errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Fatalf("Client(ghost): err = %v, want ErrRepositoryNotFound", err)
	}
}
