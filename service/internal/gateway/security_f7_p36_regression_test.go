package gateway_test

// INIT-023 TASK-014 regression tests: the 2026-10-06 audit's F7 and P3-6
// proofs of concept, inverted (AC 10). Only symbols that exist at Spine
// 61994fe are used, so this file compiles against the pre-fix tree, where
// every F7/P3-6 test FAILS; the two guards (operator bypass, assignee
// write) pass before and after.
//
// Fixture note: a run's branch name carries its run ID's suffix
// (spine/run/task-001-alice belongs to run-alice), as every engine-made
// branch does — the run-branch write gate resolves the branch back to its
// run through it (core/refadmit).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/service/internal/gateway"
)

// ── shared helpers ──

func secGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir,
		"GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=alice@x", "GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=alice@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func secDo(t *testing.T, method, url, tok string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp := doWorkflowJSON(t, method, url, tok, string(b))
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(out))
}

// repoWorkflowsDir locates the repo's seeded workflows/ (no absolute paths).
func repoWorkflowsDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "workflows")
}

// secPlanner records create and update planning-run starts. It also carries
// StartWorkflowUpdatePlanningRun so it satisfies the post-fix interface;
// at 61994fe the extra method is simply unused.
type secPlanner struct{ createHits, updateHits int }

func (p *secPlanner) StartWorkflowPlanningRun(_ context.Context, id, _ string) (*gateway.PlanningRunResult, error) {
	p.createHits++
	return &gateway.PlanningRunResult{RunID: "run-plan-c", TaskPath: "workflows/" + id + ".yaml", Mode: "planning", Status: "active", BranchName: "spine/plan/c"}, nil
}

func (p *secPlanner) StartWorkflowUpdatePlanningRun(_ context.Context, id, _ string) (*gateway.PlanningRunResult, error) {
	p.updateHits++
	return &gateway.PlanningRunResult{RunID: "run-plan-u", TaskPath: "workflows/" + id + ".yaml", Mode: "planning", Status: "active", BranchName: "spine/plan/u"}, nil
}

// secStepDefs is a StepAssigner whose LookupStepDef reports step types by id.
type secStepDefs struct{ types map[string]domain.StepType }

func (s secStepDefs) AssignStep(context.Context, engine.AssignRequest) (*engine.AssignResult, error) {
	return nil, domain.NewError(domain.ErrUnavailable, "not used")
}
func (s secStepDefs) LookupStepDef(_ context.Context, _, stepID string) (*domain.StepDefinition, *domain.Run) {
	t, ok := s.types[stepID]
	if !ok {
		return nil, nil
	}
	return &domain.StepDefinition{ID: stepID, Type: t}, nil
}

var secSteps = secStepDefs{types: map[string]domain.StepType{
	"draft": domain.StepTypeManual, "execute": domain.StepTypeManual, "review": domain.StepTypeReview,
}}

// secStore serves one configurable run plus its step executions.
type secStore struct {
	*fakeStore
	run   *domain.Run
	execs []domain.StepExecution
}

func (s *secStore) GetRun(_ context.Context, id string) (*domain.Run, error) {
	r := *s.run
	r.RunID = id
	return &r, nil
}
func (s *secStore) ListStepExecutionsByRun(_ context.Context, _ string) ([]domain.StepExecution, error) {
	return append([]domain.StepExecution(nil), s.execs...), nil
}
func (s *secStore) GetStepExecution(_ context.Context, id string) (*domain.StepExecution, error) {
	for i := range s.execs {
		if s.execs[i].ExecutionID == id {
			e := s.execs[i]
			return &e, nil
		}
	}
	return nil, domain.NewError(domain.ErrNotFound, "exec not found")
}
func (s *secStore) TransitionRunStatus(_ context.Context, _ string, from, to domain.RunStatus) (bool, error) {
	if s.run.Status != from {
		return false, nil
	}
	s.run.Status = to
	return true, nil
}

func secActor(fs *fakeStore, id string, role domain.ActorRole) {
	fs.actors[id] = &domain.Actor{ActorID: id, Name: id, Type: domain.ActorTypeHuman, Role: role, Status: domain.ActorStatusActive}
}

