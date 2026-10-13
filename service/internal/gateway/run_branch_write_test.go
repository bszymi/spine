package gateway_test

// INIT-023 TASK-014 (audit P3-6) AC 8: TestAuthorizeRunBranchWrite_Matrix
// drives every rule of authorizeRunBranchWrite — the binding (I5), the
// review/internal freeze (I6 amended), the no-cursor rule and the
// fail-closed step lookup — through all five API sinks that write a run
// branch: POST /artifacts and PUT /artifacts/* with write_context, POST
// and PUT /workflows with write_context, and POST /artifacts/add. The
// dev-mode nil-actor rows are internal (run_branch_write_internal_test.go):
// authMiddleware never lets an HTTP request through without a bearer.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/service/internal/gateway"
)

// ── fixtures ──

const (
	rbwRunID  = "run-a1b2"
	rbwBranch = "spine/plan/thing-a1b2" // engine-made: belongs to run-a1b2
	rbwHolder = "holder-1"
	rbwOther  = "foreign-1"
)

const rbwDoc = "---\nid: NEW-001\ntype: Initiative\ntitle: New\nstatus: Draft\nowner: test\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# New\n"

// rbwStore serves one run (by its exact ID), its executions and the runs
// listed for a task path.
type rbwStore struct {
	*fakeStore
	run         *domain.Run
	execs       []domain.StepExecution
	runsByTask  []domain.Run
	listRunsErr error
}

func (s *rbwStore) GetRun(_ context.Context, id string) (*domain.Run, error) {
	if s.run == nil || id != s.run.RunID {
		return nil, domain.NewError(domain.ErrNotFound, "run not found")
	}
	r := *s.run
	return &r, nil
}

func (s *rbwStore) ListStepExecutionsByRun(_ context.Context, _ string) ([]domain.StepExecution, error) {
	return append([]domain.StepExecution(nil), s.execs...), nil
}

func (s *rbwStore) ListRunsByTask(_ context.Context, taskPath string) ([]domain.Run, error) {
	if s.listRunsErr != nil {
		return nil, s.listRunsErr
	}
	var out []domain.Run
	for i := range s.runsByTask {
		if s.runsByTask[i].TaskPath == taskPath {
			out = append(out, s.runsByTask[i])
		}
	}
	return out, nil
}

// rbwSteps is a StepAssigner whose LookupStepDef reports step types by id
// (nil for an unknown id: "cannot resolve").
type rbwSteps struct{ types map[string]domain.StepType }

func (s rbwSteps) AssignStep(context.Context, engine.AssignRequest) (*engine.AssignResult, error) {
	return nil, domain.NewError(domain.ErrUnavailable, "not used")
}

func (s rbwSteps) LookupStepDef(_ context.Context, _, stepID string) (*domain.StepDefinition, *domain.Run) {
	t, ok := s.types[stepID]
	if !ok {
		return nil, nil
	}
	return &domain.StepDefinition{ID: stepID, Type: t}, nil
}

var rbwStepTypes = rbwSteps{types: map[string]domain.StepType{
	"draft":    domain.StepTypeManual,
	"review":   domain.StepTypeReview,
	"publish":  domain.StepTypeInternal,
	"converge": domain.StepTypeConvergence,
}}

// recArtSvc records the artifact writes that reach the service and the
// branch each one carried.
type recArtSvc struct {
	*fakeArtifactService
	writes   int
	branches []string
}

func (r *recArtSvc) record(ctx context.Context) {
	r.writes++
	if wc := artifact.GetWriteContext(ctx); wc != nil {
		r.branches = append(r.branches, wc.Branch)
	}
}

func (r *recArtSvc) Create(ctx context.Context, path, content string) (*artifact.WriteResult, error) {
	r.record(ctx)
	return r.fakeArtifactService.Create(ctx, path, content)
}

func (r *recArtSvc) Update(ctx context.Context, path, content string) (*artifact.WriteResult, error) {
	r.record(ctx)
	return r.fakeArtifactService.Update(ctx, path, content)
}

type rbwEnv struct {
	ts     *httptest.Server
	st     *rbwStore
	art    *recArtSvc
	wf     *fakeWorkflowSvc
	tokens map[string]string
}

