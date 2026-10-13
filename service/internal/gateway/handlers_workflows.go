package gateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"

	"github.com/go-chi/chi/v5"
)

type workflowCreateRequest struct {
	ID           string               `json:"id"`
	Body         string               `json:"body"`
	WriteContext *writeContextRequest `json:"write_context,omitempty"`
}

type workflowUpdateRequest struct {
	Body         string               `json:"body"`
	WriteContext *writeContextRequest `json:"write_context,omitempty"`
}

type workflowValidateRequest struct {
	Body string `json:"body"`
}

func (s *Server) handleWorkflowCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "workflow.create") {
		return
	}

	req, ok := decodeBody[workflowCreateRequest](w, r)
	if !ok {
		return
	}
	if req.ID == "" || req.Body == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "id and body required"))
		return
	}

	// If this 503 fires in production, a new service was added to
	// gateway.ServerConfig without being wired in cmd/spine buildServerConfig.
	// See cmd/spine/serve_smoke_test.go — that test probes every endpoint
	// and is the canary for this class of regression.
	svc, ok := s.needWorkflows(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	// Dispatch per ADR-008, identical for create and update (INIT-023
	// TASK-014, audit F7):
	//   - write_context present → the workflow's own lifecycle run branch
	//     (resolveWorkflowWriteContext: run_id required, the run must govern
	//     workflows/<id>.yaml, the caller must be its bound actor)
	//   - no write_context + below operator → a workflow-lifecycle planning
	//     run (409 while one is already open for the id)
	//   - no write_context + operator/admin → direct commit (bypass), tagged
	//     with a Workflow-Bypass trailer for audit; workflow.Service re-checks
	//     the operator role when a branch-protection policy is installed
	if req.WriteContext == nil && !callerHasOperatorPrivileges(ctx) {
		planning, err := s.startWorkflowLifecycleRun(ctx, req.ID, req.Body, false)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, workflowPlanningResponse(planning, req.ID))
		return
	}
	if req.WriteContext != nil {
		branch, err := s.resolveWorkflowWriteContext(ctx, req.WriteContext, req.ID)
		if err != nil {
			WriteError(w, err)
			return
		}
		ctx = workflow.WithWriteContext(ctx, workflow.WriteContext{Branch: branch})
	} else {
		// Operator direct-commit path: tag the commit for audit.
		ctx = workflow.WithBypass(ctx)
	}

	result, err := svc.Create(ctx, req.ID, req.Body)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusCreated, workflowWriteResponse(result, r.Context(), req.WriteContext, workflow.IsBypass(ctx)))
}

// callerHasOperatorPrivileges returns true when the authenticated actor is at
// operator level or higher. Reviewers are governed through the
// workflow-lifecycle planning flow; operators (and above) retain a
// direct-commit path for recovery (ADR-008). Uses HasAtLeast so future roles
// inserted between reviewer and operator do not silently drop into the wrong
// dispatch branch.
func callerHasOperatorPrivileges(ctx context.Context) bool {
	actor := actorFromContext(ctx)
	if actor == nil {
		return false
	}
	return actor.Role.HasAtLeast(domain.RoleOperator)
}

func (s *Server) handleWorkflowUpdate(w http.ResponseWriter, r *http.Request, id string) {
	if !s.authorize(w, r, "workflow.update") {
		return
	}

	req, ok := decodeBody[workflowUpdateRequest](w, r)
	if !ok {
		return
	}
	if req.Body == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "body required"))
		return
	}

	svc, ok := s.needWorkflows(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	// Same dispatch as handleWorkflowCreate (INIT-023 TASK-014, audit F7).
	// Before it, a reviewer's PUT without write_context set neither a branch
	// nor the bypass and committed straight to main ("authoritative"),
	// skipping both the ADR-008 lifecycle and branch protection.
	if req.WriteContext == nil && !callerHasOperatorPrivileges(ctx) {
		planning, err := s.startWorkflowLifecycleRun(ctx, id, req.Body, true)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, workflowPlanningResponse(planning, id))
		return
	}
	if req.WriteContext != nil {
		branch, err := s.resolveWorkflowWriteContext(ctx, req.WriteContext, id)
		if err != nil {
			WriteError(w, err)
			return
		}
		ctx = workflow.WithWriteContext(ctx, workflow.WriteContext{Branch: branch})
	} else {
		// Operator direct-commit path: tag the commit for audit (ADR-008).
		ctx = workflow.WithBypass(ctx)
	}

	result, err := svc.Update(ctx, id, req.Body)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, workflowWriteResponse(result, r.Context(), req.WriteContext, workflow.IsBypass(ctx)))
}