func secToken(t *testing.T, a *auth.Service, id string) string {
	t.Helper()
	tok, _, err := a.CreateToken(context.Background(), id, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// seedWorkflowRepo makes a repo whose main holds workflows/<name>.yaml
// copied from the repo's seeded workflows (timeouts without a
// timeout_outcome are dropped so the body validates).
func seedWorkflowRepo(t *testing.T, names ...string) (string, map[string]string) {
	t.Helper()
	repo := t.TempDir()
	secGit(t, repo, "init", "-q", "-b", "main")
	_ = os.MkdirAll(filepath.Join(repo, "workflows"), 0o755)
	bodies := map[string]string{}
	for _, n := range names {
		raw, err := os.ReadFile(filepath.Join(repoWorkflowsDir(t), n+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		b := strings.Replace(strings.Replace(string(raw), "    timeout: \"4h\"\n", "", 1), "    timeout: \"24h\"\n", "", 1)
		bodies[n] = b
		_ = os.WriteFile(filepath.Join(repo, "workflows", n+".yaml"), []byte(b), 0o644) //nolint:gosec // G703: test fixture path under t.TempDir(), not external input
	}
	secGit(t, repo, "add", ".")
	secGit(t, repo, "commit", "-q", "-m", "seed")
	return repo, bodies
}

// ── F7: reviewer workflow.update goes through governance ──

// Inverted poc/standalone-service TestPOC_ReviewerDirectWorkflowUpdateOnMain.
func TestF7_ReviewerUpdate_RoutedToPlanningRun_MainUntouched(t *testing.T) {
	repo, bodies := seedWorkflowRepo(t, "task-default")
	before := secGit(t, repo, "rev-parse", "main")

	fs := newFakeStore()
	secActor(fs, "rev-1", domain.RoleReviewer)
	authSvc := auth.NewService(fs)
	tok := secToken(t, authSvc, "rev-1")
	planner := &secPlanner{}
	wfSvc := workflow.NewService(git.NewCLIClient(repo), repo)
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, Workflows: wfSvc, WFPlanningStarter: planner}).Handler())
	defer ts.Close()

	body := strings.Replace(bodies["task-default"], `version: "1.0"`, `version: "1.1"`, 1)
	body = strings.Replace(body, "required_skills: [review]", "required_skills: [execution]", 1)
	code, out := secDo(t, "PUT", ts.URL+"/api/v1/workflows/task-default", tok, map[string]string{"body": body})

	if code != http.StatusCreated || !strings.Contains(out, `"write_mode":"planning"`) {
		t.Fatalf("reviewer PUT without write_context: got %d %s, want 201 write_mode=planning", code, out)
	}
	if planner.updateHits != 1 || planner.createHits != 0 {
		t.Fatalf("planner hits: update=%d create=%d, want 1/0", planner.updateHits, planner.createHits)
	}
	if after := secGit(t, repo, "rev-parse", "main"); after != before {
		t.Fatalf("main moved %s -> %s; a reviewer update must not commit to the authoritative branch", before, after)
	}
}

// Inverted poc/verify-r1-5 TestR15_ReviewerRewritesLifecycleOnMain.
func TestF7_ReviewerCannotRewriteLifecycleOnMain(t *testing.T) {
	repo, bodies := seedWorkflowRepo(t, "workflow-lifecycle")
	before := secGit(t, repo, "rev-parse", "main")

	fs := newFakeStore()
	secActor(fs, "rev-1", domain.RoleReviewer)
	authSvc := auth.NewService(fs)
	tok := secToken(t, authSvc, "rev-1")
	planner := &secPlanner{}
	wfSvc := workflow.NewService(git.NewCLIClient(repo), repo)
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, Workflows: wfSvc, WFPlanningStarter: planner}).Handler())
	defer ts.Close()

	body := strings.Replace(bodies["workflow-lifecycle"], `version: "1.0"`, `version: "1.1"`, 1)
	body = strings.Replace(body, "      mode: human_only\n      eligible_actor_types:\n        - human\n      required_skills: [workflow_review]",
		"      mode: hybrid\n      eligible_actor_types:\n        - human\n        - ai_agent\n      required_skills: [workflow_authoring]", 1)
	code, out := secDo(t, "PUT", ts.URL+"/api/v1/workflows/workflow-lifecycle", tok, map[string]string{"body": body})
	if code != http.StatusCreated || planner.updateHits != 1 {
		t.Fatalf("reviewer PUT workflow-lifecycle: %d %s (update planning hits=%d), want 201 + 1 hit", code, out, planner.updateHits)
	}
	after := secGit(t, repo, "rev-parse", "main")
	onMain := secGit(t, repo, "show", "main:workflows/workflow-lifecycle.yaml")
	if after != before || !strings.Contains(onMain, "workflow_review") {
		t.Fatalf("lifecycle on main changed (before %s after %s); workflow_review gate present=%v", before, after, strings.Contains(onMain, "workflow_review"))
	}

	// Control kept from the PoC: create is still routed to a planning run.
	cbody := strings.Replace(bodies["workflow-lifecycle"], "id: workflow-lifecycle", "id: wf-new", 1)
	code, out = secDo(t, "POST", ts.URL+"/api/v1/workflows", tok, map[string]string{"id": "wf-new", "body": cbody})
	if code != http.StatusCreated || planner.createHits != 1 {
		t.Fatalf("control create: %d %s (create hits=%d)", code, out, planner.createHits)
	}
}