// newRBWEnv serves the five sinks over one run. holder and foreign get
// authorRole (contributor for the artifact sinks, reviewer for the
// workflow sinks, which need workflow.create/update).
func newRBWEnv(t *testing.T, run *domain.Run, execs []domain.StepExecution, steps gateway.StepAssigner, authorRole domain.ActorRole) *rbwEnv {
	t.Helper()
	base := newFakeStore()
	roles := map[string]domain.ActorRole{
		rbwHolder: authorRole, rbwOther: authorRole,
		"op-1": domain.RoleOperator, "admin-1": domain.RoleAdmin,
	}
	for id, role := range roles {
		base.actors[id] = &domain.Actor{ActorID: id, Name: id, Type: domain.ActorTypeHuman, Role: role, Status: domain.ActorStatusActive}
	}
	authSvc := auth.NewService(base)
	env := &rbwEnv{
		st:     &rbwStore{fakeStore: base, run: run, execs: execs},
		art:    &recArtSvc{fakeArtifactService: newFakeArtifactService()},
		wf:     newFakeWorkflowSvc(),
		tokens: map[string]string{},
	}
	for id := range roles {
		tok, _, err := authSvc.CreateToken(context.Background(), id, "t", nil)
		if err != nil {
			t.Fatal(err)
		}
		env.tokens[id] = tok
	}
	env.art.artifacts["initiatives/INIT-001/notes.md"] = &domain.Artifact{Path: "initiatives/INIT-001/notes.md", ID: "NOTES", Type: domain.ArtifactTypeInitiative, Title: "Notes"}
	env.art.artifacts["initiatives/INIT-001/initiative.md"] = &domain.Artifact{Path: "initiatives/INIT-001/initiative.md", ID: "INIT-001", Type: domain.ArtifactTypeInitiative, Title: "Init"}
	env.wf.stored["wf-x"] = &domain.WorkflowDefinition{ID: "wf-x", Version: "1.0", Path: "workflows/wf-x.yaml"}
	cfg := gateway.ServerConfig{
		Store: env.st, Auth: authSvc, Artifacts: env.art, Workflows: env.wf,
		Git: &fakeGitReader{files: map[string][]byte{}},
	}
	if steps != nil {
		cfg.StepAssigner = steps
	}
	env.ts = httptest.NewServer(gateway.NewServer(":0", cfg).Handler())
	t.Cleanup(env.ts.Close)
	return env
}

func (e *rbwEnv) do(t *testing.T, actor, method, path string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+e.tokens[actor])
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// written reports how many writes reached a service, and the branch of
// each (the workflow fake records only the last branch).
func (e *rbwEnv) written() (int, []string) {
	n := e.art.writes
	branches := append([]string(nil), e.art.branches...)
	if e.wf.lastCreateID != "" {
		n++
		branches = append(branches, e.wf.lastCreateBranch)
	}
	if e.wf.lastUpdateID != "" {
		n++
		branches = append(branches, e.wf.lastUpdateBranch)
	}
	return n, branches
}

// rbwSink is one API surface that writes a run branch.
type rbwSink struct {
	name       string
	authorRole domain.ActorRole
	taskPath   string
	add        bool // POST /artifacts/add keeps its own "draft step only" rule
	send       func(t *testing.T, e *rbwEnv, actor string) (int, string)
}

var rbwSinks = []rbwSink{
	{name: "POST /artifacts", authorRole: domain.RoleContributor, taskPath: "initiatives/INIT-001/initiative.md",
		send: func(t *testing.T, e *rbwEnv, actor string) (int, string) {
			return e.do(t, actor, "POST", "/api/v1/artifacts", map[string]any{
				"path": "initiatives/INIT-001/new.md", "content": rbwDoc,
				"write_context": map[string]string{"run_id": rbwRunID}})
		}},
	{name: "PUT /artifacts/*", authorRole: domain.RoleContributor, taskPath: "initiatives/INIT-001/initiative.md",
		send: func(t *testing.T, e *rbwEnv, actor string) (int, string) {
			return e.do(t, actor, "PUT", "/api/v1/artifacts/initiatives/INIT-001/notes.md", map[string]any{
				"content": rbwDoc, "write_context": map[string]string{"run_id": rbwRunID}})
		}},
	{name: "POST /workflows", authorRole: domain.RoleReviewer, taskPath: "workflows/wf-x.yaml",
		send: func(t *testing.T, e *rbwEnv, actor string) (int, string) {
			return e.do(t, actor, "POST", "/api/v1/workflows", map[string]any{
				"id": "wf-x", "body": "id: wf-x\n", "write_context": map[string]string{"run_id": rbwRunID}})
		}},
	{name: "PUT /workflows/{id}", authorRole: domain.RoleReviewer, taskPath: "workflows/wf-x.yaml",
		send: func(t *testing.T, e *rbwEnv, actor string) (int, string) {
			return e.do(t, actor, "PUT", "/api/v1/workflows/wf-x", map[string]any{
				"body": "id: wf-x\n", "write_context": map[string]string{"run_id": rbwRunID}})
		}},
	{name: "POST /artifacts/add", authorRole: domain.RoleContributor, taskPath: "initiatives/INIT-001/initiative.md", add: true,
		send: func(t *testing.T, e *rbwEnv, actor string) (int, string) {
			return e.do(t, actor, "POST", "/api/v1/artifacts/add", map[string]any{
				"run_id": rbwRunID, "artifact_type": "Epic", "parent": "INIT-001", "title": "Added"})
		}},
}

