package artifact

import (
	"fmt"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"gopkg.in/yaml.v3"
)

// Bounds on front-matter parsing. Real artifacts sit well under 4 KB
// with a handful of fields; these limits reject adversarial YAML
// (deep nesting, billion-laughs alias expansion, long scalars) before
// the decoder can allocate unbounded memory. The thresholds are
// generous enough that legitimate artifacts will never hit them.
const (
	maxFrontMatterBytes = 64 * 1024
	maxYAMLNodes        = 10_000
	maxYAMLDepth        = 64
	maxYAMLAliases      = 100
)

// frontMatter represents the raw YAML front matter before mapping to domain types.
type frontMatter struct {
	ID                  string         `yaml:"id"`
	Type                string         `yaml:"type"`
	Title               string         `yaml:"title"`
	Status              string         `yaml:"status"`
	Owner               string         `yaml:"owner"`
	Created             string         `yaml:"created"`
	LastUpdated         string         `yaml:"last_updated"`
	Version             string         `yaml:"version"`
	Initiative          string         `yaml:"initiative"`
	Epic                string         `yaml:"epic"`
	WorkType            string         `yaml:"work_type"`
	Acceptance          string         `yaml:"acceptance"`
	AcceptanceRationale string         `yaml:"acceptance_rationale"`
	Date                string         `yaml:"date"`
	DecisionMakers      string         `yaml:"decision_makers"`
	Links               []rawLink      `yaml:"links"`
	Extra               map[string]any `yaml:"-"` // captured separately
}

type rawLink struct {
	Type   string `yaml:"type"`
	Target string `yaml:"target"`
}

// Parse parses a Markdown file with YAML front matter into a domain Artifact.
// The path parameter is the repository-relative path of the file.
func Parse(path string, content []byte) (*domain.Artifact, error) {
	fm, body, err := splitFrontMatter(content)
	if err != nil {
		return nil, &ParseError{Path: path, Message: err.Error()}
	}

	root, err := decodeBoundedYAML(fm)
	if err != nil {
		return nil, &ParseError{Path: path, Message: err.Error()}
	}

	var parsed frontMatter
	if err := root.Decode(&parsed); err != nil {
		return nil, &ParseError{Path: path, Message: fmt.Sprintf("invalid YAML: %v", err)}
	}

	// Also unmarshal into a map to capture extra fields as metadata.
	// The root is reused (already bounds-checked); decode failures are
	// non-fatal since the struct decode above already succeeded.
	var metadata map[string]any
	if err := root.Decode(&metadata); err != nil {
		metadata = make(map[string]any)
	}

	// Remove known fields from metadata map
	for _, key := range []string{"id", "type", "title", "status", "owner", "created",
		"last_updated", "version", "initiative", "epic", "work_type", "acceptance",
		"acceptance_rationale", "date", "decision_makers", "links"} {
		delete(metadata, key)
	}

	artifactType := domain.ArtifactType(parsed.Type)

	links := make([]domain.Link, 0, len(parsed.Links))
	for _, rl := range parsed.Links {
		links = append(links, domain.Link{
			Type:   domain.LinkType(rl.Type),
			Target: rl.Target,
		})
	}

	// Build metadata string map from remaining fields + known optional fields
	meta := make(map[string]string)
	if parsed.Owner != "" {
		meta["owner"] = parsed.Owner
	}
	if parsed.Created != "" {
		meta["created"] = parsed.Created
	}
	if parsed.LastUpdated != "" {
		meta["last_updated"] = parsed.LastUpdated
	}
	if parsed.Version != "" {
		meta["version"] = parsed.Version
	}
	if parsed.Initiative != "" {
		meta["initiative"] = parsed.Initiative
	}
	if parsed.Epic != "" {
		meta["epic"] = parsed.Epic
	}
	if parsed.WorkType != "" {
		meta["work_type"] = parsed.WorkType
	}
	if parsed.Acceptance != "" {
		meta["acceptance"] = parsed.Acceptance
	}
	if parsed.AcceptanceRationale != "" {
		meta["acceptance_rationale"] = parsed.AcceptanceRationale
	}
	if parsed.Date != "" {
		meta["date"] = parsed.Date
	}
	if parsed.DecisionMakers != "" {
		meta["decision_makers"] = parsed.DecisionMakers
	}
	for k, v := range metadata {
		meta[k] = fmt.Sprintf("%v", v)
	}

	// Validate required fields per artifact-schema.md §5
	if parsed.Type == "" {
		return nil, &ParseError{Path: path, Message: "missing required field: type"}
	}

	// Validate artifact type is known
	validType := false
	for _, t := range domain.ValidArtifactTypes() {
		if artifactType == t {
			validType = true
			break
		}
	}
	if !validType {
		return nil, &ParseError{Path: path, Message: fmt.Sprintf("unknown artifact type: %s", parsed.Type)}
	}

	if parsed.Title == "" {
		return nil, &ParseError{Path: path, Message: "missing required field: title"}
	}
	if parsed.Status == "" {
		return nil, &ParseError{Path: path, Message: "missing required field: status"}
	}

	// Type-specific required fields
	switch artifactType {
	case domain.ArtifactTypeInitiative:
		if parsed.ID == "" {
			return nil, &ParseError{Path: path, Message: "missing required field: id (Initiative)"}
		}
		if parsed.Created == "" {
			return nil, &ParseError{Path: path, Message: "missing required field: created (Initiative)"}
		}
	case domain.ArtifactTypeEpic:
		if parsed.ID == "" {
			return nil, &ParseError{Path: path, Message: "missing required field: id (Epic)"}
		}
		if parsed.Initiative == "" {
			return nil, &ParseError{Path: path, Message: "missing required field: initiative (Epic)"}
		}
	case domain.ArtifactTypeTask:
		if parsed.ID == "" {
			return nil, &ParseError{Path: path, Message: "missing required field: id (Task)"}
		}
		if parsed.Epic == "" {
			return nil, &ParseError{Path: path, Message: "missing required field: epic (Task)"}
		}
		if parsed.Initiative == "" {
			return nil, &ParseError{Path: path, Message: "missing required field: initiative (Task)"}
		}
	case domain.ArtifactTypeADR:
		if parsed.ID == "" {
			return nil, &ParseError{Path: path, Message: "missing required field: id (ADR)"}
		}
		if parsed.Date == "" {
			return nil, &ParseError{Path: path, Message: "missing required field: date (ADR)"}
		}
		if parsed.DecisionMakers == "" {
			return nil, &ParseError{Path: path, Message: "missing required field: decision_makers (ADR)"}
		}
	}

	return &domain.Artifact{
		Path:                path,
		ID:                  parsed.ID,
		Type:                artifactType,
		Title:               parsed.Title,
		Status:              domain.ArtifactStatus(parsed.Status),
		Acceptance:          domain.TaskAcceptance(parsed.Acceptance),
		AcceptanceRationale: parsed.AcceptanceRationale,
		Links:               links,
		Metadata:            meta,
		Content:             body,
	}, nil
}

