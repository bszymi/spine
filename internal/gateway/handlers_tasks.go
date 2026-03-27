package gateway

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// taskActionStatus maps governance actions to target statuses.
var taskActionStatus = map[string]domain.ArtifactStatus{
	"accept":    domain.StatusCompleted,
	"reject":    domain.StatusRejected,
	"cancel":    domain.StatusCancelled,
	"abandon":   domain.StatusAbandoned,
	"supersede": domain.StatusSuperseded,
}

// handleTaskWildcard dispatches task governance actions with slash-containing paths.
func (s *Server) handleTaskWildcard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
		return
	}

	taskPath, action := extractTaskAction(r)
	targetStatus, ok := taskActionStatus[action]
	if !ok {
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
		return
	}

	if !s.authorize(w, r, auth.Operation("task."+action)) {
		return
	}

	if s.artifacts == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return
	}

	// For accept/reject, delegate to the acceptance pipeline which records
	// acceptance fields in addition to status changes.
	if action == "accept" {
		var req struct {
			Rationale  string   `json:"rationale"`
			ThreadRefs []string `json:"thread_refs,omitempty"`
		}
		_ = decodeJSON(r, &req) // rationale is optional

		rationale := req.Rationale
		if len(req.ThreadRefs) > 0 {
			rationale = appendThreadRefs(rationale, req.ThreadRefs)
		}

		result, err := s.artifacts.AcceptTask(r.Context(), taskPath, rationale)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"task_path":  result.Artifact.Path,
			"status":     result.Artifact.Status,
			"acceptance": result.Artifact.Acceptance,
			"commit_sha": result.CommitSHA,
			"trace_id":   observe.TraceID(r.Context()),
		})
		return
	}

	if action == "reject" {
		var req struct {
			Acceptance string   `json:"acceptance"`
			Rationale  string   `json:"rationale"`
			ThreadRefs []string `json:"thread_refs,omitempty"`
		}
		_ = decodeJSON(r, &req) // body is optional for reject
		acceptance := domain.TaskAcceptance(req.Acceptance)
		if acceptance == "" {
			acceptance = domain.AcceptanceRejectedClosed
		}
		rationale := req.Rationale
		if len(req.ThreadRefs) > 0 {
			rationale = appendThreadRefs(rationale, req.ThreadRefs)
		}
		result, err := s.artifacts.RejectTask(r.Context(), taskPath, acceptance, rationale)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"task_path":  result.Artifact.Path,
			"status":     result.Artifact.Status,
			"acceptance": result.Artifact.Acceptance,
			"commit_sha": result.CommitSHA,
			"trace_id":   observe.TraceID(r.Context()),
		})
		return
	}

	// For supersede, parse successor_path from the body.
	if action == "supersede" {
		var req struct {
			SuccessorPath string `json:"successor_path"`
		}
		_ = decodeJSON(r, &req) // successor_path is optional per spec
	}

	// Other actions (cancel, abandon, supersede) — status-only transition.

	// Read current task — need raw content with front matter for status update
	a, err := s.artifacts.Read(r.Context(), taskPath, "")
	if err != nil {
		WriteError(w, err)
		return
	}

	if a.Type != domain.ArtifactTypeTask {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "governance actions only apply to tasks"))
		return
	}

	// Read raw file content (with front matter) via Git
	if s.git == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "git client not configured"))
		return
	}
	rawContent, err := s.git.ReadFile(r.Context(), "HEAD", taskPath)
	if err != nil {
		WriteError(w, err)
		return
	}

	// Update the status in the raw content
	updatedContent := updateFrontMatterStatus(string(rawContent), string(targetStatus))

	result, err := s.artifacts.Update(r.Context(), taskPath, updatedContent)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"task_path":  result.Artifact.Path,
		"status":     result.Artifact.Status,
		"commit_sha": result.CommitSHA,
		"trace_id":   observe.TraceID(r.Context()),
	})
}

// statusRegexp matches the status field in YAML front matter.
var statusRegexp = regexp.MustCompile(`(?m)^status:\s*.*$`)

// appendThreadRefs appends discussion thread references to a rationale string.
func appendThreadRefs(rationale string, refs []string) string {
	suffix := "\n\nDiscussion threads: " + strings.Join(refs, ", ")
	return rationale + suffix
}

// updateFrontMatterStatus replaces the status field in markdown front matter content.
func updateFrontMatterStatus(content, newStatus string) string {
	// Find the front matter boundaries
	if !strings.HasPrefix(content, "---") {
		return content
	}

	endIdx := strings.Index(content[3:], "---")
	if endIdx == -1 {
		return content
	}
	endIdx += 3 // adjust for the offset

	frontMatter := content[:endIdx]
	rest := content[endIdx:]

	updated := statusRegexp.ReplaceAllString(frontMatter, "status: "+newStatus)
	return updated + rest
}
