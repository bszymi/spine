package engine

// Fixtures for the INIT-023 TASK-008 compare-and-swap, ownership and
// evidence tests: an in-memory RunStore with value semantics (reads and
// writes copy, as with PostgreSQL, so the engine's snapshot never aliases
// the stored row), read hooks that fire after a read has taken its
// snapshot — exactly the window a concurrent writer uses — and an optional
// StepExecutionTransitioner capability (condStore).

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
)

type stepTransitionCall struct {
	executionID string
	fromStatus  domain.StepExecutionStatus
	fromActor   string
	toStatus    domain.StepExecutionStatus
	applied     bool
}

// casStore is a blind store: it has no TransitionStepExecution method.
type casStore struct {
	mu    sync.Mutex
	runs  map[string]*domain.Run
	steps map[string]*domain.StepExecution

	runReads  map[string]int
	execReads map[string]int
	// afterGetRun / afterGetExec run after the n-th read (1-based) of a
	// row has copied its snapshot, outside the store lock, so they may
	// call back into the orchestrator.
	afterGetRun  func(runID string, n int)
	afterGetExec func(execID string, n int)
	afterList    func(runID string, n int)
	listReads    map[string]int

	runWrites   []domain.RunStatus // every applied run status write
	blindRun    int                // UpdateRunStatus calls
	stepUpdates []string           // UpdateStepExecution calls ("<id> <status>")
	transitions []stepTransitionCall
	metaWrites  []map[string]string
	outcomes    map[string]domain.RepositoryMergeOutcome
	branches    map[string]*domain.Branch

	// transitionRunErr makes TransitionRunStatus fail like SMP's store does
	// for a missing row.
	transitionRunErr error
	transitionRuns   int
}

func newCasStore() *casStore {
	return &casStore{
		runs: map[string]*domain.Run{}, steps: map[string]*domain.StepExecution{},
		runReads: map[string]int{}, execReads: map[string]int{}, listReads: map[string]int{},
		outcomes: map[string]domain.RepositoryMergeOutcome{}, branches: map[string]*domain.Branch{},
	}
}

func copyRun(r *domain.Run) *domain.Run {
	cp := *r
	if r.CommitMeta != nil {
		cp.CommitMeta = make(map[string]string, len(r.CommitMeta))
		for k, v := range r.CommitMeta {
			cp.CommitMeta[k] = v
		}
	}
	return &cp
}

func copyExec(e *domain.StepExecution) *domain.StepExecution {
	cp := *e
	return &cp
}

func (s *casStore) CreateRun(_ context.Context, run *domain.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.RunID] = copyRun(run)
	return nil
}

func (s *casStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	s.mu.Lock()
	r, ok := s.runs[runID]
	if !ok {
		s.mu.Unlock()
		return nil, domain.NewError(domain.ErrNotFound, "run not found")
	}
	cp := copyRun(r)
	s.runReads[runID]++
	n := s.runReads[runID]
	hook := s.afterGetRun
	s.mu.Unlock()
	if hook != nil {
		hook(runID, n)
	}
	return cp, nil
}

func (s *casStore) UpdateRunStatus(_ context.Context, runID string, status domain.RunStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[runID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "run not found")
	}
	s.blindRun++
	r.Status = status
	s.runWrites = append(s.runWrites, status)
	return nil
}

func (s *casStore) TransitionRunStatus(_ context.Context, runID string, from, to domain.RunStatus) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transitionRuns++
	if s.transitionRunErr != nil {
		return false, s.transitionRunErr
	}
	r, ok := s.runs[runID]
	if !ok {
		return false, nil
	}
	if r.Status != from {
		return false, nil
	}
	r.Status = to
	s.runWrites = append(s.runWrites, to)
	return true, nil
}

// setRunStatus is a concurrent writer's committed run write.
func (s *casStore) setRunStatus(runID string, st domain.RunStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[runID].Status = st
}

func (s *casStore) UpdateCurrentStep(_ context.Context, runID, stepID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[runID]; ok {
		r.CurrentStepID = stepID
	}
	return nil
}

func (s *casStore) SetCommitMeta(_ context.Context, runID string, meta map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[runID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "run not found")
	}
	cp := make(map[string]string, len(meta))
	for k, v := range meta {
		cp[k] = v
	}
	r.CommitMeta = cp
	s.metaWrites = append(s.metaWrites, cp)
	return nil
}

func (s *casStore) CreateStepExecution(_ context.Context, e *domain.StepExecution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.steps[e.ExecutionID]; dup {
		return fmt.Errorf("duplicate key: step execution %s", e.ExecutionID)
	}
	s.steps[e.ExecutionID] = copyExec(e)
	return nil
}