func (s *Server) handleWorkflowRead(w http.ResponseWriter, r *http.Request, id string) {
	if !s.authorize(w, r, "workflow.read") {
		return
	}

	ref := r.URL.Query().Get("ref")
	if err := git.ValidateRef(ref); err != nil {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, err.Error()))
		return
	}

	svc, ok := s.needWorkflows(w, r)
	if !ok {
		return
	}

	res, err := svc.Read(r.Context(), id, ref)
	if err != nil {
		WriteError(w, err)
		return
	}

	wf := res.Workflow
	WriteJSON(w, http.StatusOK, map[string]any{
		"id":            wf.ID,
		"workflow_path": res.Path,
		"name":          wf.Name,
		"version":       wf.Version,
		"status":        wf.Status,
		"mode":          wf.Mode,
		"applies_to":    wf.AppliesTo,
		"description":   wf.Description,
		"body":          res.Body,
		"source_commit": res.SourceCommit,
	})
}

func (s *Server) handleWorkflowList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "workflow.list") {
		return
	}

	svc, ok := s.needWorkflows(w, r)
	if !ok {
		return
	}

	opts := workflow.ListOptions{
		AppliesTo: r.URL.Query().Get("applies_to"),
		Status:    r.URL.Query().Get("status"),
		Mode:      r.URL.Query().Get("mode"),
	}
	items, err := svc.List(r.Context(), opts)
	if err != nil {
		WriteError(w, err)
		return
	}

	summaries := make([]map[string]any, 0, len(items))
	for _, wf := range items {
		summaries = append(summaries, map[string]any{
			"id":            wf.ID,
			"workflow_path": wf.Path,
			"name":          wf.Name,
			"version":       wf.Version,
			"status":        wf.Status,
			"mode":          wf.Mode,
			"applies_to":    wf.AppliesTo,
			"description":   wf.Description,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"items":    summaries,
		"has_more": false,
	})
}

func (s *Server) handleWorkflowValidate(w http.ResponseWriter, r *http.Request, id string) {
	if !s.authorize(w, r, "workflow.validate") {
		return
	}

	req, ok := decodeBody[workflowValidateRequest](w, r)
	if !ok {
		return
	}
	if req.Body == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "body required"))
		return
	}

	svc, ok := s.needWorkflows(w, r)
	if !ok {
		return
	}

	result := svc.ValidateBody(r.Context(), id, req.Body)
	WriteJSON(w, http.StatusOK, map[string]any{
		"workflow_id": id,
		"status":      result.Status,
		"errors":      result.Errors,
		"warnings":    result.Warnings,
	})
}

// handleWorkflowWildcard dispatches GET/PUT /workflows/{id} and POST
// /workflows/{id}/validate. Workflow IDs are simple slugs with no slashes, but
// we still register via wildcard to keep the routing shape consistent with
// the artifact surface.
func (s *Server) handleWorkflowWildcard(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "*")
	id, suffix := splitWorkflowSuffix(raw)

	switch {
	case suffix == "/validate" && r.Method == http.MethodPost:
		s.handleWorkflowValidate(w, r, id)
	case suffix == "" && r.Method == http.MethodGet:
		s.handleWorkflowRead(w, r, id)
	case suffix == "" && r.Method == http.MethodPut:
		s.handleWorkflowUpdate(w, r, id)
	default:
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
	}
}

