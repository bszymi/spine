package engine

// Fixtures for the INIT-023 TASK-005 merge-binding unit tests: a fake
// GitOperator that is also a CommitBinder (recording every call), a
// SetCommitMeta-recording store, and workflows shaped like the shipped
// ones.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
)

// sha40 returns a 40-digit lowercase id made of one repeated hex digit.
func sha40(c byte) string { return strings.Repeat(string(c), 40) }

const bindRunID = "run-abc12345"

// fakeBinderGit is a GitOperator + CommitBinder whose answers the test
// programs. It records every call by name ("binder:" prefix for the
// CommitBinder methods).
type fakeBinderGit struct {
	stubGitOperator

	mu      sync.Mutex
	calls   []string
	heads   map[string]string // branch → head
	headErr error

	verifyErr   error
	notAncestor map[string]bool // "anc>desc" → not an ancestor
	parents     map[string][]string

	mergeRes  git.MergeResult
	mergeErr  error
	merges    []git.MergeOpts // MergeBound calls
	pushErr   error
	pushes    []string // "remote branch sha"
	ontoSHA   string
	ontoErr   error
	ontoCalls []map[string]string

	plainMerges []git.MergeOpts
	plainPushes []string
	files       map[string]string // "ref:path" → content for ReadFile
}

func newFakeBinderGit() *fakeBinderGit {
	return &fakeBinderGit{heads: map[string]string{}, notAncestor: map[string]bool{}, parents: map[string][]string{}, files: map[string]string{}}
}

func (f *fakeBinderGit) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeBinderGit) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeBinderGit) setHead(branch, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[branch] = sha
}

func (f *fakeBinderGit) ResolveBranchHead(_ context.Context, branch string) (string, error) {
	f.record("binder:ResolveBranchHead " + branch)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.headErr != nil {
		return "", f.headErr
	}
	h, ok := f.heads[branch]
	if !ok {
		return "", &git.GitError{Kind: git.ErrKindNotFound, Op: "resolve_branch_head", Message: "no branch " + branch}
	}
	return h, nil
}

func (f *fakeBinderGit) VerifyCommit(_ context.Context, sha string) error {
	f.record("binder:VerifyCommit " + sha)
	return f.verifyErr
}

func (f *fakeBinderGit) IsAncestor(_ context.Context, a, d string) (bool, error) {
	f.record("binder:IsAncestor " + a + ">" + d)
	if a == d {
		return true, nil
	}
	return !f.notAncestor[a+">"+d], nil
}

func (f *fakeBinderGit) CommitParents(_ context.Context, sha string) ([]string, error) {
	f.record("binder:CommitParents " + sha)
	p, ok := f.parents[sha]
	if !ok {
		return nil, errors.New("unknown commit")
	}
	return p, nil
}

func (f *fakeBinderGit) CommitFilesOnto(_ context.Context, base string, files map[string]string, _ git.CommitOpts) (git.CommitResult, error) {
	f.record("binder:CommitFilesOnto " + base)
	f.ontoCalls = append(f.ontoCalls, files)
	if f.ontoErr != nil {
		return git.CommitResult{}, f.ontoErr
	}
	return git.CommitResult{SHA: f.ontoSHA}, nil
}

func (f *fakeBinderGit) MergeBound(_ context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	f.record("binder:MergeBound " + opts.SourceSHA)
	f.merges = append(f.merges, opts)
	return f.mergeRes, f.mergeErr
}

func (f *fakeBinderGit) PushCommit(_ context.Context, remote, branch, sha string) error {
	f.record("binder:PushCommit " + remote + " " + branch + " " + sha)
	f.pushes = append(f.pushes, remote+" "+branch+" "+sha)
	return f.pushErr
}

func (f *fakeBinderGit) Merge(_ context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	f.record("Merge " + opts.Source)
	f.plainMerges = append(f.plainMerges, opts)
	return git.MergeResult{SHA: sha40('e')}, nil
}

func (f *fakeBinderGit) Push(_ context.Context, remote, ref string) error {
	f.record("Push " + remote + " " + ref)
	f.plainPushes = append(f.plainPushes, remote+" "+ref)
	return nil
}

func (f *fakeBinderGit) Checkout(_ context.Context, b string) error {
	f.record("Checkout " + b)
	return nil
}

func (f *fakeBinderGit) WriteAndStageFile(_ context.Context, p, _ string) error {
	f.record("WriteAndStageFile " + p)
	return nil
}