func (s *casStore) GetStepExecution(_ context.Context, id string) (*domain.StepExecution, error) {
	s.mu.Lock()
	e, ok := s.steps[id]
	if !ok {
		s.mu.Unlock()
		return nil, domain.NewError(domain.ErrNotFound, "step execution not found")
	}
	cp := copyExec(e)
	s.execReads[id]++
	n := s.execReads[id]
	hook := s.afterGetExec
	s.mu.Unlock()
	if hook != nil {
		hook(id, n)
	}
	return cp, nil
}

func (s *casStore) UpdateStepExecution(_ context.Context, e *domain.StepExecution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stepUpdates = append(s.stepUpdates, e.ExecutionID+" "+string(e.Status))
	s.steps[e.ExecutionID] = copyExec(e)
	return nil
}

// mutateExec is a concurrent writer's committed step write.
func (s *casStore) mutateExec(id string, f func(e *domain.StepExecution)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s.steps[id])
}

func (s *casStore) exec(id string) *domain.StepExecution {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.steps[id]
	if !ok {
		return nil
	}
	return copyExec(e)
}

func (s *casStore) run(id string) *domain.Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyRun(s.runs[id])
}

func (s *casStore) ListStepExecutionsByRun(_ context.Context, runID string) ([]domain.StepExecution, error) {
	out := s.list(runID)
	s.mu.Lock()
	s.listReads[runID]++
	n := s.listReads[runID]
	hook := s.afterList
	s.mu.Unlock()
	if hook != nil {
		hook(runID, n)
	}
	return out, nil
}

func (s *casStore) list(runID string) []domain.StepExecution {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.StepExecution
	for _, e := range s.steps {
		if e.RunID == runID {
			out = append(out, *e)
		}
	}
	// PostgreSQL orders by created_at; ties by id keep the fake stable.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ExecutionID < out[j].ExecutionID
	})
	return out
}

func (s *casStore) CreateDivergenceContext(context.Context, *domain.DivergenceContext) error {
	return nil
}
func (s *casStore) UpdateDivergenceContext(context.Context, *domain.DivergenceContext) error {
	return nil
}
func (s *casStore) GetDivergenceContext(context.Context, string) (*domain.DivergenceContext, error) {
	return nil, domain.NewError(domain.ErrNotFound, "divergence not found")
}
func (s *casStore) CreateBranch(_ context.Context, b *domain.Branch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *b
	s.branches[b.BranchID] = &cp
	return nil
}
func (s *casStore) UpdateBranch(_ context.Context, b *domain.Branch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *b
	s.branches[b.BranchID] = &cp
	return nil
}
func (s *casStore) GetBranch(_ context.Context, id string) (*domain.Branch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.branches[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "branch not found")
	}
	cp := *b
	return &cp, nil
}
func (s *casStore) ListBranchesByDivergence(context.Context, string) ([]domain.Branch, error) {
	return nil, nil
}
func (s *casStore) UpsertRepositoryMergeOutcome(_ context.Context, o *domain.RepositoryMergeOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes[o.RunID+"/"+o.RepositoryID] = *o
	return nil
}
func (s *casStore) GetRepositoryMergeOutcome(_ context.Context, runID, repoID string) (*domain.RepositoryMergeOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.outcomes[runID+"/"+repoID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "merge outcome not found")
	}
	return &o, nil
}
func (s *casStore) ListRepositoryMergeOutcomes(_ context.Context, runID string) ([]domain.RepositoryMergeOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.RepositoryMergeOutcome{}
	for k := range s.outcomes {
		if s.outcomes[k].RunID == runID {
			out = append(out, s.outcomes[k])
		}
	}
	return out, nil
}

// condStore is casStore with the StepExecutionTransitioner capability: the
// standalone PostgresStore's conditional write, over the same rows.
type condStore struct{ *casStore }

var (
	_ StepExecutionTransitioner = condStore{}
	_ RunCursorTransitioner     = condStore{}
	_ BranchCursorTransitioner  = condStore{}
)

// TransitionBranchCursor is (*store.PostgresStore).TransitionBranchCursor
// over the same rows: the cursor only, while the branch is open and at from.
func (s condStore) TransitionBranchCursor(_ context.Context, branchID, from, to string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.branches[branchID]
	if !ok || (b.Status != domain.BranchStatusPending && b.Status != domain.BranchStatusInProgress) || b.CurrentStepID != from {
		return false, nil
	}
	b.CurrentStepID = to
	return true, nil
}

func (s condStore) TransitionCurrentStep(_ context.Context, runID, from, to string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[runID]
	if !ok || r.Status != domain.RunStatusActive || r.CurrentStepID != from {
		return false, nil
	}
	r.CurrentStepID = to
	return true, nil
}

func (s condStore) TransitionStepExecution(_ context.Context, e *domain.StepExecution, from domain.StepExecutionStatus, fromActor string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.steps[e.ExecutionID]
	if !ok {
		return false, domain.NewError(domain.ErrNotFound, "step execution not found")
	}
	applied := cur.Status == from && cur.ActorID == fromActor
	s.transitions = append(s.transitions, stepTransitionCall{e.ExecutionID, from, fromActor, e.Status, applied})
	if applied {
		s.steps[e.ExecutionID] = copyExec(e)
	}
	return applied, nil
}