func rbwExec(stepID string, status domain.StepExecutionStatus, actorID string) domain.StepExecution {
	return domain.StepExecution{ExecutionID: rbwRunID + "-" + stepID + "-1", RunID: rbwRunID, StepID: stepID,
		Status: status, ActorID: actorID, Attempt: 1, CreatedAt: time.Now()}
}

// rbwRow is one run state and the outcome expected per caller.
type rbwRow struct {
	name   string
	cursor string
	execs  []domain.StepExecution
	steps  gateway.StepAssigner
	want   map[string]int // actor → status; 0 means "allowed" (2xx)
	// wantDetail: for these actors the refusal must carry the key in
	// errors[0].detail.
	wantDetail map[string]string
}

const allowed = 0

func TestAuthorizeRunBranchWrite_Matrix(t *testing.T) {
	rows := []rbwRow{
		{name: "assignee in progress", cursor: "draft",
			execs: []domain.StepExecution{rbwExec("draft", domain.StepStatusInProgress, rbwHolder)},
			want:  map[string]int{rbwHolder: allowed, rbwOther: 403, "op-1": allowed, "admin-1": allowed}},
		{name: "assignee assigned", cursor: "draft",
			execs: []domain.StepExecution{rbwExec("draft", domain.StepStatusAssigned, rbwHolder)},
			want:  map[string]int{rbwHolder: allowed, rbwOther: 403}},
		{name: "latest attempt wins", cursor: "draft",
			execs: []domain.StepExecution{
				rbwExec("draft", domain.StepStatusInProgress, rbwOther),
				{ExecutionID: rbwRunID + "-draft-2", RunID: rbwRunID, StepID: "draft", Status: domain.StepStatusInProgress, ActorID: rbwHolder, Attempt: 2},
			},
			want: map[string]int{rbwHolder: allowed, rbwOther: 403}},
		{name: "step waiting, unassigned", cursor: "draft",
			execs:      []domain.StepExecution{rbwExec("draft", domain.StepStatusWaiting, "")},
			want:       map[string]int{rbwHolder: 409, rbwOther: 409, "op-1": allowed},
			wantDetail: map[string]string{rbwHolder: "execution_id", rbwOther: "execution_id"}},
		{name: "open slot (assigned, no actor)", cursor: "draft",
			execs:      []domain.StepExecution{rbwExec("draft", domain.StepStatusAssigned, "")},
			want:       map[string]int{rbwOther: 409},
			wantDetail: map[string]string{rbwOther: "execution_id"}},
		{name: "waiting row still naming its holder (restart recovery)", cursor: "draft",
			execs:      []domain.StepExecution{rbwExec("draft", domain.StepStatusWaiting, rbwHolder)},
			want:       map[string]int{rbwHolder: 409},
			wantDetail: map[string]string{rbwHolder: "execution_id"}},
		{name: "no execution", cursor: "draft",
			want: map[string]int{rbwHolder: 409, "op-1": allowed}},
		{name: "no open execution", cursor: "draft",
			execs: []domain.StepExecution{rbwExec("draft", domain.StepStatusCompleted, rbwHolder)},
			want:  map[string]int{rbwHolder: 409}},
		{name: "review freeze", cursor: "review",
			execs:      []domain.StepExecution{rbwExec("draft", domain.StepStatusCompleted, rbwHolder), rbwExec("review", domain.StepStatusInProgress, rbwHolder)},
			want:       map[string]int{rbwHolder: 409, "op-1": 409, "admin-1": 409, rbwOther: 403},
			wantDetail: map[string]string{rbwHolder: "step_type", "op-1": "step_type", "admin-1": "step_type"}},
		{name: "internal-step freeze", cursor: "publish",
			execs:      []domain.StepExecution{rbwExec("publish", domain.StepStatusInProgress, "")},
			want:       map[string]int{"op-1": 409, "admin-1": 409, rbwOther: 409},
			wantDetail: map[string]string{"op-1": "step_type", "admin-1": "step_type"}},
		{name: "convergence step not frozen", cursor: "converge",
			execs: []domain.StepExecution{rbwExec("converge", domain.StepStatusInProgress, rbwHolder)},
			want:  map[string]int{rbwHolder: allowed, "op-1": allowed, rbwOther: 403}},
		{name: "empty cursor", cursor: "",
			want: map[string]int{"op-1": allowed, "admin-1": allowed, rbwHolder: 409}},
		{name: "divergence cursor", cursor: "divergence:run-a1b2-div-1",
			execs: []domain.StepExecution{rbwExec("draft", domain.StepStatusCompleted, rbwHolder)},
			want:  map[string]int{"op-1": allowed, rbwHolder: 409}},
		{name: "unresolvable step definition", cursor: "draft", steps: rbwSteps{types: map[string]domain.StepType{}},
			execs: []domain.StepExecution{rbwExec("draft", domain.StepStatusInProgress, rbwHolder)},
			want:  map[string]int{rbwHolder: 409, "op-1": 409, "admin-1": 409}},
	}

	for _, sink := range rbwSinks {
		for _, row := range rows {
			for actor, want := range row.want {
				sink, row, actor, want := sink, row, actor, want
				t.Run(sink.name+"/"+row.name+"/"+actor, func(t *testing.T) {
					t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
					// POST /artifacts/add keeps its pre-TASK-014 rule: the
					// run must be on its draft step (or have no cursor),
					// for every role.
					if sink.add && row.cursor != "draft" && row.cursor != "" {
						want = 409
					}
					steps := row.steps
					if steps == nil {
						steps = rbwStepTypes
					}
					run := &domain.Run{RunID: rbwRunID, Status: domain.RunStatusActive, Mode: domain.RunModePlanning,
						TaskPath: sink.taskPath, CurrentStepID: row.cursor, BranchName: rbwBranch}
					env := newRBWEnv(t, run, row.execs, steps, sink.authorRole)

					code, body := sink.send(t, env, actor)
					n, branches := env.written()
					if want == allowed {
						if code >= 300 {
							t.Fatalf("got %d %s, want 2xx", code, body)
						}
						if n != 1 || branches[0] != rbwBranch {
							t.Fatalf("allowed write: %d service writes on %v, want 1 on %s", n, branches, rbwBranch)
						}
						return
					}
					if code != want {
						t.Fatalf("got %d %s, want %d", code, body, want)
					}
					if n != 0 {
						t.Fatalf("refused write reached the service (%d writes on %v)", n, branches)
					}
					if want == 403 && actor != rbwHolder && strings.Contains(body, rbwHolder) {
						t.Fatalf("403 names the holder: %s", body)
					}
					if key := row.wantDetail[actor]; key != "" && !sink.add || key != "" && row.cursor == "draft" {
						var er gateway.ErrorResponse
						_ = json.Unmarshal([]byte(body), &er)
						detail, _ := er.Errors[0].Detail.(map[string]any)
						if detail[key] == nil || detail[key] == "" {
							t.Fatalf("refusal lacks detail.%s: %s", key, body)
						}
						if key == "step_type" && row.cursor == "publish" && detail[key] != string(domain.StepTypeInternal) {
							t.Fatalf("internal freeze reports step_type %v", detail[key])
						}
					}
				})
			}
		}
	}
}