func (f *fakeBinderGit) Commit(_ context.Context, _ git.CommitOpts) (git.CommitResult, error) {
	f.record("Commit")
	return git.CommitResult{SHA: sha40('d')}, nil
}

func (f *fakeBinderGit) ReadFile(_ context.Context, ref, p string) ([]byte, error) {
	f.record("ReadFile " + ref + ":" + p)
	if c, ok := f.files[ref+":"+p]; ok {
		return []byte(c), nil
	}
	return nil, &git.GitError{Kind: git.ErrKindNotFound, Op: "read_file", Message: "missing"}
}

func (f *fakeBinderGit) MergeBase(_ context.Context, a, b string) (string, error) {
	f.record("MergeBase " + a + " " + b)
	return sha40('0'), nil
}

func (f *fakeBinderGit) Diff(_ context.Context, a, b string) ([]git.FileDiff, error) {
	f.record("Diff " + a + " " + b)
	return nil, nil
}

func (f *fakeBinderGit) DeleteBranch(_ context.Context, name string) error {
	f.record("DeleteBranch " + name)
	return nil
}

// plainGit is a GitOperator with no CommitBinder capability.
type plainGit struct{ stubGitOperator }

// metaRecordingStore records every SetCommitMeta payload.
type metaRecordingStore struct {
	*mockRunStore
	metaWrites []map[string]string
}

func (s *metaRecordingStore) SetCommitMeta(ctx context.Context, runID string, meta map[string]string) error {
	s.metaWrites = append(s.metaWrites, meta)
	return s.mockRunStore.SetCommitMeta(ctx, runID, meta)
}

// publishWorkflow mirrors task-default: execute → validate → review →
// publish (internal merge handler).
func publishWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "task-default", Path: "workflows/task-default.yaml", EntryStep: "execute",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "Done", NextStep: "validate"}}},
			{ID: "validate", Name: "Validate", Type: domain.StepTypeAutomated,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "passed", Name: "Passed", NextStep: "review"},
					{ID: "failed", Name: "Failed", NextStep: "execute"},
				}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted", NextStep: "publish"},
					{ID: "needs_rework", Name: "Rework", NextStep: "execute"},
				}},
			{ID: "publish", Name: "Publish", Type: domain.StepTypeInternal,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly, Handler: "merge"},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "published", Name: "Published", NextStep: "end", Commit: map[string]string{"status": "Completed"}},
					{ID: "merge_failed", Name: "Merge failed", NextStep: "execute"},
				}},
		},
	}
}

// legacyWorkflow commits on the review's terminal outcome (no publish
// step), like artifact-creation and adr.
func legacyWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "legacy", Path: "workflows/legacy.yaml", EntryStep: "execute",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "Done", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted", NextStep: "end", Commit: map[string]string{"status": "Completed"}},
					{ID: "needs_rework", Name: "Rework", NextStep: "execute"},
				}},
		},
	}
}

// testBindingWorkflow covers every step type and outcome kind for the
// classification tables.
func testBindingWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "wf-bind", Path: "workflows/bind.yaml", EntryStep: "execute",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "done", Name: "Done", NextStep: "review"},
					{ID: "again", Name: "Again", NextStep: "validate"},
					{ID: "stop", Name: "Stop", NextStep: "end"},
				}},
			{ID: "validate", Name: "Validate", Type: domain.StepTypeAutomated,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "passed", Name: "Passed", NextStep: "review"},
					{ID: "failed", Name: "Failed", NextStep: "execute"},
					{ID: "stop", Name: "Stop", NextStep: "end"},
				}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted", NextStep: "publish"},
					{ID: "rework", Name: "Rework", NextStep: "execute"},
					{ID: "reject", Name: "Reject", NextStep: "end"},
					{ID: "approve_end", Name: "Approve", NextStep: "end", Commit: map[string]string{"status": "Completed"}},
					{ID: "to_unknown", Name: "Unknown", NextStep: "ghost"},
				}},
			{ID: "converge", Name: "Converge", Type: domain.StepTypeConvergence,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "ok", Name: "OK", NextStep: "review"},
					{ID: "rework", Name: "Rework", NextStep: "execute"},
					{ID: "reject", Name: "Reject", NextStep: "end"},
				}},
			{ID: "publish", Name: "Publish", Type: domain.StepTypeInternal,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly, Handler: "merge"},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "published", Name: "Published", NextStep: "end", Commit: map[string]string{"status": "Completed"}},
					{ID: "merge_failed", Name: "Merge failed", NextStep: "execute"},
				}},
		},
	}
}

