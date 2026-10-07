package gateway_test

// End-to-end regression for INIT-023 TASK-013 (audit F6), ported from
// the 2026-10-06 audit PoC poc/verify-r1-4 with its assertions
// inverted. It drives the REAL standalone gateway router, the REAL
// engine.Orchestrator (ClaimStep, IngestResult and the publish/merge
// handler) and the REAL workflows/task-default.yaml definition.
//
// Against 61994fe a contributor executed its own task, granted itself
// the "review" skill through POST /actors/{self}/skills/{id}, stripped
// the real reviewer's skill, claimed the review step, submitted
// "accepted", and the publish step merged the run branch into main.
// Now the grant and the strip are refused, the claim is refused for a
// missing skill, and nothing is merged.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/service/internal/gateway"
)

// ── in-memory run/step store layered on skillStore ──

type selfReviewStore struct {
	*skillStore
	mu    sync.Mutex
	runs  map[string]*domain.Run
	execs map[string]*domain.StepExecution
	order []string
}

func (s *selfReviewStore) GetRun(_ context.Context, id string) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "run not found")
	}
	cp := *r
	return &cp, nil
}

func (s *selfReviewStore) UpdateRunStatus(_ context.Context, id string, st domain.RunStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[id].Status = st
	return nil
}

func (s *selfReviewStore) UpdateRunStatusAt(ctx context.Context, id string, st domain.RunStatus, _ time.Time) error {
	return s.UpdateRunStatus(ctx, id, st)
}

func (s *selfReviewStore) TransitionRunStatus(_ context.Context, id string, from, to domain.RunStatus) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[id].Status != from {
		return false, nil
	}
	s.runs[id].Status = to
	return true, nil
}

func (s *selfReviewStore) UpdateCurrentStep(_ context.Context, id, step string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[id].CurrentStepID = step
	return nil
}

func (s *selfReviewStore) SetCommitMeta(_ context.Context, id string, m map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[id].CommitMeta = m
	return nil
}

func (s *selfReviewStore) CreateStepExecution(_ context.Context, e *domain.StepExecution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *e
	s.execs[e.ExecutionID] = &cp
	s.order = append(s.order, e.ExecutionID)
	return nil
}

func (s *selfReviewStore) GetStepExecution(_ context.Context, id string) (*domain.StepExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.execs[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "execution not found")
	}
	cp := *e
	return &cp, nil
}

func (s *selfReviewStore) UpdateStepExecution(_ context.Context, e *domain.StepExecution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *e
	s.execs[e.ExecutionID] = &cp
	return nil
}

func (s *selfReviewStore) ListStepExecutionsByRun(_ context.Context, runID string) ([]domain.StepExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.StepExecution
	for _, id := range s.order {
		if e := s.execs[id]; e.RunID == runID {
			out = append(out, *e)
		}
	}
	return out, nil
}

func (s *selfReviewStore) ListRepositoryMergeOutcomes(_ context.Context, _ string) ([]domain.RepositoryMergeOutcome, error) {
	return nil, nil
}

func (s *selfReviewStore) GetExecutionProjection(_ context.Context, _ string) (*store.ExecutionProjection, error) {
	return nil, domain.NewError(domain.ErrNotFound, "no projection")
}

func (s *selfReviewStore) UpsertExecutionProjection(_ context.Context, _ *store.ExecutionProjection) error {
	return nil
}

// ── engine dependencies ──

type selfReviewEvents struct{}

func (selfReviewEvents) Emit(_ context.Context, _ domain.Event) error { return nil }

type selfReviewWorkflows struct{ wf *domain.WorkflowDefinition }

func (l selfReviewWorkflows) LoadWorkflow(_ context.Context, _, _ string) (*domain.WorkflowDefinition, error) {
	return l.wf, nil
}

// recordingGit records every Merge so the test can assert that nothing
// reached main.
type recordingGit struct {
	mu     sync.Mutex
	merges []git.MergeOpts
}

