package artifact

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// CreateSuccessorTask creates a follow-up task linked to a rejected task.
// The successor inherits the epic and initiative from the original.
// Returns the created successor artifact.
func (s *Service) CreateSuccessorTask(ctx context.Context, rejectedPath, rationale string) (*domain.Artifact, error) {
	log := observe.Logger(ctx)

	// Read the rejected task.
	original, err := s.Read(ctx, rejectedPath, "HEAD")
	if err != nil {
		return nil, err
	}

	if original.Type != domain.ArtifactTypeTask {
		return nil, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("successor creation only applies to Task artifacts, got %s", original.Type))
	}

	// Generate successor path in the same directory.
	successorID := original.ID + "-followup"
	dir := filepath.Dir(rejectedPath)
	successorPath := filepath.Join(dir, strings.ToLower(successorID)+".md")

	// Build successor content.
	content := buildSuccessorContent(original, successorID, rejectedPath, rationale)

	log.Info("creating successor task",
		"rejected_path", rejectedPath,
		"successor_path", successorPath,
	)

	// Create the successor artifact.
	successor, err := s.Create(ctx, successorPath, content)
	if err != nil {
		return nil, fmt.Errorf("create successor: %w", err)
	}

	// Add follow_up_from link to the rejected task.
	if err := s.addLinkToArtifact(ctx, rejectedPath, "follow_up_from", successorPath); err != nil {
		log.Warn("failed to add follow_up_from link to rejected task",
			"rejected", rejectedPath, "successor", successorPath, "error", err)
	}

	return successor, nil
}

func buildSuccessorContent(original *domain.Artifact, successorID, rejectedPath, rationale string) string {
	epic := original.Metadata["epic"]
	initiative := original.Metadata["initiative"]

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("id: %s\n", successorID))
	b.WriteString("type: Task\n")
	b.WriteString(fmt.Sprintf("title: \"Follow-up: %s\"\n", original.Title))
	b.WriteString("status: Draft\n")
	if epic != "" {
		b.WriteString(fmt.Sprintf("epic: %s\n", epic))
	}
	if initiative != "" {
		b.WriteString(fmt.Sprintf("initiative: %s\n", initiative))
	}
	b.WriteString("links:\n")
	b.WriteString(fmt.Sprintf("  - type: follow_up_to\n    target: %s\n", rejectedPath))
	b.WriteString("---\n\n")
	b.WriteString(fmt.Sprintf("# %s — Follow-up: %s\n\n", successorID, original.Title))
	if rationale != "" {
		b.WriteString(fmt.Sprintf("## Rejection Rationale\n\n%s\n\n", rationale))
	}
	b.WriteString("## Purpose\n\nFollow-up task created from rejection of the original task.\n")

	return b.String()
}

// addLinkToArtifact reads an artifact, adds a link, and commits the update.
func (s *Service) addLinkToArtifact(ctx context.Context, path, linkType, target string) error {
	fullPath, err := s.safePath(path)
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("read file %s: %w", path, err)
	}

	// Insert the link before the closing --- of front matter.
	updated := insertLink(string(raw), linkType, target)
	_, updateErr := s.Update(ctx, path, updated)
	return updateErr
}

// insertLink adds a link entry before the closing --- of YAML front matter.
func insertLink(content, linkType, target string) string {
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
			// Closing ---: insert link before it.
			if !inserted {
				result = append(result, fmt.Sprintf("  - type: %s", linkType))
				result = append(result, fmt.Sprintf("    target: %s", target))
				inserted = true
			}
			inFrontMatter = false
		}
		result = append(result, line)
	}

	return strings.Join(result, "\n")
}
