package gateway_test

// INIT-023 TASK-008: the gateway forwards the authenticated submitter
// (AC 4), its store fallbacks write conditionally (AC 12) and run detail
// carries step-output evidence (AC 16).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/service/internal/gateway"
)

func casTokenFor(t *testing.T, fs *fakeStore, id string, role domain.ActorRole) (*auth.Service, string) {
	t.Helper()
	fs.actors[id] = &domain.Actor{ActorID: id, Type: domain.ActorTypeHuman, Name: id, Role: role, Status: domain.ActorStatusActive}
	authSvc := auth.NewService(fs)
	token, _, err := authSvc.CreateToken(context.Background(), id, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return authSvc, token
}

func casDoJSON(t *testing.T, method, url, token, body string) (*http.Response, string) {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}

// AC 4 (a): the authenticated actor reaches ResultSubmission.ActorID.
func TestStepSubmit_ForwardsAuthenticatedActor(t *testing.T) {
	fs := newFakeStore()
	fs.stepExecOverride = &domain.StepExecution{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
		Status: domain.StepStatusInProgress, Attempt: 1, ActorID: "alice"}
	authSvc, token := casTokenFor(t, fs, "alice", domain.RoleContributor)
	rh := &capturingResultHandler{}
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, ResultHandler: rh}).Handler())
	defer ts.Close()

	resp, body := casDoJSON(t, "POST", ts.URL+"/api/v1/steps/exec-1/submit", token, `{"outcome_id":"completed"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit: %d %s", resp.StatusCode, body)
	}
	if len(rh.got) != 1 || rh.got[0].ActorID != "alice" {
		t.Fatalf("submissions = %+v, want ActorID alice", rh.got)
	}
}

// casEngineResultHandler mirrors service/cmd/spine's resultAdapter: it
// forwards the submission, ActorID included, into IngestResult.
type casEngineResultHandler struct{ orch *engine.Orchestrator }

func (h casEngineResultHandler) IngestResult(ctx context.Context, req gateway.ResultSubmission) (*gateway.ResultResponse, error) {
	resp, err := h.orch.IngestResult(ctx, engine.SubmitRequest{
		ExecutionID: req.ExecutionID, OutcomeID: req.OutcomeID,
		ArtifactsProduced: req.ArtifactsProduced, ExpectedHeadSHA: req.ExpectedHeadSHA, ActorID: req.ActorID,
	})
	if err != nil {
		return nil, err
	}
	return &gateway.ResultResponse{ExecutionID: resp.ExecutionID, StepID: resp.StepID, Status: string(resp.Status), OutcomeID: resp.OutcomeID}, nil
}

// casEngineRunStore is the minimum RunStore for the engine paths these tests
// reach; anything else panics (nil embedded interface).
type casEngineRunStore struct {
	engine.RunStore
	exec  *domain.StepExecution
	run   *domain.Run
	execs []domain.StepExecution
}

func (s *casEngineRunStore) GetStepExecution(context.Context, string) (*domain.StepExecution, error) {
	cp := *s.exec
	return &cp, nil
}
func (s *casEngineRunStore) GetRun(context.Context, string) (*domain.Run, error) {
	cp := *s.run
	return &cp, nil
}
func (s *casEngineRunStore) ListStepExecutionsByRun(context.Context, string) ([]domain.StepExecution, error) {
	return s.execs, nil
}

type (
	casNilResolver  struct{ engine.WorkflowResolver }
	casNilActors    struct{ engine.ActorAssigner }
	casNilArtifacts struct{ engine.ArtifactReader }
	casNilEvents    struct{ engine.EventEmitter }
)

// casNoGit fails the test on any call: run detail and a refused
// submit must not touch git.
type casNoGit struct {
	engine.GitOperator
	calls int
}

func (g *casNoGit) Diff(context.Context, string, string) ([]git.FileDiff, error) {
	g.calls++
	return nil, errors.New("no git expected")
}

type casFixedLoader struct{ wf *domain.WorkflowDefinition }

func (l casFixedLoader) LoadWorkflow(context.Context, string, string) (*domain.WorkflowDefinition, error) {
	return l.wf, nil
}

func casNewTestOrch(t *testing.T, rs engine.RunStore, wf *domain.WorkflowDefinition) *engine.Orchestrator {
	t.Helper()
	orch, err := engine.New(casNilResolver{}, rs, casNilActors{}, casNilArtifacts{}, casNilEvents{}, &casNoGit{}, casFixedLoader{wf: wf})
	if err != nil {
		t.Fatal(err)
	}
	return orch
}

// AC 4 (b): with no store wired (the gateway pre-check's fall-through
// branch) the engine still refuses a non-owner: 403.
func TestStepSubmit_EngineRefusesForeignSubmitterWithoutStore(t *testing.T) {
	fs := newFakeStore()
	authSvc, token := casTokenFor(t, fs, "alice", domain.RoleContributor)
	rs := &casEngineRunStore{exec: &domain.StepExecution{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
		Status: domain.StepStatusInProgress, Attempt: 1, ActorID: "bob"}}
	orch := casNewTestOrch(t, rs, &domain.WorkflowDefinition{})
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{
		Auth: authSvc, ResultHandler: casEngineResultHandler{orch: orch}, // no Store
	}).Handler())
	defer ts.Close()

	resp, body := casDoJSON(t, "POST", ts.URL+"/api/v1/steps/exec-1/submit", token, `{"outcome_id":"completed"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner submit without a store: %d %s, want 403", resp.StatusCode, body)
	}
}

