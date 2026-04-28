package domain

// ArtifactType represents the type of a governed artifact.
type ArtifactType string

const (
	ArtifactTypeInitiative   ArtifactType = "Initiative"
	ArtifactTypeEpic         ArtifactType = "Epic"
	ArtifactTypeTask         ArtifactType = "Task"
	ArtifactTypeADR          ArtifactType = "ADR"
	ArtifactTypeGovernance   ArtifactType = "Governance"
	ArtifactTypeArchitecture ArtifactType = "Architecture"
	ArtifactTypeProduct      ArtifactType = "Product"
	// ArtifactTypeWorkflow refers to a workflow definition (ADR-007). Workflow
	// definitions are YAML resources, not Markdown-with-frontmatter artifacts,
	// so they do not appear in the artifact parser — this constant exists so
	// workflows can declare applies_to: [Workflow] for the workflow-lifecycle
	// governance flow (ADR-008).
	ArtifactTypeWorkflow ArtifactType = "Workflow"
)

// ValidArtifactTypes returns all valid artifact types. Includes
// ArtifactTypeWorkflow so workflow binding can declare applies_to: [Workflow]
// (ADR-008). Callers that validate Markdown-with-frontmatter artifacts should
// use ValidMarkdownArtifactTypes instead — workflow definitions are YAML and
// must not be routed through the artifact parser.
func ValidArtifactTypes() []ArtifactType {
	return []ArtifactType{
		ArtifactTypeInitiative, ArtifactTypeEpic, ArtifactTypeTask,
		ArtifactTypeADR, ArtifactTypeGovernance, ArtifactTypeArchitecture,
		ArtifactTypeProduct, ArtifactTypeWorkflow,
	}
}

// ValidMarkdownArtifactTypes returns types that are stored as Markdown with
// YAML front matter. Excludes ArtifactTypeWorkflow (pure YAML, routed through
// workflow.Service). Use this when validating or parsing artifact files so a
// hand-crafted `.md` declaring `type: Workflow` is rejected.
func ValidMarkdownArtifactTypes() []ArtifactType {
	return []ArtifactType{
		ArtifactTypeInitiative, ArtifactTypeEpic, ArtifactTypeTask,
		ArtifactTypeADR, ArtifactTypeGovernance, ArtifactTypeArchitecture,
		ArtifactTypeProduct,
	}
}

// ArtifactStatus represents the lifecycle status of an artifact.
// Valid values depend on the artifact type (see artifact-schema.md §6).
type ArtifactStatus string

// Initiative and Epic statuses.
const (
	StatusDraft      ArtifactStatus = "Draft"
	StatusPending    ArtifactStatus = "Pending"
	StatusInProgress ArtifactStatus = "In Progress"
	StatusCompleted  ArtifactStatus = "Completed"
	StatusSuperseded ArtifactStatus = "Superseded"
)

// Task-specific terminal statuses.
const (
	StatusCancelled ArtifactStatus = "Cancelled"
	StatusRejected  ArtifactStatus = "Rejected"
	StatusAbandoned ArtifactStatus = "Abandoned"
)

// ADR statuses.
const (
	StatusProposed   ArtifactStatus = "Proposed"
	StatusAccepted   ArtifactStatus = "Accepted"
	StatusDeprecated ArtifactStatus = "Deprecated"
)

// Document statuses (Governance, Architecture, Product).
const (
	StatusLivingDocument ArtifactStatus = "Living Document"
	StatusFoundational   ArtifactStatus = "Foundational"
	StatusStable         ArtifactStatus = "Stable"
)

// ValidStatusesForType returns the allowed status values for a given artifact type.
func ValidStatusesForType(t ArtifactType) []ArtifactStatus {
	switch t {
	case ArtifactTypeInitiative, ArtifactTypeEpic:
		return []ArtifactStatus{StatusDraft, StatusPending, StatusInProgress, StatusCompleted, StatusSuperseded}
	case ArtifactTypeTask:
		return []ArtifactStatus{StatusDraft, StatusPending, StatusCompleted, StatusCancelled, StatusRejected, StatusSuperseded, StatusAbandoned}
	case ArtifactTypeADR:
		return []ArtifactStatus{StatusProposed, StatusAccepted, StatusDeprecated, StatusSuperseded}
	case ArtifactTypeGovernance:
		return []ArtifactStatus{StatusLivingDocument, StatusFoundational, StatusSuperseded}
	case ArtifactTypeArchitecture:
		return []ArtifactStatus{StatusLivingDocument, StatusStable, StatusSuperseded}
	case ArtifactTypeProduct:
		return []ArtifactStatus{StatusLivingDocument, StatusStable, StatusSuperseded}
	default:
		return nil
	}
}

// TaskAcceptance represents the acceptance outcome for a Task artifact.
type TaskAcceptance string

const (
	AcceptanceApproved             TaskAcceptance = "Approved"
	AcceptanceRejectedWithFollowup TaskAcceptance = "Rejected With Followup"
	AcceptanceRejectedClosed       TaskAcceptance = "Rejected Closed"
)

// LinkType represents the type of relationship between artifacts.
type LinkType string

const (
	LinkTypeParent    LinkType = "parent"
	LinkTypeBlockedBy LinkType = "blocked_by"
	LinkTypeRelatedTo LinkType = "related_to"
)

// Link represents a relationship between two artifacts.
type Link struct {
	Type   LinkType `json:"type" yaml:"type"`
	Target string   `json:"target" yaml:"target"` // canonical artifact path
}

// Artifact represents a governed artifact parsed from a Git-backed Markdown file.
type Artifact struct {
	Path                string            `json:"path" yaml:"path"`                                                     // repository-relative path
	ID                  string            `json:"id" yaml:"id"`                                                         // artifact ID from front matter
	Type                ArtifactType      `json:"type" yaml:"type"`                                                     // artifact type
	Title               string            `json:"title" yaml:"title"`                                                   // artifact title
	Status              ArtifactStatus    `json:"status" yaml:"status"`                                                 // lifecycle status
	Acceptance          TaskAcceptance    `json:"acceptance,omitempty" yaml:"acceptance,omitempty"`                     // task acceptance outcome
	AcceptanceRationale string            `json:"acceptance_rationale,omitempty" yaml:"acceptance_rationale,omitempty"` // rationale
	Repositories        []string          `json:"repositories,omitempty" yaml:"repositories,omitempty"`                 // task-only: code repository IDs (excluding the primary spine repo)
	Links               []Link            `json:"links" yaml:"links"`                                                   // relationships to other artifacts
	Metadata            map[string]string `json:"metadata" yaml:"metadata"`                                             // additional front matter fields
	Content             string            `json:"content" yaml:"content"`                                               // markdown body (after front matter)
}