func splitWorkflowSuffix(raw string) (string, string) {
	for _, suffix := range []string{"/validate"} {
		if strings.HasSuffix(raw, suffix) {
			return strings.TrimSuffix(raw, suffix), suffix
		}
	}
	return raw, ""
}

func workflowWriteResponse(result *workflow.WriteResult, ctx context.Context, wc *writeContextRequest, bypass bool) map[string]any {
	writeMode := "authoritative"
	switch {
	case wc != nil && wc.RunID != "":
		writeMode = "proposed"
	case bypass:
		writeMode = "bypass"
	}
	resp := map[string]any{
		"id":            result.Workflow.ID,
		"workflow_path": result.Path,
		"version":       result.Workflow.Version,
		"commit_sha":    result.CommitSHA,
		"write_mode":    writeMode,
		"trace_id":      observe.TraceID(ctx),
	}
	if bypass {
		resp["workflow_bypass"] = true
	}
	return resp
}

// startWorkflowLifecycleRun opens the workflow-lifecycle planning run for a
// caller below operator with no write_context: a create run for POST
// /workflows, an update run for PUT /workflows/{id} (ADR-008 §1). No starter
// wired is a 503 rather than a silent demotion to a direct commit, which
// would skip both the lifecycle review and the bypass trailer; bootstrap
// recovery is the operator bypass (ADR-008 §5). A lifecycle run already
// open for the id is a 409 naming it (refuseIfWorkflowRunOpen).
func (s *Server) startWorkflowLifecycleRun(ctx context.Context, id, body string, update bool) (*PlanningRunResult, error) {
	starter := s.wfPlanningStarterFrom(ctx)
	if starter == nil {
		return nil, domain.NewError(domain.ErrUnavailable,
			"workflow planning run starter not configured; reviewer writes require the governance flow")
	}
	if err := s.refuseIfWorkflowRunOpen(ctx, id); err != nil {
		return nil, err
	}
	if update {
		return starter.StartWorkflowUpdatePlanningRun(ctx, id, body)
	}
	return starter.StartWorkflowPlanningRun(ctx, id, body)
}

// workflowPlanningResponse is the 201 body for a write routed to a
// workflow-lifecycle planning run (create and update).
func workflowPlanningResponse(planning *PlanningRunResult, id string) map[string]any {
	return map[string]any{
		"run_id":                planning.RunID,
		"branch_name":           planning.BranchName,
		"workflow_id":           id,
		"workflow_path":         planning.TaskPath,
		"governing_workflow_id": planning.WorkflowID,
		"status":                planning.Status,
		"mode":                  planning.Mode,
		"trace_id":              planning.TraceID,
		"write_mode":            "planning",
	}
}