// casLostRaceStore makes every conditional write miss.
type casLostRaceStore struct {
	*fakeStore
	runCAS, stepCAS int
}

func (s *casLostRaceStore) TransitionRunStatus(context.Context, string, domain.RunStatus, domain.RunStatus) (bool, error) {
	s.runCAS++
	return false, nil
}

func (s *casLostRaceStore) TransitionStepExecution(context.Context, *domain.StepExecution, domain.StepExecutionStatus, string) (bool, error) {
	s.stepCAS++
	return false, nil
}

// AC 12: the no-orchestrator cancel fallback and the fallback assigner
// answer 409 on a lost race and write nothing.
func TestStoreFallbacks_LostRaceAnswers409(t *testing.T) {
	ls := &casLostRaceStore{fakeStore: newFakeStore()}
	authSvc, token := casTokenFor(t, ls.fakeStore, "admin-1", domain.RoleAdmin)
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: ls, Auth: authSvc}).Handler())
	defer ts.Close()

	resp, body := casDoJSON(t, "POST", ts.URL+"/api/v1/runs/run-1/cancel", token, "")
	if resp.StatusCode != http.StatusConflict || ls.runCAS != 1 {
		t.Fatalf("cancel fallback: %d %s (CAS %d), want 409 after one conditional write", resp.StatusCode, body, ls.runCAS)
	}
	resp, body = casDoJSON(t, "POST", ts.URL+"/api/v1/runs/run-123/steps/step1/assign", token, `{"actor_id":"admin-1"}`)
	if resp.StatusCode != http.StatusConflict || ls.stepCAS != 1 {
		t.Fatalf("fallback assigner: %d %s (CAS %d), want 409 after one conditional write", resp.StatusCode, body, ls.stepCAS)
	}
}

// casEvidenceCanceller is a RunCanceller that is also the evidence reader,
// as the orchestrator is.
type casEvidenceCanceller struct {
	entries []engine.StepOutputEvidence
	err     error
}

func (casEvidenceCanceller) CancelRun(context.Context, string) error { return nil }
func (e casEvidenceCanceller) StepOutputEvidence(context.Context, string) ([]engine.StepOutputEvidence, error) {
	return e.entries, e.err
}

type casPlainCanceller struct{}

func (casPlainCanceller) CancelRun(context.Context, string) error { return nil }