// No step assigner at all (an engine-less server with the store fallback
// disabled) fails closed for every role: 503.
func TestAuthorizeRunBranchWrite_NoAssigner503(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	for _, actor := range []string{rbwHolder, "op-1"} {
		run := &domain.Run{RunID: rbwRunID, Status: domain.RunStatusActive, Mode: domain.RunModePlanning,
			TaskPath: "initiatives/INIT-001/initiative.md", CurrentStepID: "draft", BranchName: rbwBranch}
		base := newFakeStore()
		for id, role := range map[string]domain.ActorRole{rbwHolder: domain.RoleContributor, "op-1": domain.RoleOperator} {
			base.actors[id] = &domain.Actor{ActorID: id, Name: id, Type: domain.ActorTypeHuman, Role: role, Status: domain.ActorStatusActive}
		}
		authSvc := auth.NewService(base)
		tok, _, _ := authSvc.CreateToken(context.Background(), actor, "t", nil)
		art := &recArtSvc{fakeArtifactService: newFakeArtifactService()}
		ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{
			Store: &rbwStore{fakeStore: base, run: run, execs: []domain.StepExecution{rbwExec("draft", domain.StepStatusInProgress, rbwHolder)}},
			Auth:  authSvc, Artifacts: art, DisableStepAssignFallback: true,
		}).Handler())
		b, _ := json.Marshal(map[string]any{"path": "initiatives/INIT-001/new.md", "content": rbwDoc,
			"write_context": map[string]string{"run_id": rbwRunID}})
		resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/artifacts", tok, string(b))
		resp.Body.Close()
		ts.Close()
		if resp.StatusCode != http.StatusServiceUnavailable || art.writes != 0 {
			t.Fatalf("%s with no step assigner: %d (writes %d), want 503", actor, resp.StatusCode, art.writes)
		}
	}
}

