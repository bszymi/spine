package engine

// INIT-023 wave 3 review (TASK-005): two merge-recovery requests for one
// partially-merged run each read the run, chained their ledger on the
// binding they read and replaced the whole commit_meta map from that
// snapshot, so the later write restored the other repository's old
// binding and left the other ledger out of the published ancestry. The
// bound ledger is now built and recorded under a per-run stripe from a
// fresh read of the run.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// copyRunForTest is a deep enough copy of a run: what a database read
// returns, sharing nothing with the stored row.
func copyRunForTest(r *domain.Run) *domain.Run {
	c := *r
	c.CommitMeta = make(map[string]string, len(r.CommitMeta))
	for k, v := range r.CommitMeta {
		c.CommitMeta[k] = v
	}
	c.AffectedRepositories = append([]string(nil), r.AffectedRepositories...)
	return &c
}

// snapshotRunStore serves GetRun copies, as the Postgres store does. When
// stale is set, the next GetRun serves it instead (once): a request that
// read the run before another request's write.
type snapshotRunStore struct {
	*mockRunStore
	mu    sync.Mutex
	stale *domain.Run
}

func (s *snapshotRunStore) GetRun(ctx context.Context, runID string) (*domain.Run, error) {
	s.mu.Lock()
	st := s.stale
	s.stale = nil
	s.mu.Unlock()
	if st != nil {
		return copyRunForTest(st), nil
	}
	r, err := s.mockRunStore.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	return copyRunForTest(r), nil
}

// TestCodeRepoBinding_RecoveryRequestsDoNotLoseEachOthersBinding: the
// second of two overlapping recovery requests read the run before the
// first one's write. Before the fix it chained its ledger on the old
// binding and restored code-a's old bound head, so the operator's rebind
// to the fix was lost and the resumed merge published r1, not the fix.
func TestCodeRepoBinding_RecoveryRequestsDoNotLoseEachOthersBinding(t *testing.T) {
	e := mustCREnv(t, true)
	r1 := partiallyMerged(t, e)
	snap := copyRunForTest(e.runNow()) // request 2 reads the run now
	fix := e.commitCode("fix.go", "package x // operator fix\n")

	res1, err := e.orch.RetryRepositoryMergeAt(e.ctx, e.run.RunID, "code-a", "conflict fixed on the branch", fix)
	if err != nil {
		t.Fatalf("request 1: %v", err)
	}
	liveStore := e.orch.store
	e.orch.store = &snapshotRunStore{mockRunStore: e.store, stale: snap}
	res2, err := e.orch.RetryRepositoryMerge(e.ctx, e.run.RunID, "code-a", "try again")
	e.orch.store = liveStore
	if err != nil {
		t.Fatalf("request 2: %v", err)
	}

	meta := e.runNow().CommitMeta
	if got := meta[RepoBoundSHAKey("code-a")]; got != fix {
		t.Fatalf("code-a binding = %s after both requests, want request 1's rebind %s (r1 %s)", got, fix, r1)
	}
	if got := meta[CommitMetaBoundSHA]; got != res2.LedgerCommitSHA {
		t.Fatalf("primary binding = %s, want request 2's ledger %s", got, res2.LedgerCommitSHA)
	}
	if !crIsAncestor(e.primary, res1.LedgerCommitSHA, res2.LedgerCommitSHA) {
		t.Fatal("request 2's ledger is not built on request 1's: request 1's ledger left the published ancestry")
	}
	e.resume()
	if !e.originHas(fix) {
		t.Fatal("the rebound head did not merge")
	}
	for _, l := range []string{res1.LedgerCommitSHA, res2.LedgerCommitSHA} {
		if !crIsAncestor(e.primary, l, "refs/heads/main") {
			t.Fatalf("ledger %s is not on main", l)
		}
	}
}

// ledgerRaceBinder is a CommitBinder whose CommitFilesOnto makes a fresh
// commit id per call (a child of base) and, when hold is set, holds the
// first call until release is closed. It records whether two calls were
// ever inside at once.
type ledgerRaceBinder struct {
	*fakeBinderGit
	mu      sync.Mutex
	hold    bool
	n       int
	inside  int
	overlap bool
	kids    map[string][]string
	entered chan struct{}
	release chan struct{}
}

func (b *ledgerRaceBinder) CommitFilesOnto(_ context.Context, base string, _ map[string]string, _ git.CommitOpts) (git.CommitResult, error) {
	b.mu.Lock()
	b.n++
	n := b.n
	b.inside++
	if b.inside > 1 {
		b.overlap = true
	}
	sha := sha40("123456789"[n-1])
	b.kids[sha] = []string{base}
	b.mu.Unlock()
	if n == 1 && b.hold {
		close(b.entered)
		<-b.release
	}
	b.mu.Lock()
	b.inside--
	b.mu.Unlock()
	return git.CommitResult{SHA: sha}, nil
}

func (b *ledgerRaceBinder) CommitParents(_ context.Context, sha string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.kids[sha]
	if !ok {
		return nil, errors.New("unknown commit")
	}
	return p, nil
}

// syncRunStore holds one run behind a mutex and serves copies.
type syncRunStore struct {
	stubRunStore
	mu         sync.Mutex
	run        *domain.Run
	metaWrites int
}

func (s *syncRunStore) GetRun(_ context.Context, _ string) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyRunForTest(s.run), nil
}

func (s *syncRunStore) SetCommitMeta(_ context.Context, _ string, meta map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metaWrites++
	s.run.CommitMeta = meta
	return nil
}

const raceRunID = "run-wave3rec0001"

