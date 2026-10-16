package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/domain"
)

// INIT-023 TASK-012 review round 2, finding 4: a failed attempt carries an
// earlier attempt's landed merge (publication pending, never abandoned)
// only within the run's current committing episode.

// stepsDownStore fails ListStepExecutionsByRun while down is set.
type stepsDownStore struct {
	*mockRunStore
	down bool
}

func (s *stepsDownStore) ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error) {
	if s.down {
		return nil, errors.New("store unavailable")
	}
	return s.mockRunStore.ListStepExecutionsByRun(ctx, runID)
}

func TestKeepPendingPublication_OnlyWithinTheEpisode(t *testing.T) {
	ctx := context.Background()
	const runID = "run-episodes"
	sha := strings.Repeat("a", 40)
	t0 := time.Now().Add(-time.Hour)
	t1, t2 := t0.Add(time.Minute), t0.Add(2*time.Minute)
	pushFailed := func(at time.Time, class domain.MergeFailureClass) *domain.RepositoryMergeOutcome {
		return &domain.RepositoryMergeOutcome{RunID: runID, RepositoryID: domain.PrimaryRepositoryID, Status: domain.RepositoryMergeStatusFailed,
			FailureClass: class, Attempts: 1, LastAttemptedAt: &at, FailureDetail: domain.PushFailureDetail(sha, "git push: x")}
	}
	merged := func(at time.Time) *domain.RepositoryMergeOutcome {
		return &domain.RepositoryMergeOutcome{RunID: runID, RepositoryID: domain.PrimaryRepositoryID, Status: domain.RepositoryMergeStatusMerged,
			MergeCommitSHA: sha, Attempts: 1, LastAttemptedAt: &at, MergedAt: &at}
	}
	later := func() domain.RepositoryMergeOutcome {
		return domain.RepositoryMergeOutcome{RunID: runID, RepositoryID: domain.PrimaryRepositoryID, Status: domain.RepositoryMergeStatusFailed,
			FailureClass: domain.MergeFailureRemoteUnavailable, Attempts: 2, FailureDetail: "git merge: repository locked"}
	}
	for _, tc := range []struct {
		name    string
		prior   *domain.RepositoryMergeOutcome
		stepAt  time.Time
		carried bool
	}{
		{"transient push failure of this episode", pushFailed(t1, domain.MergeFailureNetwork), t0, true},
		{"merged in this episode (completion did not follow)", merged(t1), t0, true},
		{"transient push failure of an earlier episode", pushFailed(t1, domain.MergeFailureNetwork), t2, false},
		{"merged in an earlier episode", merged(t1), t2, false},
		{"permanent push rejection, this episode", pushFailed(t1, domain.MergeFailureBranchProtection), t0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &mockRunStore{runs: map[string]*domain.Run{runID: {RunID: runID, Status: domain.RunStatusCommitting}}}
			st.createdSteps = append(st.createdSteps, &domain.StepExecution{ExecutionID: runID + "-publish-1", RunID: runID,
				StepID: "publish", Status: domain.StepStatusInProgress, CreatedAt: tc.stepAt, StartedAt: &tc.stepAt})
			o := &Orchestrator{store: st}
			out := later()
			if err := o.keepPendingPublication(ctx, st.runs[runID], &out, tc.prior); err != nil {
				t.Fatalf("keepPendingPublication: %v", err)
			}
			if out.MergedLocally() != tc.carried {
				t.Fatalf("carried = %v (%q), want %v", out.MergedLocally(), out.FailureDetail, tc.carried)
			}
			if tc.carried && out.LocalMergeSHA() != sha {
				t.Fatalf("carried SHA = %q, want %s", out.LocalMergeSHA(), sha)
			}
		})
	}

	t.Run("unreadable steps: nothing decided, error returned", func(t *testing.T) {
		st := &stepsDownStore{mockRunStore: &mockRunStore{runs: map[string]*domain.Run{runID: {RunID: runID}}}, down: true}
		o := &Orchestrator{store: st}
		out := later()
		before := out.FailureDetail
		if err := o.keepPendingPublication(ctx, st.runs[runID], &out, pushFailed(t1, domain.MergeFailureNetwork)); err == nil {
			t.Fatal("want the step-read error")
		}
		if out.FailureDetail != before {
			t.Fatalf("detail changed to %q on a failed read", out.FailureDetail)
		}
	})
	t.Run("no carry possible: no step read", func(t *testing.T) {
		st := &stepsDownStore{mockRunStore: &mockRunStore{runs: map[string]*domain.Run{runID: {RunID: runID}}}, down: true}
		o := &Orchestrator{store: st}
		for _, prior := range []*domain.RepositoryMergeOutcome{nil, pushFailed(t1, domain.MergeFailureAuth),
			{Status: domain.RepositoryMergeStatusFailed, FailureClass: domain.MergeFailureNetwork, FailureDetail: "git merge: x"}} {
			out := later()
			if err := o.keepPendingPublication(ctx, st.runs[runID], &out, prior); err != nil {
				t.Fatalf("prior %+v: the steps were read (%v)", prior, err)
			}
		}
	})
}