// casFix is an orchestrator over a casStore (blind) or condStore (with the
// capability), a fake binder GitOperator and recording collaborators, with
// one active run.
type casFix struct {
	t      *testing.T
	st     *casStore
	git    *fakeBinderGit
	ev     *mockEventEmitter
	actors *mockActorAssigner
	asg    *memAssignmentStore
	orch   *Orchestrator
	wf     *domain.WorkflowDefinition
	runID  string
}

const casTrace = "trace-0123456789abcdef"

func newCasFix(t *testing.T, wf *domain.WorkflowDefinition, capability bool) *casFix {
	t.Helper()
	st := newCasStore()
	var rs RunStore = st
	if capability {
		rs = condStore{st}
	}
	g := newFakeBinderGit()
	ev := &mockEventEmitter{}
	actors := &mockActorAssigner{}
	asg := newMemAssignmentStore()
	orch := &Orchestrator{
		workflows: &mockWorkflowResolver{}, store: rs, actors: actors,
		artifacts: &mockArtifactReader{}, events: ev, git: g,
		wfLoader: &mockWorkflowLoader{wfDef: wf}, policy: branchprotect.NewPermissive(),
		assignments: asg,
	}
	f := &casFix{t: t, st: st, git: g, ev: ev, actors: actors, asg: asg, orch: orch, wf: wf, runID: "run-1"}
	now := time.Now()
	st.runs["run-1"] = &domain.Run{
		RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: wf.Path, WorkflowID: wf.ID,
		WorkflowVersion: "v1", Status: domain.RunStatusActive, BranchName: "spine/run/run-1",
		TraceID: casTrace, StartedAt: &now, CurrentStepID: wf.EntryStep,
		RepositoryBaselines: map[string]string{domain.PrimaryRepositoryID: sha40('b')},
	}
	return f
}

// addExec stores an execution of stepID; attempt and created order follow
// the call order.
func (f *casFix) addExec(stepID string, st domain.StepExecutionStatus, actorID string) *domain.StepExecution {
	f.t.Helper()
	attempt := f.orch.nextAttempt(context.Background(), f.runID, stepID)
	n := len(f.st.steps)
	created := time.Date(2026, 10, 1, 12, 0, n, 0, time.UTC)
	started := created
	e := &domain.StepExecution{
		ExecutionID: fmt.Sprintf("%s-%s-%d", f.runID, stepID, attempt), RunID: f.runID, StepID: stepID,
		Status: st, ActorID: actorID, Attempt: attempt, CreatedAt: created,
	}
	if st == domain.StepStatusInProgress || st == domain.StepStatusCompleted {
		e.StartedAt = &started
	}
	if st.IsTerminal() {
		done := created.Add(time.Second)
		e.CompletedAt = &done
	}
	f.st.steps[e.ExecutionID] = copyExec(e)
	f.st.mu.Lock()
	f.st.runs[f.runID].CurrentStepID = stepID
	f.st.mu.Unlock()
	return e
}

// afterNextList runs fn once, right after the next ListStepExecutionsByRun
// of the run has copied its rows (setup reads such as nextAttempt have
// already happened by the time a test arms it).
func (f *casFix) afterNextList(fn func()) {
	f.st.mu.Lock()
	at := f.st.listReads[f.runID] + 1
	f.st.mu.Unlock()
	f.st.afterList = func(runID string, n int) {
		if runID == f.runID && n == at {
			fn()
		}
	}
}

func (f *casFix) events(tp domain.EventType) []domain.Event {
	var out []domain.Event
	for i := range f.ev.events {
		if f.ev.events[i].Type == tp {
			out = append(out, f.ev.events[i])
		}
	}
	return out
}

func (f *casFix) governedMerges() int { return len(f.git.plainMerges) + len(f.git.merges) }

// casWorkflow is execute → review; review "accepted" ends the run with a
// commit (a governed merge), "rejected" sends it back to execute.
func casWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "wf-cas", Name: "wf", Version: "1.0.0", Status: domain.WorkflowStatusActive,
		AppliesTo: []string{"Task"}, EntryStep: "execute", Path: "workflows/wf-cas.yaml",
		Steps: []domain.StepDefinition{
			{
				ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				RequiredOutputs: []string{"deliverable"},
				Outcomes:        []domain.OutcomeDefinition{{ID: "completed", Name: "Completed", NextStep: "review"}},
			},
			{
				ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "approved", Name: "Approved", Commit: map[string]string{"status": "Completed"}},
					{ID: "rejected", Name: "Rejected", NextStep: "execute"},
				},
			},
		},
	}
}