// A run whose branch is not an engine run branch (a legacy or hand-set
// name refadmit does not resolve back to the run) is operator-only.
func TestAuthorizeRunBranchWrite_NonEngineBranchOperatorOnly(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	for _, branch := range []string{"feature/legacy", "spine/plan/thing-ffff", "spine/odd"} {
		for actor, want := range map[string]int{rbwHolder: 409, "op-1": allowed} {
			run := &domain.Run{RunID: rbwRunID, Status: domain.RunStatusActive, Mode: domain.RunModePlanning,
				TaskPath: "initiatives/INIT-001/initiative.md", CurrentStepID: "draft", BranchName: branch}
			env := newRBWEnv(t, run, []domain.StepExecution{rbwExec("draft", domain.StepStatusInProgress, rbwHolder)}, rbwStepTypes, domain.RoleContributor)
			code, body := rbwSinks[0].send(t, env, actor)
			if want == allowed && code >= 300 || want != allowed && code != want {
				t.Fatalf("branch %s, %s: %d %s, want %d", branch, actor, code, body, want)
			}
		}
	}
}

// AC 9 / I7: a run that governs a workflow definition accepts no artifact
// writes — write_context on the artifact endpoints or /artifacts/add — for
// any role, and nothing reaches the service.
func TestWorkflowRun_AcceptsNoArtifactWrites(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	for _, sink := range []rbwSink{rbwSinks[0], rbwSinks[1], rbwSinks[4]} {
		for _, actor := range []string{rbwHolder, "op-1", "admin-1"} {
			run := &domain.Run{RunID: rbwRunID, Status: domain.RunStatusActive, Mode: domain.RunModePlanning,
				TaskPath: "workflows/wf-x.yaml", CurrentStepID: "draft", BranchName: rbwBranch}
			env := newRBWEnv(t, run, []domain.StepExecution{rbwExec("draft", domain.StepStatusInProgress, rbwHolder)}, rbwStepTypes, domain.RoleContributor)
			code, body := sink.send(t, env, actor)
			if code != http.StatusBadRequest || !strings.Contains(body, "governs a workflow definition") {
				t.Fatalf("%s by %s on a workflow run: %d %s, want 400", sink.name, actor, code, body)
			}
			if n, _ := env.written(); n != 0 {
				t.Fatalf("%s by %s: a refused write reached the service", sink.name, actor)
			}
		}
	}
	// Any spelling of the workflow path counts (canonical guard).
	run := &domain.Run{RunID: rbwRunID, Status: domain.RunStatusActive, Mode: domain.RunModePlanning,
		TaskPath: "./workflows/wf-x.yaml", CurrentStepID: "draft", BranchName: rbwBranch}
	env := newRBWEnv(t, run, []domain.StepExecution{rbwExec("draft", domain.StepStatusInProgress, rbwHolder)}, rbwStepTypes, domain.RoleContributor)
	if code, body := rbwSinks[0].send(t, env, rbwHolder); code != http.StatusBadRequest {
		t.Fatalf("dot-segment workflow task path: %d %s, want 400", code, body)
	}
}

// ── AC 7: workflow write_context (V1, V2) ──

