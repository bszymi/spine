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
			Rationale string `json:"rationale"`
		}
		_ = decodeJSON(r, &req) // rationale is optional

		art, err := s.artifacts.AcceptTask(r.Context(), taskPath, req.Rationale)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"artifact_path": art.Path,
			"artifact_id":   art.ID,
			"status":        art.Status,
			"acceptance":    art.Acceptance,
			"action":        action,
			"trace_id":      observe.TraceID(r.Context()),
		})
		return
	}

	if action == "reject" {
		var req struct {
			Acceptance string `json:"acceptance"`
			Rationale  string `json:"rationale"`
		}
		_ = decodeJSON(r, &req) // body is optional for reject
		acceptance := domain.TaskAcceptance(req.Acceptance)
		if acceptance == "" {
			acceptance = domain.AcceptanceRejectedClosed
		}
		art, err := s.artifacts.RejectTask(r.Context(), taskPath, acceptance, req.Rationale)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"artifact_path": art.Path,
			"artifact_id":   art.ID,
			"status":        art.Status,
			"acceptance":    art.Acceptance,
			"action":        action,
			"trace_id":      observe.TraceID(r.Context()),
		})
		return
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

	updated, err := s.artifacts.Update(r.Context(), taskPath, updatedContent)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"artifact_path": updated.Path,
		"artifact_id":   updated.ID,
		"status":        updated.Status,
		"action":        action,
		"trace_id":      observe.TraceID(r.Context()),
	})
}

// statusRegexp matches the status field in YAML front matter.
var statusRegexp = regexp.MustCompile(`(?m)^status:\s*.*$`)

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