// Variant found in design: an EMPTY write_context ({}) skipped the reviewer
// dispatch and committed to main on both create and update.
func TestF7_EmptyWriteContext_RefusedOnWorkflowEndpoints(t *testing.T) {
	repo, bodies := seedWorkflowRepo(t, "workflow-lifecycle")
	before := secGit(t, repo, "rev-parse", "main")

	fs := newFakeStore()
	secActor(fs, "rev-1", domain.RoleReviewer)
	authSvc := auth.NewService(fs)
	tok := secToken(t, authSvc, "rev-1")
	planner := &secPlanner{}
	wfSvc := workflow.NewService(git.NewCLIClient(repo), repo)
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, Workflows: wfSvc, WFPlanningStarter: planner}).Handler())
	defer ts.Close()

	ub := strings.Replace(bodies["workflow-lifecycle"], `version: "1.0"`, `version: "1.1"`, 1)
	if code, out := secDo(t, "PUT", ts.URL+"/api/v1/workflows/workflow-lifecycle", tok,
		map[string]any{"body": ub, "write_context": map[string]string{}}); code != http.StatusBadRequest {
		t.Errorf("PUT with write_context {}: %d %s, want 400", code, out)
	}
	cb := strings.Replace(bodies["workflow-lifecycle"], "id: workflow-lifecycle", "id: wf-sneaky", 1)
	if code, out := secDo(t, "POST", ts.URL+"/api/v1/workflows", tok,
		map[string]any{"id": "wf-sneaky", "body": cb, "write_context": map[string]string{}}); code != http.StatusBadRequest {
		t.Errorf("POST with write_context {}: %d %s, want 400", code, out)
	}
	if after := secGit(t, repo, "rev-parse", "main"); after != before {
		t.Fatalf("main moved %s -> %s", before, after)
	}
}

// A workflow write_context must name the lifecycle run governing THAT workflow.
func TestF7_WorkflowWriteContext_OtherWorkflowsRun_Refused(t *testing.T) {
	repo, bodies := seedWorkflowRepo(t, "workflow-lifecycle")
	secGit(t, repo, "branch", "spine/plan/a-runa")
	tip := secGit(t, repo, "rev-parse", "spine/plan/a-runa")

	base := newFakeStore()
	secActor(base, "rev-1", domain.RoleReviewer)
	fs := &secStore{fakeStore: base,
		run: &domain.Run{Status: domain.RunStatusActive, Mode: domain.RunModePlanning, CurrentStepID: "draft",
			TaskPath: "workflows/a.yaml", BranchName: "spine/plan/a-runa"},
		execs: []domain.StepExecution{{ExecutionID: "e-draft", StepID: "draft", Status: domain.StepStatusInProgress, ActorID: "rev-1", Attempt: 1}},
	}
	authSvc := auth.NewService(base)
	tok := secToken(t, authSvc, "rev-1")
	wfSvc := workflow.NewService(git.NewCLIClient(repo), repo)
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, Workflows: wfSvc,
		WFPlanningStarter: &secPlanner{}, StepAssigner: secSteps}).Handler())
	defer ts.Close()

	ob := strings.Replace(bodies["workflow-lifecycle"], "id: workflow-lifecycle", "id: other-flow", 1)
	code, out := secDo(t, "POST", ts.URL+"/api/v1/workflows", tok,
		map[string]any{"id": "other-flow", "body": ob, "write_context": map[string]string{"run_id": "run-runa"}})
	if code != http.StatusBadRequest {
		t.Fatalf("other-flow onto run governing workflows/a.yaml: %d %s, want 400", code, out)
	}
	if now := secGit(t, repo, "rev-parse", "spine/plan/a-runa"); now != tip {
		t.Fatalf("run branch moved %s -> %s", tip, now)
	}
}

