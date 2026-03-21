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
)

// ValidArtifactTypes returns all valid artifact types.
func ValidArtifactTypes() []ArtifactType {
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
	Type   LinkType `json:"type"`
	Target string   `json:"target"` // canonical artifact path
}

// Artifact represents a governed artifact parsed from a Git-backed Markdown file.
type Artifact struct {
	Path     string            `json:"path"`     // repository-relative path
	ID       string            `json:"id"`       // artifact ID from front matter
	Type     ArtifactType      `json:"type"`     // artifact type
	Title    string            `json:"title"`    // artifact title
	Status   ArtifactStatus    `json:"status"`   // lifecycle status
	Links    []Link            `json:"links"`    // relationships to other artifacts
	Metadata map[string]string `json:"metadata"` // additional front matter fields
	Content  string            `json:"content"`  // markdown body (after front matter)
}
