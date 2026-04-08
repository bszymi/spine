package gateway

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// artifactEntryCreateRequest is the request body for POST /artifacts/entry.
type artifactEntryCreateRequest struct {
	ArtifactType string `json:"artifact_type"` // Task, Epic, or Initiative
	Parent       string `json:"parent"`        // parent artifact ID (e.g., "EPIC-003") — required for Task/Epic
	Title        string `json:"title"`         // human-readable title
}

// handleArtifactEntryCreate handles POST /artifacts/entry.
// It allocates the next ID, builds artifact content, and starts a planning run.
func (s *Server) handleArtifactEntryCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "artifact.create") {
		return
	}

	var req artifactEntryCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}

	// Validate inputs.
	if req.Title == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "title is required"))
		return
	}

	artType := domain.ArtifactType(req.ArtifactType)
	switch artType {
	case domain.ArtifactTypeTask, domain.ArtifactTypeEpic, domain.ArtifactTypeInitiative:
		// OK — supported types.
	default:
		WriteError(w, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("unsupported artifact_type: %s (must be Task, Epic, or Initiative)", req.ArtifactType)))
		return
	}

	// Validate parent requirement.
	if artType == domain.ArtifactTypeTask && req.Parent == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "parent is required for Task (provide parent epic ID)"))
		return
	}
	if artType == domain.ArtifactTypeEpic && req.Parent == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "parent is required for Epic (provide parent initiative ID)"))
		return
	}
	if artType == domain.ArtifactTypeInitiative && req.Parent != "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "parent must not be set for Initiative"))
		return
	}

	// Check dependencies.
	if s.planningRunStarter == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "planning run starter not configured"))
		return
	}
	artSvc := s.artifactsFrom(r.Context())
	if artSvc == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return
	}
	gitReader := s.gitFrom(r.Context())
	if gitReader == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "git reader not configured"))
		return
	}

	ctx := r.Context()

	// Resolve parent directory path.
	var parentDir string
	var parentArtifactPath string
	if req.Parent != "" {
		var err error
		parentArtifactPath, parentDir, err = resolveParentFromList(ctx, artSvc, req.Parent, artType)
		if err != nil {
			WriteError(w, err)
			return
		}
	} else {
		parentDir = "initiatives"
	}

	// Allocate next ID using GitReader (which implements git.GitClient).
	nextID, err := artifact.NextID(ctx, gitReader, parentDir, artType, "HEAD")
	if err != nil {
		WriteError(w, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("allocate next ID: %v", err)))
		return
	}

	// Build slug and path.
	slug := artifact.Slugify(req.Title)
	if slug == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "title produces an empty slug"))
		return
	}
	artifactPath := artifact.BuildArtifactPath(artType, nextID, slug, parentDir)

	// Build initial artifact content.
	content := buildInitialContent(artType, nextID, req.Title, parentArtifactPath)

	// Start the planning run.
	result, err := s.planningRunStarter.StartPlanningRun(ctx, artifactPath, content)
	if err != nil {
		WriteError(w, err)
		return
	}

	gatewayTraceID := observe.TraceID(ctx)
	if gatewayTraceID == "" {
		gatewayTraceID = result.TraceID
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"run_id":        result.RunID,
		"artifact_id":   nextID,
		"artifact_path": artifactPath,
		"branch":        result.BranchName,
		"workflow_id":   result.WorkflowID,
		"trace_id":      gatewayTraceID,
	})
}

// resolveParentFromList finds a parent artifact by ID using the artifact service's List.
// Returns the parent's artifact path and the child directory path.
func resolveParentFromList(ctx context.Context, artSvc ArtifactService, parentID string, childType domain.ArtifactType) (parentPath, childDir string, err error) {
	artifacts, err := artSvc.List(ctx, "HEAD")
	if err != nil {
		return "", "", domain.NewError(domain.ErrInternal,
			fmt.Sprintf("list artifacts: %v", err))
	}

	upperID := strings.ToUpper(parentID)
	for _, a := range artifacts {
		if strings.ToUpper(a.ID) == upperID {
			// Validate parent type matches child expectation.
			switch childType {
			case domain.ArtifactTypeTask:
				if a.Type != domain.ArtifactTypeEpic {
					return "", "", domain.NewError(domain.ErrInvalidParams,
						fmt.Sprintf("parent %s is %s, but Task requires an Epic parent", parentID, a.Type))
				}
			case domain.ArtifactTypeEpic:
				if a.Type != domain.ArtifactTypeInitiative {
					return "", "", domain.NewError(domain.ErrInvalidParams,
						fmt.Sprintf("parent %s is %s, but Epic requires an Initiative parent", parentID, a.Type))
				}
			}

			// Found the parent. Derive the child directory.
			parentPath = a.Path
			dir := filepath.Dir(parentPath)

			switch childType {
			case domain.ArtifactTypeTask:
				childDir = filepath.Join(dir, "tasks")
			case domain.ArtifactTypeEpic:
				childDir = filepath.Join(dir, "epics")
			default:
				childDir = dir
			}
			return parentPath, childDir, nil
		}
	}

	return "", "", domain.NewError(domain.ErrNotFound,
		fmt.Sprintf("parent artifact not found: %s", parentID))
}

// buildInitialContent creates the markdown content for a new artifact in Draft status.
// Includes all required front-matter fields per artifact schema validation.
func buildInitialContent(artType domain.ArtifactType, id, title, parentArtifactPath string) string {
	today := time.Now().Format("2006-01-02")

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("id: %s\n", id))
	b.WriteString(fmt.Sprintf("type: %s\n", artType))
	b.WriteString(fmt.Sprintf("title: %s\n", title))
	b.WriteString("status: Draft\n")
	b.WriteString(fmt.Sprintf("created: %s\n", today))
	b.WriteString(fmt.Sprintf("last_updated: %s\n", today))

	if parentArtifactPath != "" {
		// Use canonical absolute path with leading /.
		target := parentArtifactPath
		if !strings.HasPrefix(target, "/") {
			target = "/" + target
		}

		// Add type-specific parent metadata fields required by validation.
		switch artType {
		case domain.ArtifactTypeTask:
			// Tasks need epic and initiative metadata.
			b.WriteString(fmt.Sprintf("epic: %s\n", target))
		case domain.ArtifactTypeEpic:
			// Epics need initiative metadata.
			b.WriteString(fmt.Sprintf("initiative: %s\n", target))
		}

		b.WriteString("links:\n")
		b.WriteString("  - type: parent\n")
		b.WriteString(fmt.Sprintf("    target: %s\n", target))
	}

	b.WriteString("---\n\n")
	b.WriteString(fmt.Sprintf("# %s — %s\n", id, title))

	return b.String()
}
