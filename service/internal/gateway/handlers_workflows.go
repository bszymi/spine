package gateway

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

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

// workflowIdentifierPattern is the charset the standalone gateway accepts
// for the workflow identifiers the engine embeds in event IDs, execution
// IDs and branch names: step IDs, divergence-point IDs, divergence-branch
// IDs and convergence-point IDs (core/engine "evt-<trace>-<step>-
// completed", core/divergence "<run>-div-<dp>"). INIT-023 TASK-015, audit
// P3-28; the default of owner question 7, pending the owner.
//
// It is enforced here, on the standalone API write path only, and
// deliberately not in core/workflow validation (Decision 3): workflows
// already on a branch or in the projection — and every workflow SMP's
// embedded WorkflowWriter writes — keep loading and validating unchanged.
// The SSE writer refuses unsafe field values on its own
// (formatSSEEvent), which also covers workflows that arrive by git push
// or governed merge; this rule is defence in depth. Upper case, "." and
// "/" are refused: step IDs appear in URL paths
// (/runs/{run_id}/steps/{step_id}/assign). The 64-character cap matches
// the trace-ID and repository-ID caps and bounds event-ID growth.
var workflowIdentifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// workflowIdentifierCheckPath is the path the identifier check decodes a
// request body under. The body, not the path, carries the identifiers.
const workflowIdentifierCheckPath = workflow.WorkflowsDir + "/identifier-check.yaml"

// maxIdentifierEcho bounds how much of a refused identifier an error
// message (or a log line, warnUnsafeSSEFields) repeats.
const maxIdentifierEcho = 80

// boundedEcho returns v, or its first maxIdentifierEcho bytes cut back to
// a rune boundary plus "...", so an attacker-sized identifier cannot grow
// an error message or a log line. Callers quote the result (%q, slog).
func boundedEcho(v string) string {
	if len(v) <= maxIdentifierEcho {
		return v
	}
	cut := maxIdentifierEcho
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut] + "..."
}

// workflowIdentifierErrors returns one schema error per identifier in
// body that does not match workflowIdentifierPattern. The body is decoded
// with workflow.ProjectionDefinition — the decoder the write path itself
// uses (INIT-023 TASK-011) — so the check sees exactly the document that
// would be written. A body that does not decode returns nil: the
// workflow service owns that error (strict decoding refuses case-variant
// keys such as `ID:`, so a body cannot hide an identifier from this check
// and still be written). Empty identifiers are left to core validation
// ("required field missing").
func workflowIdentifierErrors(body string) []domain.ValidationError {
	wf, _, err := workflow.ProjectionDefinition(workflowIdentifierCheckPath, []byte(body))
	if err != nil {
		return nil
	}
	var bad []domain.ValidationError
	check := func(field, v string) {
		if v == "" || workflowIdentifierPattern.MatchString(v) {
			return
		}
		bad = append(bad, domain.ValidationError{
			RuleID:   "schema",
			Field:    field,
			Severity: "error",
			Message:  fmt.Sprintf("identifier %q must match %s", boundedEcho(v), workflowIdentifierPattern.String()),
		})
	}
	for i := range wf.Steps {
		check(fmt.Sprintf("steps[%d].id", i), wf.Steps[i].ID)
	}
	for i := range wf.DivergencePoints {
		dp := &wf.DivergencePoints[i]
		check(fmt.Sprintf("divergence_points[%d].id", i), dp.ID)
		for j := range dp.Branches {
			check(fmt.Sprintf("divergence_points[%d].branches[%d].id", i, j), dp.Branches[j].ID)
		}
	}
	for i := range wf.ConvergencePoints {
		check(fmt.Sprintf("convergence_points[%d].id", i), wf.ConvergencePoints[i].ID)
	}
	return bad
}