func TestWorkflowWriteContext_Rules(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	lifecycle := func(taskPath string, mode domain.RunMode) *domain.Run {
		return &domain.Run{RunID: rbwRunID, Status: domain.RunStatusActive, Mode: mode,
			TaskPath: taskPath, CurrentStepID: "draft", BranchName: rbwBranch}
	}
	held := []domain.StepExecution{rbwExec("draft", domain.StepStatusInProgress, rbwHolder)}

	type call struct {
		method, path string
		body         map[string]any
	}
	createWith := func(wc map[string]any) call {
		return call{"POST", "/api/v1/workflows", map[string]any{"id": "wf-x", "body": "id: wf-x\n", "write_context": wc}}
	}
	updateWith := func(wc map[string]any) call {
		return call{"PUT", "/api/v1/workflows/wf-x", map[string]any{"body": "id: wf-x\n", "write_context": wc}}
	}

	cases := []struct {
		name  string
		run   *domain.Run
		actor string
		calls []call
		want  int
		text  string
	}{
		{"empty write_context, reviewer", lifecycle("workflows/wf-x.yaml", domain.RunModePlanning), rbwHolder,
			[]call{createWith(map[string]any{}), updateWith(map[string]any{})}, 400, "write_context.run_id is required"},
		{"empty write_context, operator", lifecycle("workflows/wf-x.yaml", domain.RunModePlanning), "op-1",
			[]call{createWith(map[string]any{}), updateWith(map[string]any{})}, 400, "write_context.run_id is required"},
		{"task_path only, operator", lifecycle("workflows/wf-x.yaml", domain.RunModePlanning), "op-1",
			[]call{createWith(map[string]any{"task_path": "workflows/wf-x.yaml"})}, 400, "write_context.run_id is required"},
		{"override", lifecycle("workflows/wf-x.yaml", domain.RunModePlanning), "op-1",
			[]call{createWith(map[string]any{"run_id": rbwRunID, "override": true}), updateWith(map[string]any{"override": true})}, 400, "override is not supported"},
		{"another workflow's lifecycle run", lifecycle("workflows/other.yaml", domain.RunModePlanning), rbwHolder,
			[]call{createWith(map[string]any{"run_id": rbwRunID}), updateWith(map[string]any{"run_id": rbwRunID})}, 400, "does not govern workflows/wf-x.yaml"},
		{"an artifact planning run", lifecycle("initiatives/INIT-001/initiative.md", domain.RunModePlanning), rbwHolder,
			[]call{createWith(map[string]any{"run_id": rbwRunID})}, 400, "does not govern"},
		{"a standard run whose task_path is the workflow", lifecycle("workflows/wf-x.yaml", domain.RunModeStandard), rbwHolder,
			[]call{updateWith(map[string]any{"run_id": rbwRunID, "task_path": "workflows/wf-x.yaml"})}, 400, "does not govern"},
		{"the lifecycle run, operator not exempt from the run check", lifecycle("workflows/other.yaml", domain.RunModePlanning), "op-1",
			[]call{updateWith(map[string]any{"run_id": rbwRunID})}, 400, "does not govern"},
		{"the lifecycle run, its draft assignee", lifecycle("workflows/wf-x.yaml", domain.RunModePlanning), rbwHolder,
			[]call{createWith(map[string]any{"run_id": rbwRunID}), updateWith(map[string]any{"run_id": rbwRunID})}, 0, ""},
	}
	for _, c := range cases {
		for _, cl := range c.calls {
			t.Run(c.name+"/"+cl.method, func(t *testing.T) {
				env := newRBWEnv(t, c.run, held, rbwStepTypes, domain.RoleReviewer)
				code, body := env.do(t, c.actor, cl.method, cl.path, cl.body)
				n, branches := env.written()
				if c.want == 0 {
					if code >= 300 || !strings.Contains(body, `"write_mode":"proposed"`) {
						t.Fatalf("got %d %s, want 2xx proposed", code, body)
					}
					if n != 1 || branches[0] != rbwBranch {
						t.Fatalf("write landed on %v, want %s", branches, rbwBranch)
					}
					return
				}
				if code != c.want || !strings.Contains(body, c.text) {
					t.Fatalf("got %d %s, want %d containing %q", code, body, c.want, c.text)
				}
				if n != 0 || env.wf.lastCreateBypass || env.wf.lastUpdateBypass {
					t.Fatalf("refused workflow write reached the service (%d writes)", n)
				}
			})
		}
	}
}

// ── AC 6: the actionable 409 ──