// Guard (passes before and after): operators keep the ADR-008 bypass.
func TestF7_OperatorUpdate_KeepsBypassTrailer(t *testing.T) {
	repo, bodies := seedWorkflowRepo(t, "task-default")
	fs := newFakeStore()
	secActor(fs, "op-1", domain.RoleOperator)
	authSvc := auth.NewService(fs)
	tok := secToken(t, authSvc, "op-1")
	planner := &secPlanner{}
	wfSvc := workflow.NewService(git.NewCLIClient(repo), repo)
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, Workflows: wfSvc, WFPlanningStarter: planner}).Handler())
	defer ts.Close()

	body := strings.Replace(bodies["task-default"], `version: "1.0"`, `version: "1.1"`, 1)
	code, out := secDo(t, "PUT", ts.URL+"/api/v1/workflows/task-default", tok, map[string]string{"body": body})
	if code != http.StatusOK || !strings.Contains(out, `"write_mode":"bypass"`) {
		t.Fatalf("operator PUT: %d %s, want 200 write_mode=bypass", code, out)
	}
	if msg := secGit(t, repo, "log", "-1", "--format=%B", "main"); !strings.Contains(msg, "Workflow-Bypass: true") {
		t.Fatalf("bypass commit lacks Workflow-Bypass trailer:\n%s", msg)
	}
	if planner.updateHits+planner.createHits != 0 {
		t.Fatalf("operator update must not open a planning run")
	}
}

// ── P3-6: run-branch writes limited to the current step's assignee ──

const secTask = "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001-x.md"
const secBranch = "spine/run/task-001-alice"

const secCharter = "---\ntype: Governance\ntitle: Charter\nstatus: Living Document\n---\n\n# Charter\n\nAll changes require two reviewers.\n"
const secInjected = "---\ntype: Governance\ntitle: Charter\nstatus: Living Document\n---\n\n# Charter\n\nMALLORY: reviews are optional.\n"

func seedRunRepo(t *testing.T) (repo, reviewedSHA string) {
	t.Helper()
	repo = t.TempDir()
	secGit(t, repo, "init", "-q", "-b", "main")
	_ = os.MkdirAll(filepath.Join(repo, "governance"), 0o755)
	_ = os.MkdirAll(filepath.Join(repo, filepath.Dir(secTask)), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "governance/charter.md"), []byte(secCharter), 0o644)
	_ = os.WriteFile(filepath.Join(repo, secTask), []byte("task v1\n"), 0o644)
	secGit(t, repo, "add", ".")
	secGit(t, repo, "commit", "-q", "-m", "seed")
	secGit(t, repo, "checkout", "-q", "-b", secBranch)
	_ = os.WriteFile(filepath.Join(repo, secTask), []byte("task v2 (alice work)\n"), 0o644)
	secGit(t, repo, "commit", "-q", "-am", "alice: implement task")
	reviewedSHA = secGit(t, repo, "rev-parse", secBranch)
	secGit(t, repo, "checkout", "-q", "main")
	return repo, reviewedSHA
}

func secRunServer(t *testing.T, repo, step string, execs []domain.StepExecution) (*secStore, *auth.Service, *httptest.Server) {
	t.Helper()
	base := newFakeStore()
	secActor(base, "alice", domain.RoleContributor)
	secActor(base, "mallory", domain.RoleContributor)
	secActor(base, "bob", domain.RoleReviewer)
	secActor(base, "op-1", domain.RoleOperator)
	fs := &secStore{fakeStore: base, execs: execs,
		run: &domain.Run{Status: domain.RunStatusActive, Mode: domain.RunModeStandard, TaskPath: secTask,
			CurrentStepID: step, BranchName: secBranch, TraceID: "trace-0123456789abcdef"}}
	authSvc := auth.NewService(base)
	art := artifact.NewService(git.NewCLIClient(repo), nil, repo)
	art.WithPolicy(branchprotect.New(branchprotect.StaticRules(nil))) // bootstrap: main no-direct-write
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, Artifacts: art, StepAssigner: secSteps}).Handler())
	t.Cleanup(ts.Close)
	return fs, authSvc, ts
}

