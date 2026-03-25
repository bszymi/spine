package artifact

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// AcceptTask records an acceptance outcome on a task artifact.
// The acceptance and rationale are written into the YAML front matter
// and committed to Git as a governed outcome.
func (s *Service) AcceptTask(ctx context.Context, path, rationale string) (*domain.Artifact, error) {
	return s.setAcceptance(ctx, path, domain.AcceptanceApproved, rationale)
}

// RejectTask records a rejection outcome on a task artifact.
// acceptance must be AcceptanceRejectedWithFollowup or AcceptanceRejectedClosed.
func (s *Service) RejectTask(ctx context.Context, path string, acceptance domain.TaskAcceptance, rationale string) (*domain.Artifact, error) {
	if acceptance != domain.AcceptanceRejectedWithFollowup && acceptance != domain.AcceptanceRejectedClosed {
		return nil, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("invalid rejection type: %s", acceptance))
	}
	return s.setAcceptance(ctx, path, acceptance, rationale)
}

func (s *Service) setAcceptance(ctx context.Context, path string, acceptance domain.TaskAcceptance, rationale string) (*domain.Artifact, error) {
	log := observe.Logger(ctx)

	// Read the current artifact.
	art, err := s.Read(ctx, path, "HEAD")
	if err != nil {
		return nil, err
	}

	// Only tasks can be accepted/rejected.
	if art.Type != domain.ArtifactTypeTask {
		return nil, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("acceptance only applies to Task artifacts, got %s", art.Type))
	}

	// Prevent re-acceptance of already-decided tasks.
	if art.Acceptance != "" {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("task already has acceptance: %s", art.Acceptance))
	}

	// Read the raw file to modify front matter.
	fullPath, err := s.safePath(path)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("read file %s: %v", path, err))
	}

	// Insert acceptance fields into front matter.
	updated := insertAcceptanceFields(string(raw), string(acceptance), rationale)

	log.Info("recording task acceptance",
		"path", path,
		"acceptance", acceptance,
	)

	// Write and commit via Update.
	return s.Update(ctx, path, updated)
}

// insertAcceptanceFields adds acceptance and acceptance_rationale to YAML front matter.
func insertAcceptanceFields(content, acceptance, rationale string) string {
	lines := strings.Split(content, "\n")
	var result []string
	inFrontMatter := false
	inserted := false

	for _, line := range lines {
		if line == "---" {
			if !inFrontMatter {
				inFrontMatter = true
				result = append(result, line)
				continue
			}
			// Closing ---: insert acceptance before it.
			if !inserted {
				result = append(result, fmt.Sprintf("acceptance: %s", acceptance))
				if rationale != "" {
					result = append(result, fmt.Sprintf("acceptance_rationale: \"%s\"", rationale))
				}
				inserted = true
			}
			inFrontMatter = false
		}
		result = append(result, line)
	}

	return strings.Join(result, "\n")
}