func TestWorkflow_OpenLifecycleRun_Returns409(t *testing.T) {
	open := domain.Run{RunID: "run-open1", TaskPath: "workflows/wf-x.yaml", Mode: domain.RunModePlanning,
		Status: domain.RunStatusActive, BranchName: "spine/plan/wf-x-open1", CurrentStepID: "draft"}
	for _, op := range []string{"POST", "PUT"} {
		for _, status := range []domain.RunStatus{domain.RunStatusActive, domain.RunStatusPending, domain.RunStatusPaused, domain.RunStatusCommitting} {
			t.Run(op+"/"+string(status), func(t *testing.T) {
				r := open
				r.Status = status
				env, planner := newLifecycleEnv(t, []domain.Run{r}, nil)
				code, body := sendLifecycle(t, env, op)
				if code != http.StatusConflict {
					t.Fatalf("got %d %s, want 409", code, body)
				}
				var er gateway.ErrorResponse
				_ = json.Unmarshal([]byte(body), &er)
				detail, _ := er.Errors[0].Detail.(map[string]any)
				if detail["run_id"] != "run-open1" || detail["branch_name"] != "spine/plan/wf-x-open1" || detail["status"] != string(status) {
					t.Fatalf("409 detail = %v", detail)
				}
				if planner.lastID != "" || planner.lastUpdateID != "" {
					t.Fatal("the starter must not be called while a lifecycle run is open")
				}
			})
		}
	}
}

// A terminal run, a standard run or a run on another workflow does not
// block; a failed start leaves its record failed (engine, design-check L1),
// so it never locks the workflow (TestWorkflowUpdate_FailedRunDoesNotBlock).
func TestWorkflow_TerminalOrForeignRunsDoNotBlock(t *testing.T) {
	runs := []domain.Run{
		{RunID: "run-c", TaskPath: "workflows/wf-x.yaml", Mode: domain.RunModePlanning, Status: domain.RunStatusCompleted},
		{RunID: "run-f", TaskPath: "workflows/wf-x.yaml", Mode: domain.RunModePlanning, Status: domain.RunStatusFailed},
		{RunID: "run-x", TaskPath: "workflows/wf-x.yaml", Mode: domain.RunModePlanning, Status: domain.RunStatusCancelled},
		{RunID: "run-s", TaskPath: "workflows/wf-x.yaml", Mode: domain.RunModeStandard, Status: domain.RunStatusActive},
		{RunID: "run-o", TaskPath: "workflows/other.yaml", Mode: domain.RunModePlanning, Status: domain.RunStatusActive},
	}
	for _, op := range []string{"POST", "PUT"} {
		env, planner := newLifecycleEnv(t, runs, nil)
		code, body := sendLifecycle(t, env, op)
		if code != http.StatusCreated {
			t.Fatalf("%s: got %d %s, want 201", op, code, body)
		}
		if op == "POST" && planner.lastID != "wf-x" || op == "PUT" && planner.lastUpdateID != "wf-x" {
			t.Fatalf("%s: starter not called", op)
		}
	}
}

func TestWorkflowUpdate_FailedRunDoesNotBlock(t *testing.T) {
	failed := domain.Run{RunID: "run-f1", TaskPath: "workflows/wf-x.yaml", Mode: domain.RunModePlanning, Status: domain.RunStatusFailed}
	env, planner := newLifecycleEnv(t, []domain.Run{failed}, nil)
	if code, body := sendLifecycle(t, env, "PUT"); code != http.StatusCreated || planner.lastUpdateID != "wf-x" {
		t.Fatalf("reviewer PUT after a failed start: %d %s (update starter id %q), want 201 through the starter", code, body, planner.lastUpdateID)
	}
}

func TestWorkflow_OpenRunCheckFailure_Returns503(t *testing.T) {
	for _, op := range []string{"POST", "PUT"} {
		env, planner := newLifecycleEnv(t, nil, domain.NewError(domain.ErrInternal, "db down at 10.0.0.7"))
		code, body := sendLifecycle(t, env, op)
		if code != http.StatusServiceUnavailable || strings.Contains(body, "10.0.0.7") {
			t.Fatalf("%s: got %d %s, want 503 without the store error text", op, code, body)
		}
		if planner.lastID != "" || planner.lastUpdateID != "" {
			t.Fatalf("%s: starter called despite the failed check", op)
		}
	}
}

func newLifecycleEnv(t *testing.T, runs []domain.Run, listErr error) (*rbwEnv, *fakeWFPlanningStarter) {
	t.Helper()
	base := newFakeStore()
	base.actors["rev-1"] = &domain.Actor{ActorID: "rev-1", Name: "rev", Type: domain.ActorTypeHuman, Role: domain.RoleReviewer, Status: domain.ActorStatusActive}
	authSvc := auth.NewService(base)
	tok, _, _ := authSvc.CreateToken(context.Background(), "rev-1", "t", nil)
	env := &rbwEnv{st: &rbwStore{fakeStore: base, runsByTask: runs, listRunsErr: listErr}, wf: newFakeWorkflowSvc(),
		art: &recArtSvc{fakeArtifactService: newFakeArtifactService()}, tokens: map[string]string{"rev-1": tok}}
	planner := &fakeWFPlanningStarter{}
	env.ts = httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{
		Store: env.st, Auth: authSvc, Workflows: env.wf, WFPlanningStarter: planner,
	}).Handler())
	t.Cleanup(env.ts.Close)
	return env, planner
}

