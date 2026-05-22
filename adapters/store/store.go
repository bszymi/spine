package store

import (
	"context"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// Store defines the full database-access interface for Spine — the
// union of every role-specific interface in interfaces.go. Per
// Implementation Guide §3.4. New consumers should depend on the
// narrowest role they actually use rather than the union; Store is
// kept for callers that legitimately need every method (the cmd/spine
// wiring and the per-workspace ServiceSet).
type Store interface {
	SystemStore
	RunStore
	BranchStore
	ArtifactStore
	WorkflowProjectionStore
	SyncStateStore
	AuthStore
	AssignmentStore
	SkillStore
	RepositoryStore
	BranchProtectionStore
	DiscussionStore
	DeliveryStore
	SubscriptionStore
}

// Tx represents a database transaction.
type Tx interface {
	CreateRun(ctx context.Context, run *domain.Run) error
	UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error
	TransitionRunStatus(ctx context.Context, runID string, fromStatus, toStatus domain.RunStatus) (bool, error)
	CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error
	UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error
}

// ArtifactProjection represents a projected artifact in the database.
type ArtifactProjection struct {
	ArtifactPath string   `json:"artifact_path"`
	ArtifactID   string   `json:"artifact_id"`
	ArtifactType string   `json:"artifact_type"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Metadata     []byte   `json:"metadata"` // JSONB
	Content      string   `json:"content"`
	Links        []byte   `json:"links"`        // JSONB
	Repositories []string `json:"repositories"` // task-only: code repository IDs declared in frontmatter
	SourceCommit string   `json:"source_commit"`
	ContentHash  string   `json:"content_hash"`
}

// ArtifactLink represents a denormalized link in the projection store.
type ArtifactLink struct {
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	LinkType   string `json:"link_type"`
}

// WorkflowProjection represents a projected workflow definition in the database.
type WorkflowProjection struct {
	WorkflowPath string `json:"workflow_path"`
	WorkflowID   string `json:"workflow_id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Status       string `json:"status"`
	AppliesTo    []byte `json:"applies_to"` // JSONB
	Definition   []byte `json:"definition"` // JSONB
	SourceCommit string `json:"source_commit"`
}

// BranchProtectionRuleProjection is one row of the projected
// /.spine/branch-protection.yaml ruleset (ADR-009 §1). Protections is a
// JSONB array of rule-kind strings (e.g. ["no-delete","no-direct-write"])
// mirroring the YAML value; the branchprotect package converts it to its
// typed RuleKind enum. SourceCommit is "bootstrap" for the seed rows
// installed by the 018 migration, otherwise the commit SHA that produced
// the projection.
type BranchProtectionRuleProjection struct {
	BranchPattern string `json:"branch_pattern"`
	RuleOrder     int    `json:"rule_order"`
	Protections   []byte `json:"protections"` // JSONB array of strings
	SourceCommit  string `json:"source_commit"`
}

// RepositoryBinding holds the operational connection details for a code
// repository registered in the workspace catalog (ADR-013). Identity
// fields (kind, name, default_branch, role, description) live in
// /.spine/repositories.yaml — only the runtime fields are stored here.
//
// The primary Spine repository (`repository_id = "spine"`) has no
// binding row; it is resolved virtually from the workspace's RepoPath
// and configured authoritative branch. The migration enforces this
// with a CHECK constraint, and the store's create path rejects the ID
// up front.
//
// DefaultBranch is an optional override — when empty, callers fall
// back to the catalog's `default_branch` for the same repository ID.
// It exists so a single workspace can pin a non-catalog branch (e.g.
// a long-lived release branch) without rewriting the governed
// catalog.
type RepositoryBinding struct {
	RepositoryID   string    `json:"repository_id" yaml:"repository_id"`
	WorkspaceID    string    `json:"workspace_id" yaml:"workspace_id"`
	CloneURL       string    `json:"clone_url" yaml:"clone_url"`
	CredentialsRef string    `json:"credentials_ref,omitempty" yaml:"credentials_ref,omitempty"`
	LocalPath      string    `json:"local_path" yaml:"local_path"`
	DefaultBranch  string    `json:"default_branch,omitempty" yaml:"default_branch,omitempty"`
	Status         string    `json:"status" yaml:"status"`
	CreatedAt      time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" yaml:"updated_at"`
}

// Repository binding statuses.
const (
	RepositoryBindingStatusActive   = "active"
	RepositoryBindingStatusInactive = "inactive"
)

// PrimaryRepositoryID is the reserved ID of the primary Spine
// repository within a workspace. No binding row may use it — it is
// resolved virtually from the workspace's RepoPath and the configured
// authoritative branch. The 019 migration enforces this with a CHECK
// constraint, and the store's create path rejects it explicitly so
// callers see a SpineError instead of a generic database constraint
// violation.
const PrimaryRepositoryID = "spine"

// SyncState tracks projection sync progress.
type SyncState struct {
	LastSyncedCommit string     `json:"last_synced_commit"`
	LastSyncedAt     *time.Time `json:"last_synced_at,omitempty"`
	Status           string     `json:"status"` // idle, syncing, rebuilding, error
	ErrorDetail      string     `json:"error_detail,omitempty"`
}

// Artifact-query pagination bounds shared between the HTTP handler
// and the store. The store enforces these defensively even when an
// internal caller bypasses the HTTP pagination helper, so a stray
// `Limit: 0` cannot fan out to an unbounded scan and a `Limit:
// 1_000_000` cannot push a multi-megabyte result set through the
// gateway.
const (
	ArtifactQueryDefaultLimit = 50
	ArtifactQueryMaxLimit     = 200
)

// ArtifactQuery defines parameters for querying projected artifacts.
type ArtifactQuery struct {
	Type       string
	Status     string
	ParentPath string
	Search     string
	Limit      int
	Cursor     string
}

// ClampedLimit returns the limit value the store will use for this
// query: non-positive limits resolve to ArtifactQueryDefaultLimit and
// oversize limits are capped at ArtifactQueryMaxLimit. Exposed so
// callers (and tests) can preview the effective limit before
// QueryArtifacts runs.
func (q ArtifactQuery) ClampedLimit() int {
	if q.Limit <= 0 {
		return ArtifactQueryDefaultLimit
	}
	if q.Limit > ArtifactQueryMaxLimit {
		return ArtifactQueryMaxLimit
	}
	return q.Limit
}

// ArtifactQueryResult contains the result of an artifact query.
type ArtifactQueryResult struct {
	Items      []ArtifactProjection
	NextCursor string
	HasMore    bool
}

// TokenRecord represents a token in the database (includes hash).
type TokenRecord struct {
	TokenID   string
	ActorID   string
	TokenHash string
	Name      string
	ExpiresAt *time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}