func newLedgerRace(t *testing.T) (*Orchestrator, *syncRunStore, *ledgerRaceBinder, *domain.Run) {
	t.Helper()
	bound, ra, rb := sha40('b'), sha40('c'), sha40('d')
	run := &domain.Run{
		RunID: raceRunID, TraceID: "trace-wave3rec0001", Status: domain.RunStatusPartiallyMerged,
		TaskPath: "tasks/t.md", BranchName: "spine/run/" + raceRunID,
		AffectedRepositories: []string{"spine", "code-a", "code-b"},
		CommitMeta: map[string]string{
			CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", raceRunID+"-review-1"),
			RepoBoundSHAKey("code-a"): ra, RepoBoundSHAKey("code-b"): rb,
		},
	}
	st := &syncRunStore{run: copyRunForTest(run)}
	b := &ledgerRaceBinder{fakeBinderGit: newFakeBinderGit(), kids: map[string][]string{},
		entered: make(chan struct{}), release: make(chan struct{})}
	o := &Orchestrator{store: st, git: b, binder: b, bindingOn: true}
	return o, st, b, run
}

// TestBoundLedgerWritesAreSerialisedPerRun: two overlapping recovery
// requests (a retry with head_sha on code-a, a resolve on code-b) that
// both read the run before either wrote. The second waits for the first,
// chains its ledger on the first's and keeps code-a's rebind.
func TestBoundLedgerWritesAreSerialisedPerRun(t *testing.T) {
	o, st, b, snap := newLedgerRace(t)
	b.hold = true
	fixA := sha40('e')
	ctx := context.Background()
	type result struct {
		sha string
		err error
	}
	r1, r2 := make(chan result, 1), make(chan result, 1)
	go func() {
		sha, err := o.writeBoundLedgerCommit(ctx, copyRunForTest(snap), "code-a",
			mergeRecoveryAction{Operation: "merge_recovery_retry", ActorID: "op-1", Reason: "fixed", ReboundSHA: fixA})
		r1 <- result{sha, err}
	}()
	<-b.entered // request 1 holds the run's stripe, inside its ledger commit
	go func() {
		sha, err := o.writeBoundLedgerCommit(ctx, copyRunForTest(snap), "code-b",
			mergeRecoveryAction{Operation: "merge_recovery_resolve", ActorID: "op-2", Reason: "done upstream"})
		r2 <- result{sha, err}
	}()
	var early *result
	select {
	case got := <-r2:
		early = &got // finished while request 1 was still inside: not serialised
	case <-time.After(300 * time.Millisecond):
	}
	close(b.release)
	got1 := <-r1
	got2 := result{}
	if early != nil {
		got2 = *early
	} else {
		got2 = <-r2
	}
	if got1.err != nil || got2.err != nil {
		t.Fatalf("requests: %v, %v", got1.err, got2.err)
	}
	if early != nil || b.overlap {
		t.Fatal("the second ledger write ran while the first was in progress")
	}
	meta := st.run.CommitMeta
	if meta[CommitMetaBoundSHA] != got2.sha || meta[CommitMetaBoundBy] != boundByRecovery(raceRunID, "merge_recovery_resolve") {
		t.Fatalf("binding = %s by %s, want the second ledger %s", meta[CommitMetaBoundSHA], meta[CommitMetaBoundBy], got2.sha)
	}
	if p := b.kids[got2.sha]; len(p) != 1 || p[0] != got1.sha {
		t.Fatalf("second ledger's parents = %v, want [%s] (the first ledger)", p, got1.sha)
	}
	if meta[RepoBoundSHAKey("code-a")] != fixA {
		t.Fatalf("code-a binding = %s, want the first request's rebind %s", meta[RepoBoundSHAKey("code-a")], fixA)
	}
	if meta[RepoBoundSHAKey("code-b")] != sha40('d') {
		t.Fatalf("code-b binding = %s, want it unchanged", meta[RepoBoundSHAKey("code-b")])
	}
	if st.metaWrites != 2 {
		t.Fatalf("commit_meta writes = %d, want 2", st.metaWrites)
	}
}

// TestBoundLedgerWaiterHonoursItsContext: a request whose context ends
// while another recovery action on the run holds the stripe gets
// ErrUnavailable and writes nothing.
func TestBoundLedgerWaiterHonoursItsContext(t *testing.T) {
	o, st, b, snap := newLedgerRace(t)
	unlock, err := lockRecoveryBinding(context.Background(), raceRunID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = o.writeBoundLedgerCommit(ctx, copyRunForTest(snap), "code-a",
		mergeRecoveryAction{Operation: "merge_recovery_retry", ActorID: "op-1", Reason: "r"})
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrUnavailable || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want ErrUnavailable wrapping context.Canceled", err)
	}
	if b.n != 0 || st.metaWrites != 0 {
		t.Fatalf("a request that gave up wrote: %d ledger commits, %d commit_meta writes", b.n, st.metaWrites)
	}
}

// TestBoundLedgerRefusedWhenTheRunResumedMeanwhile: the fresh read under
// the stripe must still be a partially-merged run (it may have resumed
// while the request waited); otherwise nothing is written.
func TestBoundLedgerRefusedWhenTheRunResumedMeanwhile(t *testing.T) {
	o, st, b, snap := newLedgerRace(t)
	st.run.Status = domain.RunStatusCommitting
	_, err := o.writeBoundLedgerCommit(context.Background(), copyRunForTest(snap), "code-a",
		mergeRecoveryAction{Operation: "merge_recovery_retry", ActorID: "op-1", Reason: "r"})
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrConflict {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if b.n != 0 || st.metaWrites != 0 {
		t.Fatalf("wrote %d ledger commits and %d commit_meta writes for a run no longer partially merged", b.n, st.metaWrites)
	}
}
