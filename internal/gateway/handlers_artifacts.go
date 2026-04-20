package gateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

// isWorkflowTarget reports whether a path points at a workflow definition.
// Per ADR-007, generic artifact write operations must reject such targets.
func isWorkflowTarget(path string) bool {
	p := strings.TrimPrefix(path, "/")
	return strings.HasPrefix(p, "workflows/")
}

// workflowRejection returns the canonical 400 error that generic artifact
// write handlers surface when a caller tries to write a workflow definition.
// The detail payload names the correct workflow.* operation per ADR-007.
func workflowRejection(op string) error {
	return domain.NewErrorWithDetail(domain.ErrInvalidParams,
		"workflow definitions must be written through workflow."+op+" (see ADR-007)",
		map[string]string{
			"use_operation": "workflow." + op,
			"adr":           "ADR-007",
		})
}

type writeContextRequest struct {
	RunID    string `json:"run_id"`
	TaskPath string `json:"task_path"`
	// Override opts the caller into the branch-protection override surface
	// for this single operation (ADR-009 §4). The policy evaluator gates
	// effective use on Actor.Role ≥ operator — contributors who set this
	// flag see a distinct "override not authorised" reason. Omitted ≡ false.
	Override bool `json:"override,omitempty"`
}

type artifactCreateRequest struct {
	Path         string               `json:"path"`
	Content      string               `json:"content"`
	WriteContext *writeContextRequest `json:"write_context,omitempty"`
}

type artifactUpdateRequest struct {
	Content      string               `json:"content"`
	WriteContext *writeContextRequest `json:"write_context,omitempty"`
}

func (s *Server) handleArtifactCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "artifact.create") {
		return
	}

	req, ok := decodeBody[artifactCreateRequest](w, r)
	if !ok {
		return
	}
	if req.Path == "" || req.Content == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "path and content required"))
		return
	}
	if isWorkflowTarget(req.Path) {
		WriteError(w, workflowRejection("create"))
		return
	}
	if err := validateArtifactContent(req.Content); err != nil {
		WriteError(w, err)
		return
	}

	if s.artifactsFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return
	}

	ctx := r.Context()
	if req.WriteContext != nil {
		branch, override, err := s.resolveWriteContext(ctx, req.WriteContext)
		if err != nil {
			WriteError(w, err)
			return
		}
		if branch != "" || override {
			ctx = artifact.WithWriteContext(ctx, artifact.WriteContext{Branch: branch, Override: override})
		}
	}
	result, err := s.artifactsFrom(r.Context()).Create(ctx, req.Path, req.Content)
	if err != nil {
		WriteError(w, err)
		return
	}

	writeMode := "authoritative"
	if req.WriteContext != nil && req.WriteContext.RunID != "" {
		writeMode = "proposed"
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"artifact_path": result.Artifact.Path,
		"artifact_id":   result.Artifact.ID,
		"artifact_type": result.Artifact.Type,
		"title":         result.Artifact.Title,
		"status":        result.Artifact.Status,
		"commit_sha":    result.CommitSHA,
		"write_mode":    writeMode,
		"trace_id":      observe.TraceID(r.Context()),
	})
}

func (s *Server) handleArtifactList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "artifact.list") {
		return
	}

	limit, cursor := parsePagination(r)
	query := store.ArtifactQuery{
		Type:       r.URL.Query().Get("type"),
		Status:     r.URL.Query().Get("status"),
		ParentPath: r.URL.Query().Get("parent_path"),
		Search:     r.URL.Query().Get("search"),
		Limit:      limit,
		Cursor:     cursor,
	}

	if _, ok := s.needStore(w, r); !ok {
		return
	}

	result, err := s.storeFrom(r.Context()).QueryArtifacts(r.Context(), query)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"items":       result.Items,
		"next_cursor": result.NextCursor,
		"has_more":    result.HasMore,
	})
}

// handleArtifactWildcard dispatches artifact requests with slash-containing paths.
func (s *Server) handleArtifactWildcard(w http.ResponseWriter, r *http.Request) {
	path, suffix := extractArtifactPath(r)

	switch {
	case suffix == "/validate" && r.Method == http.MethodPost:
		s.handleArtifactValidate(w, r, path)
	case suffix == "/links" && r.Method == http.MethodGet:
		s.handleArtifactLinks(w, r, path)
	case suffix == "" && r.Method == http.MethodGet:
		s.handleArtifactRead(w, r, path)
	case suffix == "" && r.Method == http.MethodPut:
		s.handleArtifactUpdate(w, r, path)
	default:
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
	}
}

