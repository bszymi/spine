package engine

// INIT-023 TASK-014 (audit F7, design-check L1 and V4): update-capable
// workflow planning runs, the create pre-check, failed (never pending)
// records after a branch-write failure, and the one-producer rule for
// workflow task paths in StartPlanningRun.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// workflowReadGit serves fixed files from ReadFile (git's not-found for
// anything else, or readErr when set) and records branch creation and
// deletion.
type workflowReadGit struct {
	stubGitOperator
	files    map[string]string
	readErr  error
	created  []string
	deleted  []string
	readRefs []string
}

func (g *workflowReadGit) ReadFile(_ context.Context, ref, path string) ([]byte, error) {
	g.readRefs = append(g.readRefs, ref)
	if g.readErr != nil {
		return nil, g.readErr
	}
	if c, ok := g.files[path]; ok {
		return []byte(c), nil
	}
	return nil, &git.GitError{Kind: git.ErrKindNotFound, Op: "show", Message: "not found"}
}

func (g *workflowReadGit) CreateBranch(_ context.Context, name, _ string) error {
	g.created = append(g.created, name)
	return nil
}

func (g *workflowReadGit) DeleteBranch(_ context.Context, name string) error {
	g.deleted = append(g.deleted, name)
	return nil
}

var bumpedWorkflowBody = strings.Replace(validWorkflowBody, `version: "1.0"`, `version: "1.1"`, 1)

func spineErrCode(err error) domain.ErrorCode {
	var se *domain.SpineError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

// AC 3: the update starter writes through Update on the new run branch; the
// run has the lifecycle shape (planning, workflows/<id>.yaml, the
// Workflow/creation binding).
func TestStartWorkflowUpdatePlanningRun_WritesUpdateOnRunBranch(t *testing.T) {
	g := &workflowReadGit{files: map[string]string{"workflows/new-workflow.yaml": validWorkflowBody}}
	w := &mockWorkflowWriter{}
	store := &mockRunStore{}
	orch := workflowPlanningOrchestrator(w, workflowPlanningResolver(), store, &mockEventEmitter{}, g)

	res, err := orch.StartWorkflowUpdatePlanningRun(context.Background(), "new-workflow", bumpedWorkflowBody)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if w.createCalls != 0 || w.updateCalls != 1 || w.updatedID != "new-workflow" || w.updatedBody != bumpedWorkflowBody {
		t.Fatalf("writer: create=%d update=%d id=%q", w.createCalls, w.updateCalls, w.updatedID)
	}
	if w.branch != res.Run.BranchName || !strings.HasPrefix(res.Run.BranchName, "spine/plan/") {
		t.Fatalf("update written on %q, run branch %q", w.branch, res.Run.BranchName)
	}
	if res.Run.Mode != domain.RunModePlanning || res.Run.TaskPath != "workflows/new-workflow.yaml" ||
		res.Run.WorkflowID != "workflow-lifecycle" || res.Run.Status != domain.RunStatusActive {
		t.Fatalf("run: %+v", res.Run)
	}
	if len(g.readRefs) != 1 || g.readRefs[0] != workflowPlanningBaseRef {
		t.Fatalf("prior definition read at %v, want [%s] (the ref the branch is cut from)", g.readRefs, workflowPlanningBaseRef)
	}
}

// AC 3: every refusal happens before a branch, a run record or a writer
// call exists.
func TestStartWorkflowUpdatePlanningRun_RefusesBeforeAnyBranch(t *testing.T) {
	cases := map[string]struct {
		files   map[string]string
		readErr error
		id      string
		body    string
		want    domain.ErrorCode
	}{
		"missing workflow": {files: map[string]string{}, id: "new-workflow", body: bumpedWorkflowBody, want: domain.ErrNotFound},
		"no version bump":  {files: map[string]string{"workflows/new-workflow.yaml": validWorkflowBody}, id: "new-workflow", body: validWorkflowBody, want: domain.ErrValidationFailed},
		"invalid id":       {files: map[string]string{}, id: "../x", body: bumpedWorkflowBody, want: domain.ErrInvalidParams},
		"unparsable prior": {files: map[string]string{"workflows/new-workflow.yaml": "{{not yaml"}, id: "new-workflow", body: bumpedWorkflowBody, want: domain.ErrInternal},
		"read failure":     {readErr: errors.New("git exploded"), id: "new-workflow", body: bumpedWorkflowBody},
		"invalid body":     {files: map[string]string{"workflows/new-workflow.yaml": validWorkflowBody}, id: "new-workflow", body: "id: new-workflow\n", want: domain.ErrValidationFailed},
		"body id mismatch": {files: map[string]string{"workflows/other.yaml": validWorkflowBody}, id: "other", body: bumpedWorkflowBody, want: domain.ErrInvalidParams},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := &workflowReadGit{files: tc.files, readErr: tc.readErr}
			w := &mockWorkflowWriter{}
			store := &mockRunStore{}
			orch := workflowPlanningOrchestrator(w, workflowPlanningResolver(), store, &mockEventEmitter{}, g)
			_, err := orch.StartWorkflowUpdatePlanningRun(context.Background(), tc.id, tc.body)
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if tc.want != "" && spineErrCode(err) != tc.want {
				t.Fatalf("err=%v, want %s", err, tc.want)
			}
			if tc.want == domain.ErrValidationFailed && name == "no version bump" &&
				!strings.Contains(err.Error(), "workflow.update requires a version bump (prior=1.0, submitted=1.0)") {
				t.Fatalf("message differs from workflow.Service.Update's: %v", err)
			}
			if len(g.created) != 0 || store.createdRun != nil || w.updateCalls+w.createCalls != 0 {
				t.Fatalf("refused update left side effects: branches=%v run=%v writer=%d/%d",
					g.created, store.createdRun, w.createCalls, w.updateCalls)
			}
		})
	}
}