func (g *recordingGit) Checkout(context.Context, string) error { return nil }
func (g *recordingGit) Commit(context.Context, git.CommitOpts) (git.CommitResult, error) {
	return git.CommitResult{SHA: "c0ffee"}, nil
}
func (g *recordingGit) Merge(_ context.Context, o git.MergeOpts) (git.MergeResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.merges = append(g.merges, o)
	return git.MergeResult{SHA: "deadbeef"}, nil
}
func (g *recordingGit) CreateBranch(context.Context, string, string) error           { return nil }
func (g *recordingGit) DeleteBranch(context.Context, string) error                   { return nil }
func (g *recordingGit) Diff(context.Context, string, string) ([]git.FileDiff, error) { return nil, nil }
func (g *recordingGit) MergeBase(context.Context, string, string) (string, error)    { return "base", nil }
func (g *recordingGit) Head(context.Context) (string, error)                         { return "deadbeef", nil }
func (g *recordingGit) Push(context.Context, string, string) error                   { return nil }
func (g *recordingGit) PushBranch(context.Context, string, string) error             { return nil }
func (g *recordingGit) DeleteRemoteBranch(context.Context, string, string) error     { return nil }
func (g *recordingGit) ReadFile(context.Context, string, string) ([]byte, error) {
	return nil, domain.NewError(domain.ErrNotFound, "no file")
}
func (g *recordingGit) WriteAndStageFile(context.Context, string, string) error { return nil }

func (g *recordingGit) mergeCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.merges)
}

type selfReviewUnused struct {
	engine.WorkflowResolver
	engine.ActorAssigner
	engine.ArtifactReader
}

type selfReviewResults struct{ orch *engine.Orchestrator }

func (a selfReviewResults) IngestResult(ctx context.Context, req gateway.ResultSubmission) (*gateway.ResultResponse, error) {
	resp, err := a.orch.IngestResult(ctx, engine.SubmitRequest{
		ExecutionID: req.ExecutionID, OutcomeID: req.OutcomeID, ArtifactsProduced: req.ArtifactsProduced,
	})
	if err != nil {
		return nil, err
	}
	return &gateway.ResultResponse{ExecutionID: resp.ExecutionID, StepID: resp.StepID, Status: string(resp.Status), OutcomeID: resp.OutcomeID}, nil
}