func sendLifecycle(t *testing.T, env *rbwEnv, op string) (int, string) {
	t.Helper()
	if op == "POST" {
		return env.do(t, "rev-1", "POST", "/api/v1/workflows", map[string]any{"id": "wf-x", "body": "id: wf-x\n"})
	}
	return env.do(t, "rev-1", "PUT", "/api/v1/workflows/wf-x", map[string]any{"body": "id: wf-x\n"})
}

// ── AC 4: the update dispatch covers every role ──

func TestWorkflowUpdate_RoleDispatchTable(t *testing.T) {
	type want struct {
		status     int
		plannerHit bool
		svcHit     bool
		bypassCtx  bool
	}
	cases := []struct {
		role domain.ActorRole
		want want
	}{
		{domain.RoleReader, want{status: http.StatusForbidden}},
		{domain.RoleContributor, want{status: http.StatusForbidden}},
		{domain.RoleReviewer, want{status: http.StatusCreated, plannerHit: true}},
		{domain.RoleOperator, want{status: http.StatusOK, svcHit: true, bypassCtx: true}},
		{domain.RoleAdmin, want{status: http.StatusOK, svcHit: true, bypassCtx: true}},
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			svc := newFakeWorkflowSvc()
			svc.stored["role-test"] = &domain.WorkflowDefinition{ID: "role-test", Version: "1.0", Path: "workflows/role-test.yaml"}
			planner := &fakeWFPlanningStarter{}
			ts, tok := newWorkflowServerWithPlanning(t, svc, planner, tc.role)
			defer ts.Close()

			resp := doWorkflowJSON(t, "PUT", ts.URL+"/api/v1/workflows/role-test", tok, `{"body":"id: role-test\n"}`)
			var out map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()

			if resp.StatusCode != tc.want.status {
				t.Fatalf("status: got %d, want %d", resp.StatusCode, tc.want.status)
			}
			if tc.want.plannerHit != (planner.lastUpdateID == "role-test") || planner.lastID != "" {
				t.Errorf("update starter: id=%q (create id=%q), want hit=%v", planner.lastUpdateID, planner.lastID, tc.want.plannerHit)
			}
			if tc.want.svcHit != (svc.lastUpdateID == "role-test") {
				t.Errorf("svc.Update: id=%q, want hit=%v", svc.lastUpdateID, tc.want.svcHit)
			}
			if svc.lastUpdateBypass != tc.want.bypassCtx {
				t.Errorf("bypass ctx: got %v, want %v", svc.lastUpdateBypass, tc.want.bypassCtx)
			}
			switch {
			case tc.want.plannerHit && out["write_mode"] != "planning":
				t.Errorf("reviewer write_mode = %v, want planning", out["write_mode"])
			case tc.want.svcHit && out["write_mode"] != "bypass":
				t.Errorf("operator write_mode = %v, want bypass", out["write_mode"])
			}
		})
	}
}

func TestWorkflowUpdate_ReviewerNoPlanner_Returns503(t *testing.T) {
	svc := newFakeWorkflowSvc()
	svc.stored["x"] = &domain.WorkflowDefinition{ID: "x", Version: "1.0", Path: "workflows/x.yaml"}
	ts, tok := newWorkflowServer(t, svc, domain.RoleReviewer)
	defer ts.Close()

	resp := doWorkflowJSON(t, "PUT", ts.URL+"/api/v1/workflows/x", tok, `{"body":"id: x\n"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when starter is nil, got %d", resp.StatusCode)
	}
	if svc.lastUpdateID != "" {
		t.Errorf("direct svc.Update must not run when the governance path is unavailable, got id=%q", svc.lastUpdateID)
	}
}

// An invalid id is refused before the open-run check and the starter.
func TestWorkflowLifecycle_InvalidIDRefused(t *testing.T) {
	env, planner := newLifecycleEnv(t, nil, nil)
	code, body := env.do(t, "rev-1", "POST", "/api/v1/workflows", map[string]any{"id": "../x", "body": "id: x\n"})
	if code != http.StatusBadRequest || planner.lastID != "" {
		t.Fatalf("got %d %s (starter %q), want 400 with no start", code, body, planner.lastID)
	}
}

// workflow.FilePath is the run.TaskPath key the gateway matches on.
var _ = workflow.FilePath