// AC 3 / design-check L1: a create of an id that already exists is refused
// with ErrAlreadyExists before CreateRun, a branch or a writer call — the
// probe TestT014Check_CreateExistingWorkflow_LeavesPendingRun inverted.
func TestStartWorkflowPlanningRun_ExistingID_RefusedBeforeAnyRun(t *testing.T) {
	g := &workflowReadGit{files: map[string]string{"workflows/new-workflow.yaml": validWorkflowBody}}
	w := &mockWorkflowWriter{}
	store := &mockRunStore{}
	orch := workflowPlanningOrchestrator(w, workflowPlanningResolver(), store, &mockEventEmitter{}, g)

	_, err := orch.StartWorkflowPlanningRun(context.Background(), "new-workflow", validWorkflowBody)
	if spineErrCode(err) != domain.ErrAlreadyExists {
		t.Fatalf("err=%v, want already_exists", err)
	}
	if !strings.Contains(err.Error(), "PUT /workflows/{id}") {
		t.Errorf("message should point at the update path: %v", err)
	}
	if store.createdRun != nil || len(store.runs) != 0 || len(g.created) != 0 || w.createCalls != 0 {
		t.Fatalf("refused create left side effects: run=%v branches=%v writer=%d", store.createdRun, g.created, w.createCalls)
	}

	// A read failure other than not-found refuses too (fail closed).
	g2 := &workflowReadGit{readErr: errors.New("io error")}
	store2 := &mockRunStore{}
	orch2 := workflowPlanningOrchestrator(&mockWorkflowWriter{}, workflowPlanningResolver(), store2, &mockEventEmitter{}, g2)
	if _, err := orch2.StartWorkflowPlanningRun(context.Background(), "new-workflow", validWorkflowBody); err == nil || store2.createdRun != nil {
		t.Fatalf("read failure: err=%v run=%v, want refusal with no run", err, store2.createdRun)
	}
}

// AC 3 / design-check L1: a writer failure after CreateRun leaves the record
// failed (terminal), never pending, for create and for update; the branch
// cleanup is unchanged.
func TestStartWorkflowPlanningRun_WriterFailure_MarksRunFailed(t *testing.T) {
	for _, update := range []bool{false, true} {
		name := "create"
		if update {
			name = "update"
		}
		t.Run(name, func(t *testing.T) {
			files := map[string]string{}
			body := validWorkflowBody
			if update {
				files["workflows/new-workflow.yaml"] = validWorkflowBody
				body = bumpedWorkflowBody
			}
			g := &workflowReadGit{files: files}
			w := &mockWorkflowWriter{err: domain.NewError(domain.ErrForbidden, "denied on branch")}
			store := &mockRunStore{}
			orch := workflowPlanningOrchestrator(w, workflowPlanningResolver(), store, &mockEventEmitter{}, g)

			var err error
			if update {
				_, err = orch.StartWorkflowUpdatePlanningRun(context.Background(), "new-workflow", body)
			} else {
				_, err = orch.StartWorkflowPlanningRun(context.Background(), "new-workflow", body)
			}
			if err == nil || !strings.Contains(err.Error(), name+" workflow on branch") {
				t.Fatalf("err=%v, want the %s writer error", err, name)
			}
			if spineErrCode(err) != domain.ErrForbidden {
				t.Fatalf("writer error code lost: %v", err)
			}
			if store.createdRun == nil {
				t.Fatal("precondition: the run record is created before the branch write")
			}
			run := store.runs[store.createdRun.RunID]
			if run.Status != domain.RunStatusFailed || !run.Status.IsTerminal() {
				t.Fatalf("run left %s, want failed", run.Status)
			}
			if len(g.deleted) != 1 || g.deleted[0] != run.BranchName {
				t.Fatalf("branch cleanup: deleted=%v, want [%s]", g.deleted, run.BranchName)
			}
		})
	}
}

// AC 17 / I9 / V4: StartPlanningRun refuses a workflow-definition task path,
// whatever its spelling, before Parse — no branch, no run record, no writer
// call. A path that only mentions "workflows" deeper down is not refused by
// this guard.
func TestStartPlanningRun_RefusesWorkflowPath(t *testing.T) {
	for _, p := range []string{
		"workflows/x.yaml", "./workflows/x.yaml", "/workflows/x.yaml",
		"governance/../workflows/x.md", "workflows//x.md", "Workflows/x.md", "workflows",
	} {
		t.Run(p, func(t *testing.T) {
			writer := &mockArtifactWriter{}
			store := &mockRunStore{}
			gitOp := &workflowReadGit{}
			orch := planningOrchestrator(writer, defaultPlanningResolver(), store, &mockEventEmitter{}, gitOp)
			_, err := orch.StartPlanningRun(context.Background(), p, validArtifactContent)
			if spineErrCode(err) != domain.ErrInvalidParams || !strings.Contains(err.Error(), "workflow-lifecycle") {
				t.Fatalf("err=%v, want invalid_params naming the workflow-lifecycle path", err)
			}
			if store.createdRun != nil || len(gitOp.created) != 0 || writer.createdPath != "" {
				t.Fatalf("side effects: run=%v branches=%v writer=%q", store.createdRun, gitOp.created, writer.createdPath)
			}
		})
	}

	// Control: an ordinary artifact path still starts a run.
	writer := &mockArtifactWriter{}
	orch := planningOrchestrator(writer, defaultPlanningResolver(), &mockRunStore{}, &mockEventEmitter{}, &stubGitOperator{})
	if _, err := orch.StartPlanningRun(context.Background(), "initiatives/INIT-099/initiative.md", validArtifactContent); err != nil {
		t.Fatalf("control: %v", err)
	}
}