func selfReviewDo(t *testing.T, method, url, tok, body string) (int, string) {
	t.Helper()
	resp := skillRequest(t, method, url, tok, body)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// ── AC 2 ──

func TestSkillAuthority_SelfGrantedReviewChainBlocked(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../../../workflows/task-default.yaml")
	if err != nil {
		t.Fatalf("read real workflow: %v", err)
	}
	wf, err := workflow.Parse("workflows/task-default.yaml", raw)
	if err != nil {
		t.Fatalf("parse real workflow: %v", err)
	}

	ss := newSkillStore()
	st := &selfReviewStore{skillStore: ss, runs: map[string]*domain.Run{}, execs: map[string]*domain.StepExecution{}}

	// mallory is a plain contributor (the POST /actors default role);
	// alice is the real reviewer.
	ss.actors["mallory"] = &domain.Actor{ActorID: "mallory", Type: domain.ActorTypeHuman, Role: domain.RoleContributor, Status: domain.ActorStatusActive}
	ss.actors["alice"] = &domain.Actor{ActorID: "alice", Type: domain.ActorTypeHuman, Role: domain.RoleReviewer, Status: domain.ActorStatusActive}
	ss.skills["sk-exec"] = &domain.Skill{SkillID: "sk-exec", Name: "execution", Status: domain.SkillStatusActive}
	ss.skills["sk-review"] = &domain.Skill{SkillID: "sk-review", Name: "review", Status: domain.SkillStatusActive}
	_ = ss.AddSkillToActor(ctx, "mallory", "sk-exec") // legitimately an executor
	_ = ss.AddSkillToActor(ctx, "alice", "sk-review") // legitimately the reviewer

	authSvc := auth.NewService(ss)
	malTok, _, err := authSvc.CreateToken(ctx, "mallory", "t", nil)
	if err != nil {
		t.Fatal(err)
	}

	g := &recordingGit{}
	u := selfReviewUnused{}
	orch, err := engine.New(u, st, u, u, selfReviewEvents{}, g, selfReviewWorkflows{wf: wf})
	if err != nil {
		t.Fatal(err)
	}
	orch.WithBlockingStore(st)
	orch.WithAssignmentStore(st)
	orch.WithBranchProtectPolicy(branchprotect.New(nil))

	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store: st, Auth: authSvc, StepClaimer: orch, ResultHandler: selfReviewResults{orch: orch},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Run state: mallory has just executed her own task; the review
	// step is waiting.
	now := time.Now()
	st.runs["run-1"] = &domain.Run{RunID: "run-1", TaskPath: "initiatives/x/tasks/TASK-001.md", WorkflowPath: "workflows/task-default.yaml",
		WorkflowID: "task-default", WorkflowVersion: "abc", Status: domain.RunStatusActive, CurrentStepID: "review",
		BranchName: "spine/run/run-1", TraceID: "trace-0123456789abcdef", PrimaryRepository: true}
	_ = st.CreateStepExecution(ctx, &domain.StepExecution{ExecutionID: "run-1-execute-1", RunID: "run-1", StepID: "execute",
		ActorID: "mallory", Status: domain.StepStatusCompleted, OutcomeID: "completed", Attempt: 1, CreatedAt: now})
	_ = st.CreateStepExecution(ctx, &domain.StepExecution{ExecutionID: "run-1-review-1", RunID: "run-1", StepID: "review",
		Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: now})

	claim := `{"actor_id":"mallory","execution_id":"run-1-review-1"}`

	// [1] Precondition: the review skill is the gate.
	if code, body := selfReviewDo(t, "POST", ts.URL+"/api/v1/execution/claim", malTok, claim); code == http.StatusOK {
		t.Fatalf("[1] claim before any grant should be refused: %d %s", code, body)
	}

	// [2] Self-grant of review: refused.
	if code, body := selfReviewDo(t, "POST", ts.URL+"/api/v1/actors/mallory/skills/sk-review", malTok, ""); code != http.StatusForbidden {
		t.Fatalf("[2] self-grant of review: %d %s, want 403", code, body)
	}

	// [3] Strip the real reviewer: refused, alice stays eligible.
	if code, body := selfReviewDo(t, "DELETE", ts.URL+"/api/v1/actors/alice/skills/sk-review", malTok, ""); code != http.StatusForbidden {
		t.Fatalf("[3] strip of alice's review skill: %d %s, want 403", code, body)
	}
	if !eligible(t, ss, "alice", "review") {
		t.Fatal("[3] alice lost review eligibility")
	}

	// [4] mallory's claim of her own review step: refused.
	code, body := selfReviewDo(t, "POST", ts.URL+"/api/v1/execution/claim", malTok, claim)
	if code == http.StatusOK {
		t.Fatalf("[4] mallory claimed her own review step: %s", body)
	}
	if !strings.Contains(body, "missing required skills") {
		t.Errorf("[4] claim refused for an unexpected reason: %d %s", code, body)
	}

	// [5] Even a submit attempt cannot accept the unclaimed review.
	_, _ = selfReviewDo(t, "POST", ts.URL+"/api/v1/steps/run-1-review-1/submit", malTok, `{"outcome_id":"accepted"}`)

	if n := g.mergeCount(); n != 0 {
		t.Fatalf("recording git operator saw %d merge(s); want 0", n)
	}
	if rv, _ := st.GetStepExecution(ctx, "run-1-review-1"); rv.Status == domain.StepStatusCompleted || rv.ActorID == "mallory" {
		t.Fatalf("review execution changed hands: status=%s actor=%s", rv.Status, rv.ActorID)
	}
}