// resolveWorkflowWriteContext resolves write_context on a workflow endpoint
// (INIT-023 TASK-014). Unlike the artifact endpoints it never yields an
// authoritative write:
//
//  1. override → 400: ADR-009 override is not wired on the workflow
//     surface (workflow.Service emits no override event or trailer);
//     operators use the ADR-008 bypass instead.
//  2. run_id is required → 400 (V1: on 61994fe `write_context: {}`
//     resolved to "no branch, no bypass" and committed to main with no
//     trailer, for reviewers and operators alike).
//  3. The run must be the planning run that governs workflows/<id>.yaml
//     (I7, V2: a workflow write could name any planning run — another
//     workflow's, an artifact-creation run, or a task run whose task_path
//     the caller echoed — and reach main under that run's review instead
//     of workflow_review). Owner question 2 (default): only the workflow's
//     own lifecycle run. Only the workflow starters can create such a run
//     (I9: POST /runs and StartPlanningRun refuse workflow paths), so
//     Mode + TaskPath identify it. Then the run must be active and have a
//     branch.
//  4. The caller must be allowed to write the branch
//     (authorizeRunBranchWrite: its draft assignee, or an operator, never
//     during review).
func (s *Server) resolveWorkflowWriteContext(ctx context.Context, wc *writeContextRequest, id string) (string, error) {
	if wc.Override {
		return "", domain.NewError(domain.ErrInvalidParams,
			"write_context.override is not supported on workflow endpoints (use ADR-008 operator bypass: omit write_context entirely as operator+ role)")
	}
	if wc.RunID == "" {
		return "", domain.NewError(domain.ErrInvalidParams,
			"write_context.run_id is required on workflow endpoints; omit write_context to open a workflow-lifecycle planning run (operators: omit it for the ADR-008 bypass)")
	}
	if err := workflow.ValidateID(id); err != nil {
		return "", err
	}
	st := s.storeFrom(ctx)
	if st == nil {
		return "", domain.NewError(domain.ErrUnavailable, "store not configured")
	}
	run, err := st.GetRun(ctx, wc.RunID)
	if err != nil {
		return "", fmt.Errorf("resolve write context: %w", err)
	}
	want := workflow.FilePath(id)
	if run.Mode != domain.RunModePlanning || run.TaskPath != want {
		return "", domain.NewErrorWithDetail(domain.ErrInvalidParams,
			fmt.Sprintf("run %s does not govern %s; a workflow edit is written only on that workflow's own lifecycle run (ADR-008)", wc.RunID, want),
			map[string]string{"run_task_path": run.TaskPath, "workflow_path": want})
	}
	if run.Status != domain.RunStatusActive {
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("run %s is not active (status: %s)", wc.RunID, run.Status))
	}
	if run.BranchName == "" {
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("run %s has no branch", wc.RunID))
	}
	if err := s.authorizeRunBranchWrite(ctx, run); err != nil {
		return "", err
	}
	return run.BranchName, nil
}

// refuseIfWorkflowRunOpen answers 409 when a non-terminal planning run
// already governs workflows/<id>.yaml, naming it so the caller can stack
// edits there (as its draft assignee, with write_context {run_id}) or wait
// (INIT-023 TASK-014 §3.3; owner question 3 default: for create and
// update). Two lifecycle runs on one file both pass the version-bump check
// against main; edits to disjoint lines would merge cleanly into a
// definition no reviewer saw, under one ambiguous version label.
//
// Best effort: two first requests can race; the merge-conflict path is
// the backstop. A failed workflow start leaves its record failed, not
// pending (engine, design-check L1), so only a live run holds the id. An
// idle lifecycle run does hold it until it finishes; recovery is an
// operator cancel or the operator bypass. A store read failure is 503
// (fail closed); with no store the check is skipped, because the starter
// cannot run without one either.
func (s *Server) refuseIfWorkflowRunOpen(ctx context.Context, id string) error {
	if err := workflow.ValidateID(id); err != nil {
		return err
	}
	st := s.storeFrom(ctx)
	if st == nil {
		return nil
	}
	path := workflow.FilePath(id)
	runs, err := st.ListRunsByTask(ctx, path)
	if err != nil {
		observe.Logger(ctx).Error("workflow lifecycle: list runs for the workflow failed",
			"workflow_path", path, "error", err)
		return domain.NewError(domain.ErrUnavailable,
			fmt.Sprintf("cannot check for an open workflow-lifecycle run on %s", path))
	}
	for i := range runs {
		r := &runs[i]
		if r.Mode != domain.RunModePlanning || r.Status.IsTerminal() {
			continue
		}
		return domain.NewErrorWithDetail(domain.ErrConflict,
			fmt.Sprintf("workflow-lifecycle run %s is already open for %s; stack edits on it with write_context {run_id} as its draft assignee, or wait for it to finish",
				r.RunID, path),
			map[string]string{
				"run_id":          r.RunID,
				"branch_name":     r.BranchName,
				"status":          string(r.Status),
				"current_step_id": r.CurrentStepID,
			})
	}
	return nil
}