// checkWorkflowIdentifiers returns ErrValidationFailed (422) carrying
// workflowIdentifierErrors, or nil. POST and PUT call it right after the
// body checks, before any dispatch, so the direct-commit path and the
// planning-run path refuse the same bodies and nothing is written.
func checkWorkflowIdentifiers(body string) error {
	bad := workflowIdentifierErrors(body)
	if len(bad) == 0 {
		return nil
	}
	return domain.NewErrorWithDetail(domain.ErrValidationFailed, "workflow identifiers contain unsupported characters", bad)
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
	// Before the planning-run dispatch below, so a reviewer's planning
	// run and an operator's direct commit refuse the same bodies.
	if err := checkWorkflowIdentifiers(req.Body); err != nil {
		WriteError(w, err)
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
	// Dispatch per ADR-008:
	//   - write_context present → branch-scoped write via resolveWriteContext
	//   - no write_context + reviewer → planning run under workflow-lifecycle
	//   - no write_context + operator/admin → direct commit (bypass), tagged
	//     with a Workflow-Bypass trailer for audit
	if req.WriteContext == nil && !callerHasOperatorPrivileges(ctx) {
		starter := s.wfPlanningStarterFrom(ctx)
		if starter == nil {
			// No planning starter wired — refuse rather than silently
			// demoting the reviewer to a direct commit that would skip
			// both the lifecycle review AND the bypass trailer. Bootstrap
			// recovery is the operator-bypass path (ADR-008 §5).
			WriteError(w, domain.NewError(domain.ErrUnavailable,
				"workflow planning run starter not configured; reviewer writes require the governance flow"))
			return
		}
		planning, err := starter.StartWorkflowPlanningRun(ctx, req.ID, req.Body)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]any{
			"run_id":                planning.RunID,
			"branch_name":           planning.BranchName,
			"workflow_id":           req.ID,
			"workflow_path":         planning.TaskPath,
			"governing_workflow_id": planning.WorkflowID,
			"status":                planning.Status,
			"mode":                  planning.Mode,
			"trace_id":              planning.TraceID,
			"write_mode":            "planning",
		})
		return
	}
	if req.WriteContext != nil {
		// ADR-009 §4 names both artifact and workflow endpoints as
		// carrying write_context.override, but enforcement is currently
		// wired only on the Artifact Service (TASK-001 of this epic).
		// workflow.Service does not consult branchprotect.Policy, so an
		// accepted override here would silently succeed without the
		// mandatory audit event or commit trailer — worse than rejecting.
		// Operators needing a direct-commit write to a workflow already
		// have the ADR-008 operator-bypass path (omit write_context as
		// operator+ role, which adds a Workflow-Bypass trailer). Wiring
		// branchprotect into workflow.Service is tracked as follow-up
		// work; until then this returns 400 with the bypass hint.
		if req.WriteContext.Override {
			WriteError(w, domain.NewError(domain.ErrInvalidParams,
				"write_context.override is not yet supported on workflow endpoints (use ADR-008 operator bypass: omit write_context entirely as operator+ role)"))
			return
		}
		branch, _, err := s.resolveWriteContext(ctx, req.WriteContext)
		if err != nil {
			WriteError(w, err)
			return
		}
		if branch != "" {
			ctx = workflow.WithWriteContext(ctx, workflow.WriteContext{Branch: branch})
		}
	} else if callerHasOperatorPrivileges(ctx) {
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
	if err := checkWorkflowIdentifiers(req.Body); err != nil {
		WriteError(w, err)
		return
	}

	svc, ok := s.needWorkflows(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	if req.WriteContext != nil {
		// See handleWorkflowCreate for the rationale; override on the
		// workflow surface is rejected until workflow.Service consults
		// branchprotect.Policy.
		if req.WriteContext.Override {
			WriteError(w, domain.NewError(domain.ErrInvalidParams,
				"write_context.override is not yet supported on workflow endpoints (use ADR-008 operator bypass: omit write_context entirely as operator+ role)"))
			return
		}
		branch, _, err := s.resolveWriteContext(ctx, req.WriteContext)
		if err != nil {
			WriteError(w, err)
			return
		}
		if branch != "" {
			ctx = workflow.WithWriteContext(ctx, workflow.WriteContext{Branch: branch})
		}
	} else if callerHasOperatorPrivileges(ctx) {
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
	// Report the gateway's identifier rule too, so /validate never passes
	// a body that POST and PUT refuse.
	if bad := workflowIdentifierErrors(req.Body); len(bad) > 0 {
		result.Errors = append(result.Errors, bad...)
		result.Status = "failed"
	}
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