// bindFix is an orchestrator over a fake binder and an in-memory store,
// with one active run on spine/run/run-abc12345.
type bindFix struct {
	t     *testing.T
	store *metaRecordingStore
	git   *fakeBinderGit
	orch  *Orchestrator
	wf    *domain.WorkflowDefinition
	run   *domain.Run
}

func newBindFix(t *testing.T, wf *domain.WorkflowDefinition, bindingOn bool, policy MovedHeadPolicy) *bindFix {
	t.Helper()
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	run := &domain.Run{
		RunID: bindRunID, TraceID: "trace-0123456789abcdef", Status: domain.RunStatusActive,
		TaskPath: "tasks/t.md", WorkflowPath: wf.Path, WorkflowID: wf.ID, WorkflowVersion: "v1",
		BranchName: "spine/run/" + bindRunID, CurrentStepID: wf.EntryStep,
	}
	store := &metaRecordingStore{mockRunStore: &mockRunStore{runs: map[string]*domain.Run{run.RunID: run}}}
	g := newFakeBinderGit()
	orch := &Orchestrator{
		workflows: &mockWorkflowResolver{},
		store:     store,
		actors:    &mockActorAssigner{},
		artifacts: &mockArtifactReader{},
		events:    &mockEventEmitter{},
		git:       g,
		wfLoader:  &mockWorkflowLoader{wfDef: wf},
		policy:    branchprotect.NewPermissive(),
	}
	if bindingOn {
		if err := orch.EnableMergeBinding(MergeBindingConfig{MovedHead: policy}); err != nil {
			t.Fatalf("EnableMergeBinding: %v", err)
		}
	}
	return &bindFix{t: t, store: store, git: g, orch: orch, wf: wf, run: run}
}

// exec adds a step execution in status st, bound to actor "worker".
func (f *bindFix) exec(stepID string, st domain.StepExecutionStatus) *domain.StepExecution {
	attempt := f.orch.nextAttempt(context.Background(), f.run.RunID, stepID)
	e := &domain.StepExecution{
		ExecutionID: fmt.Sprintf("%s-%s-%d", f.run.RunID, stepID, attempt),
		RunID:       f.run.RunID, StepID: stepID, Status: st, ActorID: "worker", Attempt: attempt,
	}
	if st == domain.StepStatusInProgress || st == domain.StepStatusCompleted {
		e.ActorID = "worker"
	}
	f.store.createdSteps = append(f.store.createdSteps, e)
	f.run.CurrentStepID = stepID
	return e
}

// bindAt stores a binding this run recorded at a step.
func (f *bindFix) bindAt(sha, stepID string) {
	meta := map[string]string{}
	for k, v := range f.run.CommitMeta {
		meta[k] = v
	}
	meta[CommitMetaBoundSHA] = sha
	meta[CommitMetaBoundBy] = boundByStep(stepID, f.run.RunID+"-"+stepID+"-1")
	f.run.CommitMeta = meta
}

func (f *bindFix) submit(e *domain.StepExecution, outcome, expected string) error {
	return f.orch.SubmitStepResult(context.Background(), e.ExecutionID, StepResult{OutcomeID: outcome, ExpectedHeadSHA: expected})
}

func (f *bindFix) primaryOutcome() *domain.RepositoryMergeOutcome {
	for i := range f.store.mergeOutcomes {
		if f.store.mergeOutcomes[i].RepositoryID == repository.PrimaryRepositoryID {
			return &f.store.mergeOutcomes[i]
		}
	}
	return nil
}

// latestExec returns the newest execution of stepID.
func (f *bindFix) latestExec(stepID string) *domain.StepExecution {
	var out *domain.StepExecution
	for _, e := range f.store.createdSteps {
		if e.StepID == stepID && (out == nil || e.Attempt > out.Attempt) {
			out = e
		}
	}
	return out
}

func requireBindingErr(t *testing.T, err error, code domain.ErrorCode, reason string) {
	t.Helper()
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	d, ok := se.Detail.(HeadBindingDetail)
	if !ok || d.Reason != reason {
		t.Fatalf("detail = %#v, want reason %s", se.Detail, reason)
	}
}