// AC 16 (gateway): run.status carries step_output_evidence from the
// reader; no reader or a reader error omits the key and stays 200.
func TestRunStatus_StepOutputEvidence(t *testing.T) {
	entry := engine.StepOutputEvidence{ExecutionID: "run-1-execute-1", StepID: "execute", Attempt: 1, OutcomeID: "completed",
		RequiredOutputs: []string{"deliverable"}, RequiredOutputsUnchanged: true,
		BranchChange: engine.BranchChange{RepositoryID: "spine", Status: engine.BranchChangeUnchanged}}
	for _, tc := range []struct {
		name      string
		canceller gateway.RunCanceller
		want      bool
	}{
		{"reader with evidence", casEvidenceCanceller{entries: []engine.StepOutputEvidence{entry}}, true},
		{"reader error", casEvidenceCanceller{err: errors.New("boom")}, false},
		{"reader with no evidence", casEvidenceCanceller{}, false},
		{"no reader", casPlainCanceller{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeStore()
			authSvc, token := casTokenFor(t, fs, "admin-1", domain.RoleAdmin)
			ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, RunCanceller: tc.canceller}).Handler())
			defer ts.Close()
			resp, body := casDoJSON(t, "GET", ts.URL+"/api/v1/runs/run-1", token, "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("run status: %d %s", resp.StatusCode, body)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatal(err)
			}
			raw, ok := got["step_output_evidence"]
			if ok != tc.want {
				t.Fatalf("step_output_evidence present = %v, want %v (%s)", ok, tc.want, body)
			}
			if tc.want {
				var entries []engine.StepOutputEvidence
				if err := json.Unmarshal(raw, &entries); err != nil || len(entries) != 1 || !entries[0].RequiredOutputsUnchanged ||
					entries[0].BranchChange.Status != engine.BranchChangeUnchanged {
					t.Fatalf("step_output_evidence = %s (%v)", raw, err)
				}
			}
		})
	}
}

// AC 16 (gateway, engine-backed): the orchestrator as RunCanceller serves
// run detail from the reserved commit_meta keys with no git call; an
// execution without a key is omitted.
func TestRunStatus_StepOutputEvidenceFromOrchestrator(t *testing.T) {
	rec, _ := json.Marshal(map[string]any{
		"branch_change":              engine.BranchChange{RepositoryID: "spine", Status: engine.BranchChangeUnchanged},
		"required_outputs_unchanged": true,
	})
	rs := &casEngineRunStore{
		run: &domain.Run{RunID: "run-1", Status: domain.RunStatusActive, WorkflowPath: "workflows/w.yaml",
			CommitMeta: map[string]string{engine.CommitMetaEvidencePrefix + "run-1-execute-1": string(rec)}},
		execs: []domain.StepExecution{
			{ExecutionID: "run-1-execute-1", RunID: "run-1", StepID: "execute", Status: domain.StepStatusCompleted, Attempt: 1, OutcomeID: "completed"},
			{ExecutionID: "run-1-review-1", RunID: "run-1", StepID: "review", Status: domain.StepStatusCompleted, Attempt: 1, OutcomeID: "ok"},
		},
	}
	wf := &domain.WorkflowDefinition{Steps: []domain.StepDefinition{{ID: "execute", RequiredOutputs: []string{"deliverable"}}, {ID: "review"}}}
	orch := casNewTestOrch(t, rs, wf)
	fs := newFakeStore()
	authSvc, token := casTokenFor(t, fs, "admin-1", domain.RoleAdmin)
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, RunCanceller: orch}).Handler())
	defer ts.Close()
	resp, body := casDoJSON(t, "GET", ts.URL+"/api/v1/runs/run-1", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run status: %d %s", resp.StatusCode, body)
	}
	var got struct {
		Evidence []engine.StepOutputEvidence `json:"step_output_evidence"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].ExecutionID != "run-1-execute-1" || !got.Evidence[0].RequiredOutputsUnchanged ||
		len(got.Evidence[0].RequiredOutputs) != 1 {
		t.Fatalf("step_output_evidence = %+v", got.Evidence)
	}
}

// Compile-time: the orchestrator is a StepOutputEvidenceReader.
var _ gateway.StepOutputEvidenceReader = (*engine.Orchestrator)(nil)
