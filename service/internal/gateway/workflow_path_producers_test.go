package gateway_test

// INIT-023 TASK-014 AC 17 (design-check V3, V4, I9): workflow definitions
// have one producer. The artifact surface refuses every spelling of a
// workflows/ path, on its own (not relying on TASK-004's service-level
// Clean), and POST /runs refuses a planning run on a workflows/ task path.
// The probes TestT014Check_ArtifactPathDotSegments_ReachWorkflowsDir and
// TestT014Check_RunStartPlanning_AcceptsWorkflowTaskPath, inverted.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
)

var dotSegmentWrites = []struct {
	method, path string
	body         func(wc map[string]string) map[string]any
}{
	{"POST", "/api/v1/artifacts", func(wc map[string]string) map[string]any {
		return map[string]any{"path": "./workflows/x.yaml", "content": secCharter, "write_context": wc}
	}},
	{"POST", "/api/v1/artifacts", func(wc map[string]string) map[string]any {
		return map[string]any{"path": "governance/../workflows/x.yaml", "content": secCharter, "write_context": wc}
	}},
	{"POST", "/api/v1/artifacts", func(wc map[string]string) map[string]any {
		return map[string]any{"path": "workflows//x.yaml", "content": secCharter, "write_context": wc}
	}},
	{"PUT", "/api/v1/artifacts/./workflows/task-default.yaml", func(wc map[string]string) map[string]any {
		return map[string]any{"content": secCharter, "write_context": wc}
	}},
	{"PUT", "/api/v1/artifacts/governance/../workflows/task-default.yaml", func(wc map[string]string) map[string]any {
		return map[string]any{"content": secCharter, "write_context": wc}
	}},
}

func requireWorkflowRejection(t *testing.T, code int, body string) {
	t.Helper()
	var er gateway.ErrorResponse
	_ = json.Unmarshal([]byte(body), &er)
	if code != http.StatusBadRequest || len(er.Errors) == 0 {
		t.Fatalf("got %d %s, want 400", code, body)
	}
	detail, _ := er.Errors[0].Detail.(map[string]any)
	if op, _ := detail["use_operation"].(string); !strings.HasPrefix(op, "workflow.") {
		t.Fatalf("400 lacks use_operation workflow.*: %s", body)
	}
}

// Real artifact.Service over a real repository: the run's own assignee
// writes through every dot-segment spelling, and the run branch's
// workflows/ tree is byte-identical afterwards.
func TestArtifactWrite_DotSegmentWorkflowPath_Refused(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	repo, _ := seedRunRepo(t)
	secGit(t, repo, "checkout", "-q", secBranch)
	_ = os.MkdirAll(filepath.Join(repo, "workflows"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "workflows", "task-default.yaml"), []byte("id: task-default\nname: x\nversion: \"1.0\"\nstatus: Active\n"), 0o644)
	secGit(t, repo, "add", ".")
	secGit(t, repo, "commit", "-q", "-m", "seed wf")
	secGit(t, repo, "checkout", "-q", "main")
	treeBefore := secGit(t, repo, "rev-parse", secBranch+":workflows")

	_, authSvc, ts := secRunServer(t, repo, "execute", []domain.StepExecution{
		{ExecutionID: "e-exec", StepID: "execute", Status: domain.StepStatusInProgress, ActorID: "alice", Attempt: 1}})
	tok := secToken(t, authSvc, "alice")
	wc := map[string]string{"run_id": "run-alice", "task_path": secTask}

	for _, w := range dotSegmentWrites {
		code, out := secDo(t, w.method, ts.URL+w.path, tok, w.body(wc))
		requireWorkflowRejection(t, code, out)
	}
	if after := secGit(t, repo, "rev-parse", secBranch+":workflows"); after != treeBefore {
		t.Fatalf("run branch workflows/ tree changed %s -> %s", treeBefore, after)
	}
}

// The gateway guard stands on its own: with a recording fake artifact
// service (no service-level Clean at all), no row reaches Create or Update.
func TestArtifactWrite_DotSegmentWorkflowPath_GuardStandsAlone(t *testing.T) {
	run := &domain.Run{RunID: rbwRunID, Status: domain.RunStatusActive, Mode: domain.RunModePlanning,
		TaskPath: "initiatives/INIT-001/initiative.md", CurrentStepID: "draft", BranchName: rbwBranch}
	env := newRBWEnv(t, run, []domain.StepExecution{rbwExec("draft", domain.StepStatusInProgress, rbwHolder)}, rbwStepTypes, domain.RoleContributor)
	wc := map[string]string{"run_id": rbwRunID}
	for _, w := range dotSegmentWrites {
		code, out := env.do(t, rbwHolder, w.method, w.path, w.body(wc))
		requireWorkflowRejection(t, code, out)
	}
	if env.art.writes != 0 {
		t.Fatalf("%d refused writes reached the artifact service", env.art.writes)
	}
}

type recPlanningStarter struct{ paths []string }

func (p *recPlanningStarter) StartPlanningRun(_ context.Context, path, _ string) (*gateway.PlanningRunResult, error) {
	p.paths = append(p.paths, path)
	return &gateway.PlanningRunResult{RunID: "run-x", TaskPath: path, Mode: "planning", Status: "active", WorkflowID: "document-creation"}, nil
}

func TestRunStart_Planning_WorkflowTaskPath_Refused(t *testing.T) {
	base := newFakeStore()
	secActor(base, "carol", domain.RoleContributor)
	authSvc := auth.NewService(base)
	ps := &recPlanningStarter{}
	ts := httptest.NewServer(gateway.NewServer(":0", gateway.ServerConfig{Store: base, Auth: authSvc, PlanningRunStarter: ps}).Handler())
	defer ts.Close()
	tok := secToken(t, authSvc, "carol")

	for _, p := range []string{"workflows/x.yaml", "./workflows/x.yaml", "workflows/task-default.yaml", "/workflows/x.yaml", "Workflows/x.md"} {
		code, out := secDo(t, "POST", ts.URL+"/api/v1/runs", tok, map[string]any{
			"mode": "planning", "task_path": p, "artifact_content": secCharter})
		requireWorkflowRejection(t, code, out)
		if !strings.Contains(out, `"use_operation":"workflow.create"`) {
			t.Fatalf("task_path %q: %s, want use_operation workflow.create", p, out)
		}
	}
	if len(ps.paths) != 0 {
		t.Fatalf("workflow task paths reached the planning starter: %v", ps.paths)
	}

	// Control: a .md task path still starts a planning run.
	code, out := secDo(t, "POST", ts.URL+"/api/v1/runs", tok, map[string]any{
		"mode": "planning", "task_path": "initiatives/INIT-010/initiative.md", "artifact_content": secCharter})
	if code != http.StatusCreated || len(ps.paths) != 1 {
		t.Fatalf("control: %d %s (starter paths %v)", code, out, ps.paths)
	}
}