// The code-repository loop: a transient push failure keeps the
// repository's merge pending within the episode; after step activity
// begins a new episode, its pre-push failures are bounded and abandoned
// (marked, for the partial-merge lifecycle) once the budget is spent.
func TestKeepPendingPublication_CodeRepositoryEpisodes(t *testing.T) {
	const runID = "run-carry-code-repo"
	setup := setupMergeOrderTest(t, runID, "spine/run/"+runID, []string{repository.PrimaryRepositoryID, "api-gateway"})
	ctx := context.Background()
	api := setup.repos["api-gateway"]
	api.pushErr = &git.GitError{Kind: git.ErrKindTransient, Op: "push", Message: "network error"}
	if err := setup.orch.MergeRunBranch(ctx, runID); err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	if o := setup.outcomeForRepo(t, runID, "api-gateway"); !o.MergedLocally() {
		t.Fatalf("attempt 1 = %q, want merged locally", o.FailureDetail)
	}
	api.pushErr = nil
	api.mergeErr = &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "repository locked"}
	if err := setup.orch.MergeRunBranch(ctx, runID); err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if o := setup.outcomeForRepo(t, runID, "api-gateway"); o.Attempts != 2 || !o.MergedLocally() {
		t.Fatalf("attempt 2 in the same episode = %d %q, want the landed merge carried", o.Attempts, o.FailureDetail)
	}

	// Step activity begins the next committing episode.
	stepAt := time.Now()
	setup.store.createdSteps = append(setup.store.createdSteps, &domain.StepExecution{ExecutionID: runID + "-publish-2", RunID: runID,
		StepID: "publish", Status: domain.StepStatusInProgress, CreatedAt: stepAt, StartedAt: &stepAt})
	time.Sleep(time.Millisecond)
	for attempt := 3; attempt <= 4; attempt++ {
		if err := setup.orch.MergeRunBranch(ctx, runID); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if o := setup.outcomeForRepo(t, runID, "api-gateway"); o.Attempts != attempt || o.MergedLocally() {
			t.Fatalf("attempt %d in the next episode = %d %q; want it not to inherit the earlier episode's merge", attempt, o.Attempts, o.FailureDetail)
		}
	}
	if ok, err := setup.orch.AbandonExhaustedMerge(ctx, runID, 3); ok || err != nil {
		t.Fatalf("AbandonExhaustedMerge = %v, %v; want false (a code repository is handed to the partial-merge lifecycle)", ok, err)
	}
	if o := setup.outcomeForRepo(t, runID, "api-gateway"); !strings.HasPrefix(o.FailureDetail, retriesExhaustedMarker) {
		t.Fatalf("outcome after the budget = %q, want it marked exhausted", o.FailureDetail)
	}
}

// The binding re-merge path: a code repository recorded merged whose
// bound commit is no longer on its default branch is merged again, and a
// failure of that re-merge carries nothing — the earlier merge is not on
// the target — so it is bounded like any other.
func TestKeepPendingPublication_CodeRepositoryReMergeIsNotCarried(t *testing.T) {
	ctx := context.Background()
	repoBound, head := sha40('a'), sha40('d')
	f := committingFix(t, false, true)
	f.run.AffectedRepositories = []string{"spine", "code-a"}
	f.run.CommitMeta = map[string]string{
		CommitMetaBoundSHA:        sha40('e'),
		CommitMetaBoundBy:         boundByStep("review", bindRunID+"-review-1"),
		RepoBoundSHAKey("code-a"): repoBound,
	}
	code := newFakeBinderGit()
	code.setHead("main", head)
	code.notAncestor[repoBound+">"+head] = true
	code.mergeErr = &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "repository locked"}
	f.orch.WithRepositoryResolver(newRepoResolver(map[string]repoLookup{"code-a": activeRepoLookup("code-a", "main")}))
	f.orch.WithRepositoryGitClients(realRepoClients{"code-a": fakeCodeBinder{code}})
	at := time.Now()
	if err := f.store.UpsertRepositoryMergeOutcome(ctx, &domain.RepositoryMergeOutcome{RunID: f.run.RunID, RepositoryID: "code-a",
		Status: domain.RepositoryMergeStatusMerged, MergeCommitSHA: sha40('c'), SourceBranch: f.run.BranchName, TargetBranch: "main",
		Attempts: 1, LastAttemptedAt: &at, MergedAt: &at}); err != nil {
		t.Fatal(err)
	}
	if err := f.orch.mergeAffectedCodeRepositories(ctx, f.run); !errors.Is(err, errPendingCodeRepoRetry) {
		t.Fatalf("mergeAffectedCodeRepositories = %v, want the pending retry", err)
	}
	o, err := f.store.GetRepositoryMergeOutcome(ctx, f.run.RunID, "code-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(code.merges) != 1 {
		t.Fatalf("MergeBound calls = %d, want the re-merge", len(code.merges))
	}
	if o.Status != domain.RepositoryMergeStatusFailed || o.MergedLocally() || o.Attempts != 2 {
		t.Fatalf("re-merge failure = %s %d %q; want a plain transient failure, not carried as pending publication", o.Status, o.Attempts, o.FailureDetail)
	}
}