func (s *Server) handleArtifactRead(w http.ResponseWriter, r *http.Request, path string) {
	if !s.authorize(w, r, "artifact.read") {
		return
	}

	if s.artifactsFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return
	}

	ref := r.URL.Query().Get("ref")
	if err := git.ValidateRef(ref); err != nil {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, err.Error()))
		return
	}
	a, err := s.artifactsFrom(r.Context()).Read(r.Context(), path, ref)
	if err != nil {
		WriteError(w, err)
		return
	}

	// Resolve source_commit from the ref used for reading.
	var sourceCommit string
	if ref != "" {
		sourceCommit = ref
	} else if s.gitFrom(r.Context()) != nil {
		if sha, err := s.gitFrom(r.Context()).Head(r.Context()); err == nil {
			sourceCommit = sha
		}
	}

	// Per ADR-007, workflow definitions read via the generic artifact endpoint
	// return summary metadata only — the executable body is served exclusively
	// by workflow.read.
	if isWorkflowTarget(path) {
		WriteJSON(w, http.StatusOK, map[string]any{
			"artifact_path": a.Path,
			"artifact_id":   a.ID,
			"artifact_type": a.Type,
			"status":        a.Status,
			"title":         a.Title,
			"metadata":      a.Metadata,
			"source_commit": sourceCommit,
			"note":          "workflow definition body omitted; use GET /workflows/{id}",
		})
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"artifact_path": a.Path,
		"artifact_id":   a.ID,
		"artifact_type": a.Type,
		"status":        a.Status,
		"title":         a.Title,
		"metadata":      a.Metadata,
		"content":       a.Content,
		"source_commit": sourceCommit,
	})
}

func (s *Server) handleArtifactUpdate(w http.ResponseWriter, r *http.Request, path string) {
	if !s.authorize(w, r, "artifact.update") {
		return
	}

	if isWorkflowTarget(path) {
		WriteError(w, workflowRejection("update"))
		return
	}

	req, ok := decodeBody[artifactUpdateRequest](w, r)
	if !ok {
		return
	}
	if req.Content == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "content required"))
		return
	}
	if err := validateArtifactContent(req.Content); err != nil {
		WriteError(w, err)
		return
	}

	if s.artifactsFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return
	}

	ctx := r.Context()
	if req.WriteContext != nil {
		// NOTE: Planning runs may update artifacts they created on their own branch
		// (e.g., during draft/rework). Full ADR-006 §8 enforcement (blocking updates
		// to pre-existing artifacts from main) requires distinguishing branch-local
		// vs inherited files, which is deferred to a future task.
		branch, override, err := s.resolveWriteContext(ctx, req.WriteContext)
		if err != nil {
			WriteError(w, err)
			return
		}
		if branch != "" || override {
			ctx = artifact.WithWriteContext(ctx, artifact.WriteContext{Branch: branch, Override: override})
		}
	}
	result, err := s.artifactsFrom(r.Context()).Update(ctx, path, req.Content)
	if err != nil {
		WriteError(w, err)
		return
	}

	writeMode := "authoritative"
	if req.WriteContext != nil && req.WriteContext.RunID != "" {
		writeMode = "proposed"
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"artifact_path": result.Artifact.Path,
		"artifact_id":   result.Artifact.ID,
		"artifact_type": result.Artifact.Type,
		"title":         result.Artifact.Title,
		"status":        result.Artifact.Status,
		"commit_sha":    result.CommitSHA,
		"write_mode":    writeMode,
		"trace_id":      observe.TraceID(r.Context()),
	})
}

