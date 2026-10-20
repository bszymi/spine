package main

// INIT-023 TASK-005: `spine serve` merges by SHA. AC 8 (both orchestrator
// builders enable binding, with no switch to turn it off — owner question
// 6) and AC 4's gateway half (the resultAdapter passes expected_head_sha
// into engine.SubmitRequest).

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

func TestServeWiring_EnablesMergeBinding(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	repoPath := testutil.NewTempRepo(t)
	gitClient := git.NewCLIClient(repoPath)
	q := queue.NewMemoryQueue(4)
	events := event.NewQueueRouter(q)

	st := newTimeoutStore()
	_, wfProvider := buildWorkflowResolver(st, gitClient)
	orch := buildOrchestrator(st, wfProvider, gitClient, events, q, nil, nil, engine.StrictStepEntryPolicy(), nil, observe.Logger(ctx))
	if orch == nil {
		t.Fatal("buildOrchestrator returned nil")
	}
	if !orch.MergeBindingEnabled() {
		t.Fatal("buildOrchestrator: merge binding is off")
	}

	wsStore := newTimeoutStore()
	repoSpec := repository.PrimarySpec{LocalPath: repoPath}
	reg := repository.New("", repoSpec, func(context.Context) (*repository.Catalog, error) {
		return repository.ParseCatalog(nil, repoSpec)
	}, wsStore)
	pool, err := gitpool.New(gitClient, reg, gitpool.NewCLIClientFactory())
	if err != nil {
		t.Fatal(err)
	}
	ss := &workspace.ServiceSet{Store: wsStore, GitClient: gitClient, GitPool: pool, Queue: q, Events: events}
	if err := workspaceOrchestratorBuilder(ctx, ss, engine.StepEntryPolicy{}); err != nil {
		t.Fatalf("workspaceOrchestratorBuilder: %v", err)
	}
	wsOrch, ok := ss.StepAssigner.(*engine.Orchestrator)
	if !ok {
		t.Fatalf("ss.StepAssigner = %T", ss.StepAssigner)
	}
	if !wsOrch.MergeBindingEnabled() {
		t.Fatal("workspace orchestrator: merge binding is off")
	}
}

// headSubmitStore serves one assigned review execution of an active run
// that has no branch.
type headSubmitStore struct {
	stubStore
	run  domain.Run
	exec domain.StepExecution
}

func (s *headSubmitStore) GetRun(context.Context, string) (*domain.Run, error) {
	r := s.run
	return &r, nil
}

func (s *headSubmitStore) GetStepExecution(context.Context, string) (*domain.StepExecution, error) {
	e := s.exec
	return &e, nil
}

type headWF struct{ wf *domain.WorkflowDefinition }

func (h headWF) LoadWorkflow(context.Context, string, string) (*domain.WorkflowDefinition, error) {
	return h.wf, nil
}

func (h headWF) ResolveWorkflow(context.Context, string, string) (*workflow.BindingResult, error) {
	return &workflow.BindingResult{Workflow: h.wf}, nil
}

func (h headWF) ResolveWorkflowForMode(context.Context, string, string, string) (*workflow.BindingResult, error) {
	return &workflow.BindingResult{Workflow: h.wf}, nil
}

type headNoop struct{}

func (headNoop) DeliverAssignment(context.Context, actor.AssignmentRequest) error { return nil }
func (headNoop) ProcessResult(context.Context, actor.AssignmentRequest, actor.AssignmentResult) error {
	return nil
}
func (headNoop) Read(context.Context, string, string) (*domain.Artifact, error) { return nil, nil }
func (headNoop) Emit(context.Context, domain.Event) error                       { return nil }

func TestResultAdapter_PassesExpectedHeadSHA(t *testing.T) {
	wf := &domain.WorkflowDefinition{ID: "wf", EntryStep: "review", Steps: []domain.StepDefinition{
		{ID: "review", Name: "Review", Type: domain.StepTypeReview,
			Outcomes: []domain.OutcomeDefinition{{ID: "accepted", Name: "Accepted", NextStep: "end"}}},
	}}
	st := &headSubmitStore{
		run: domain.Run{RunID: "run-1", Status: domain.RunStatusActive, TraceID: "trace-0123456789ab"},
		exec: domain.StepExecution{ExecutionID: "run-1-review-1", RunID: "run-1", StepID: "review",
			Status: domain.StepStatusAssigned, ActorID: "alice", Attempt: 1},
	}
	orch, err := engine.New(headWF{wf}, st, headNoop{}, headNoop{}, headNoop{}, git.NewCLIClient(testutil.NewTempRepo(t)), headWF{wf})
	if err != nil {
		t.Fatal(err)
	}
	a := &resultAdapter{orch: orch}
	for value, reason := range map[string]string{
		"abc1234": engine.ReasonExpectedHeadInvalid,
		"0123456789abcdef0123456789abcdef01234567": engine.ReasonExpectedHeadNotApplicable, // the run has no branch
	} {
		_, err := a.IngestResult(context.Background(), gateway.ResultSubmission{
			ExecutionID: "run-1-review-1", OutcomeID: "accepted", ExpectedHeadSHA: value,
		})
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrInvalidParams {
			t.Fatalf("expected_head_sha %q: err = %v, want invalid_params", value, err)
		}
		if d, ok := se.Detail.(engine.HeadBindingDetail); !ok || d.Reason != reason {
			t.Fatalf("expected_head_sha %q: detail %#v, want reason %s", value, se.Detail, reason)
		}
	}
}