// splitFrontMatter separates YAML front matter from Markdown body.
// Front matter must start with "---\n" (or "---\r\n") and close with
// "\n---" followed by newline, \r\n, or EOF.
func splitFrontMatter(content []byte) ([]byte, string, error) {
	s := string(content)

	// Must start with "---" followed by a newline (not arbitrary characters).
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return nil, "", fmt.Errorf("file does not start with YAML front matter delimiter (---)")
	}

	// Skip the opening delimiter line.
	rest := s[strings.Index(s, "\n")+1:]

	// Find closing "---" on its own line.
	idx := strings.Index(rest, "---")
	if idx < 0 {
		return nil, "", fmt.Errorf("YAML front matter closing delimiter (---) not found")
	}
	// The closing --- must be at the start of a line (idx==0 or preceded by \n).
	if idx > 0 && rest[idx-1] != '\n' {
		return nil, "", fmt.Errorf("YAML front matter closing delimiter (---) not found")
	}
	// The closing --- must be followed by \n, \r\n, or EOF.
	afterClose := rest[idx+3:]
	if afterClose != "" && !strings.HasPrefix(afterClose, "\n") && !strings.HasPrefix(afterClose, "\r\n") {
		return nil, "", fmt.Errorf("YAML front matter closing delimiter (---) not found")
	}

	fm := rest[:idx]
	body := strings.TrimLeft(afterClose, "\r\n")

	return []byte(fm), body, nil
}

// IsArtifact returns true if the content looks like a Spine artifact
// (starts with YAML front matter containing a type field).
func IsArtifact(content []byte) bool {
	fm, _, err := splitFrontMatter(content)
	if err != nil {
		return false
	}

	root, err := decodeBoundedYAML(fm)
	if err != nil {
		return false
	}
	var check struct {
		Type string `yaml:"type"`
	}
	if err := root.Decode(&check); err != nil {
		return false
	}

	for _, t := range domain.ValidArtifactTypes() {
		if domain.ArtifactType(check.Type) == t {
			return true
		}
	}
	return false
}

// decodeBoundedYAML parses YAML into a yaml.Node and rejects documents
// that exceed the configured byte, depth, node-count, or alias-count
// caps. Aliases are *not* expanded during the initial unmarshal into a
// Node, so counting them here catches billion-laughs inputs before a
// subsequent .Decode into a typed target would explode memory.
func decodeBoundedYAML(fm []byte) (*yaml.Node, error) {
	if len(fm) > maxFrontMatterBytes {
		return nil, fmt.Errorf("YAML front matter exceeds %d byte cap (got %d)", maxFrontMatterBytes, len(fm))
	}
	var root yaml.Node
	if err := yaml.Unmarshal(fm, &root); err != nil {
		return nil, fmt.Errorf("invalid YAML: %v", err)
	}
	nodes, aliases := 0, 0
	if err := walkYAMLBounds(&root, 0, &nodes, &aliases); err != nil {
		return nil, err
	}
	return &root, nil
}

func walkYAMLBounds(n *yaml.Node, depth int, nodes, aliases *int) error {
	if n == nil {
		return nil
	}
	if depth > maxYAMLDepth {
		return fmt.Errorf("YAML nesting depth exceeds %d", maxYAMLDepth)
	}
	*nodes++
	if *nodes > maxYAMLNodes {
		return fmt.Errorf("YAML node count exceeds %d", maxYAMLNodes)
	}
	if n.Kind == yaml.AliasNode {
		*aliases++
		if *aliases > maxYAMLAliases {
			return fmt.Errorf("YAML alias count exceeds %d", maxYAMLAliases)
		}
		// Do not follow the alias — counting the reference is enough
		// and recursing could loop on cyclic anchors.
		return nil
	}
	for _, c := range n.Content {
		if err := walkYAMLBounds(c, depth+1, nodes, aliases); err != nil {
			return err
		}
	}
	return nil
}

// ParseError represents a failure to parse an artifact file.
type ParseError struct {
	Path    string
	Message string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse %s: %s", e.Path, e.Message)
}