func (s *Server) handleArtifactValidate(w http.ResponseWriter, r *http.Request, path string) {
	if !s.authorize(w, r, "artifact.validate") {
		return
	}

	if s.artifactsFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return
	}

	// Check for optional inline content in request body.
	var req struct {
		Content string `json:"content"`
	}
	_ = decodeJSON(r, &req) // body is optional

	var a *domain.Artifact
	if req.Content != "" {
		if err := validateArtifactContent(req.Content); err != nil {
			WriteError(w, err)
			return
		}
		// Dry-run: parse inline content without saving.
		parsed, err := artifact.Parse(path, []byte(req.Content))
		if err != nil {
			// Return a failed validation result rather than an error.
			WriteJSON(w, http.StatusOK, domain.ValidationResult{
				Status: "failed",
				Errors: []domain.ValidationError{{
					ArtifactPath: path,
					Severity:     "error",
					Message:      err.Error(),
				}},
			})
			return
		}
		a = parsed
	} else {
		// Default: read the stored artifact.
		stored, err := s.artifactsFrom(r.Context()).Read(r.Context(), path, "")
		if err != nil {
			WriteError(w, err)
			return
		}
		a = stored
	}

	result := artifact.Validate(a)
	WriteJSON(w, http.StatusOK, result)
}

func (s *Server) handleArtifactLinks(w http.ResponseWriter, r *http.Request, path string) {
	if !s.authorize(w, r, "artifact.links") {
		return
	}

	if _, ok := s.needStore(w, r); !ok {
		return
	}

	linkTypeFilter := r.URL.Query().Get("link_type")
	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "outgoing"
	}
	if direction != "outgoing" && direction != "incoming" && direction != "both" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "direction must be outgoing, incoming, or both"))
		return
	}

	type linkEntry struct {
		Direction  string `json:"direction"`
		LinkType   string `json:"link_type"`
		TargetPath string `json:"target_path"`
	}

	var entries []linkEntry

	// Outgoing links (source = this artifact)
	if direction == "outgoing" || direction == "both" {
		outgoing, err := s.storeFrom(r.Context()).QueryArtifactLinks(r.Context(), path)
		if err != nil {
			WriteError(w, err)
			return
		}
		for _, l := range outgoing {
			if linkTypeFilter != "" && l.LinkType != linkTypeFilter {
				continue
			}
			entries = append(entries, linkEntry{
				Direction:  "outgoing",
				LinkType:   l.LinkType,
				TargetPath: l.TargetPath,
			})
		}
	}

	// Incoming links (target = this artifact)
	if direction == "incoming" || direction == "both" {
		incoming, err := s.storeFrom(r.Context()).QueryArtifactLinksByTarget(r.Context(), path)
		if err != nil {
			WriteError(w, err)
			return
		}
		for _, l := range incoming {
			if linkTypeFilter != "" && l.LinkType != linkTypeFilter {
				continue
			}
			entries = append(entries, linkEntry{
				Direction:  "incoming",
				LinkType:   l.LinkType,
				TargetPath: l.SourcePath,
			})
		}
	}

	if entries == nil {
		entries = []linkEntry{}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"artifact_path": path,
		"links":         entries,
	})
}

// resolveWriteContext resolves a WriteContext request (run_id + task_path)
// into the destination branch and the override flag. Empty run_id yields
// ("", override, nil) — an authoritative-branch write, the case where the
// override flag is actually meaningful. Planning runs skip task_path
// validation per ADR-006 §7.
func (s *Server) resolveWriteContext(ctx context.Context, wc *writeContextRequest) (string, bool, error) {
	if wc.RunID == "" {
		return "", wc.Override, nil
	}
	if s.storeFrom(ctx) == nil {
		return "", false, domain.NewError(domain.ErrUnavailable, "store not configured")
	}
	run, err := s.storeFrom(ctx).GetRun(ctx, wc.RunID)
	if err != nil {
		return "", false, fmt.Errorf("resolve write context: %w", err)
	}
	if run.Status != domain.RunStatusActive {
		return "", false, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("run %s is not active (status: %s)", wc.RunID, run.Status))
	}
	if run.BranchName == "" {
		return "", false, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("run %s has no branch", wc.RunID))
	}

	// Planning runs: skip task_path validation. The run owns a constrained
	// creation scope on the branch for multi-artifact writes.
	if run.Mode == domain.RunModePlanning {
		return run.BranchName, wc.Override, nil
	}

	// Standard runs: require task_path and validate it matches the run.
	if wc.TaskPath == "" {
		return "", false, domain.NewError(domain.ErrInvalidParams, "write_context.task_path is required when run_id is provided")
	}
	if run.TaskPath != wc.TaskPath {
		return "", false, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("task_path mismatch: run %s belongs to %s, not %s", wc.RunID, run.TaskPath, wc.TaskPath))
	}
	return run.BranchName, wc.Override, nil
}
