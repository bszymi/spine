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

	// Read current task
	a, err := s.artifacts.Read(r.Context(), taskPath, "")
	if err != nil {
		WriteError(w, err)
		return
	}

	if a.Type != domain.ArtifactTypeTask {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "governance actions only apply to tasks"))
		return
	}

	// Update the status in content
	updatedContent := updateFrontMatterStatus(a.Content, string(targetStatus))

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