// Inverted poc/standalone-service TestPOC_ContributorWritesIntoForeignRunBranch.
func TestP36_UnassignedContributor_CannotWriteForeignRunBranch(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	repo, reviewed := seedRunRepo(t)
	_, authSvc, ts := secRunServer(t, repo, "execute", []domain.StepExecution{
		{ExecutionID: "e-exec", StepID: "execute", Status: domain.StepStatusInProgress, ActorID: "alice", Attempt: 1}})
	tok := secToken(t, authSvc, "mallory")

	code, out := secDo(t, "POST", ts.URL+"/api/v1/artifacts", tok, map[string]any{
		"path": "governance/injected.md", "content": secInjected,
		"write_context": map[string]string{"run_id": "run-alice", "task_path": secTask}})
	if code != http.StatusForbidden {
		t.Fatalf("mallory write into alice's run: %d %s, want 403", code, out)
	}
	if head := secGit(t, repo, "rev-parse", secBranch); head != reviewed {
		t.Fatalf("alice's branch moved %s -> %s", reviewed, head)
	}
}

// Guard (passes before and after): the assignee of the current step writes.
func TestP36_Assignee_CanWriteOwnRunBranch(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	repo, reviewed := seedRunRepo(t)
	_, authSvc, ts := secRunServer(t, repo, "execute", []domain.StepExecution{
		{ExecutionID: "e-exec", StepID: "execute", Status: domain.StepStatusInProgress, ActorID: "alice", Attempt: 1}})
	tok := secToken(t, authSvc, "alice")

	code, out := secDo(t, "POST", ts.URL+"/api/v1/artifacts", tok, map[string]any{
		"path": "governance/notes.md", "content": strings.Replace(secCharter, "Charter", "Notes", 2),
		"write_context": map[string]string{"run_id": "run-alice", "task_path": secTask}})
	if code != http.StatusCreated {
		t.Fatalf("assignee write: %d %s, want 201", code, out)
	}
	if head := secGit(t, repo, "rev-parse", secBranch); head == reviewed {
		t.Fatalf("assignee write did not land on the run branch")
	}
}

// Inverted poc/verify-r1-12 TestPOC_R12_ForeignRunWriteMergesToMain: during
// review nobody — foreign contributor, the reviewer, an operator — can change
// the run branch, and the governed merge publishes only the reviewed work.
func TestP36_R12_ReviewFreeze_InjectionNeverReachesMain(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	repo, reviewed := seedRunRepo(t)
	fs, authSvc, ts := secRunServer(t, repo, "review", []domain.StepExecution{
		{ExecutionID: "e-exec", StepID: "execute", Status: domain.StepStatusCompleted, ActorID: "alice", Attempt: 1},
		{ExecutionID: "exec-review", StepID: "review", Status: domain.StepStatusInProgress, ActorID: "bob", Attempt: 1}})

	wc := map[string]any{"content": secInjected, "write_context": map[string]string{"run_id": "run-alice", "task_path": secTask}}
	for _, c := range []struct {
		who  string
		want int
	}{{"mallory", http.StatusForbidden}, {"bob", http.StatusConflict}, {"op-1", http.StatusConflict}} {
		code, out := secDo(t, "PUT", ts.URL+"/api/v1/artifacts/governance/charter.md", secToken(t, authSvc, c.who), wc)
		if code != c.want {
			t.Errorf("%s PUT charter on run in review: %d %s, want %d", c.who, code, out, c.want)
		}
	}
	if head := secGit(t, repo, "rev-parse", secBranch); head != reviewed {
		t.Fatalf("run branch moved during review: %s -> %s", reviewed, head)
	}

	// bob accepts; the real orchestrator merges the branch.
	fs.run.Status = domain.RunStatusCommitting
	orch, err := engine.New(secWF{}, fs, secActors{}, secReader{}, secEvents{}, git.NewCLIClient(repo), secLoader{})
	if err != nil {
		t.Fatal(err)
	}
	orch.WithBranchProtectPolicy(branchprotect.New(branchprotect.StaticRules(nil)))
	if err := orch.MergeRunBranch(ctx, "run-alice"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if onMain := secGit(t, repo, "show", "main:governance/charter.md"); strings.Contains(onMain, "MALLORY") {
		t.Fatalf("unreviewed injection reached main")
	}
	// The merge itself happened (INIT-024 TASK-010): a merge git refused
	// fails the run with nil from MergeRunBranch, and main then trivially
	// lacks the injection. On GitHub's runners, which have no git
	// identity, that is what this test saw until the client's merges got
	// Spine's fallback committer.
	if fs.run.Status == domain.RunStatusFailed {
		t.Fatalf("the governed merge failed the run")
	}
	if onMain := secGit(t, repo, "show", "main:"+secTask); onMain != "task v2 (alice work)" {
		t.Fatalf("the reviewed work is not on main: %q", onMain)
	}
}

// /artifacts/add: only the draft assignee (or an operator) adds to a planning run.
func TestP36_ArtifactsAdd_RequiresDraftAssignee(t *testing.T) {
	base := newFakeStore()
	secActor(base, "alice", domain.RoleContributor)
	secActor(base, "mallory", domain.RoleContributor)
	fs := &secStore{fakeStore: base,
		run: &domain.Run{Status: domain.RunStatusActive, Mode: domain.RunModePlanning, CurrentStepID: "draft",
			TaskPath: "initiatives/INIT-009/initiative.md", BranchName: "spine/plan/init-009-alice"},
		execs: []domain.StepExecution{{ExecutionID: "e-draft", StepID: "draft", Status: domain.StepStatusInProgress, ActorID: "alice", Attempt: 1}}}
	authSvc := auth.NewService(base)
	artSvc := newFakeArtifactService()
	// The parent resolves, so before the fix mallory's add succeeded (201).
	artSvc.artifacts["initiatives/INIT-009/initiative.md"] = &domain.Artifact{
		Path: "initiatives/INIT-009/initiative.md", ID: "INIT-009", Type: domain.ArtifactTypeInitiative, Title: "Init 9"}
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, Artifacts: artSvc,
		Git: &fakeGitReader{files: map[string][]byte{}}, StepAssigner: secSteps}).Handler())
	defer ts.Close()

	code, out := secDo(t, "POST", ts.URL+"/api/v1/artifacts/add", secToken(t, authSvc, "mallory"),
		map[string]string{"run_id": "run-alice", "artifact_type": "Epic", "parent": "INIT-009", "title": "Injected"})
	if code != http.StatusForbidden {
		t.Fatalf("mallory /artifacts/add on alice's planning run: %d %s, want 403", code, out)
	}
	// Control: the draft assignee still adds.
	code, out = secDo(t, "POST", ts.URL+"/api/v1/artifacts/add", secToken(t, authSvc, "alice"),
		map[string]string{"run_id": "run-alice", "artifact_type": "Epic", "parent": "INIT-009", "title": "Planned"})
	if code != http.StatusCreated {
		t.Fatalf("alice /artifacts/add on her own planning run: %d %s, want 201", code, out)
	}
}

// ── minimal engine fakes for MergeRunBranch (mirrors poc/verify-r1-12) ──

type secWF struct{}

func (secWF) ResolveWorkflow(context.Context, string, string) (*workflow.BindingResult, error) {
	return nil, nil
}
func (secWF) ResolveWorkflowForMode(context.Context, string, string, string) (*workflow.BindingResult, error) {
	return nil, nil
}

type secActors struct{}

func (secActors) DeliverAssignment(context.Context, actor.AssignmentRequest) error { return nil }
func (secActors) ProcessResult(context.Context, actor.AssignmentRequest, actor.AssignmentResult) error {
	return nil
}

type secReader struct{}

func (secReader) Read(context.Context, string, string) (*domain.Artifact, error) { return nil, nil }

type secEvents struct{}

func (secEvents) Emit(context.Context, domain.Event) error { return nil }

type secLoader struct{}

func (secLoader) LoadWorkflow(context.Context, string, string) (*domain.WorkflowDefinition, error) {
	return &domain.WorkflowDefinition{ID: "task-default"}, nil
}
