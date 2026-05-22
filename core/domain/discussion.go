package domain

import (
	"encoding/json"
	"time"
)

// ── Thread Status ──

type ThreadStatus string

const (
	ThreadStatusOpen     ThreadStatus = "open"
	ThreadStatusResolved ThreadStatus = "resolved"
	ThreadStatusArchived ThreadStatus = "archived"
)

func ValidThreadStatuses() []ThreadStatus {
	return []ThreadStatus{ThreadStatusOpen, ThreadStatusResolved, ThreadStatusArchived}
}

// ── Anchor Type ──

type AnchorType string

const (
	AnchorTypeArtifact          AnchorType = "artifact"
	AnchorTypeRun               AnchorType = "run"
	AnchorTypeStepExecution     AnchorType = "step_execution"
	AnchorTypeDivergenceContext AnchorType = "divergence_context"
)

func ValidAnchorTypes() []AnchorType {
	return []AnchorType{AnchorTypeArtifact, AnchorTypeRun, AnchorTypeStepExecution, AnchorTypeDivergenceContext}
}

// ── Resolution Type ──

type ResolutionType string

const (
	ResolutionArtifactUpdated  ResolutionType = "artifact_updated"
	ResolutionArtifactCreated  ResolutionType = "artifact_created"
	ResolutionADRCreated       ResolutionType = "adr_created"
	ResolutionDecisionRecorded ResolutionType = "decision_recorded"
	ResolutionNoAction         ResolutionType = "no_action"
)

// ── Discussion Thread ──

type DiscussionThread struct {
	ThreadID       string          `json:"thread_id" yaml:"thread_id"`
	AnchorType     AnchorType      `json:"anchor_type" yaml:"anchor_type"`
	AnchorID       string          `json:"anchor_id" yaml:"anchor_id"`
	TopicKey       string          `json:"topic_key,omitempty" yaml:"topic_key,omitempty"`
	Title          string          `json:"title,omitempty" yaml:"title,omitempty"`
	Status         ThreadStatus    `json:"status" yaml:"status"`
	CreatedBy      string          `json:"created_by" yaml:"created_by"`
	CreatedAt      time.Time       `json:"created_at" yaml:"created_at"`
	ResolvedAt     *time.Time      `json:"resolved_at,omitempty" yaml:"resolved_at,omitempty"`
	ResolutionType ResolutionType  `json:"resolution_type,omitempty" yaml:"resolution_type,omitempty"`
	ResolutionRefs json.RawMessage `json:"resolution_refs,omitempty" yaml:"resolution_refs,omitempty"`
}

// ── Comment ──

type Comment struct {
	CommentID       string          `json:"comment_id" yaml:"comment_id"`
	ThreadID        string          `json:"thread_id" yaml:"thread_id"`
	ParentCommentID string          `json:"parent_comment_id,omitempty" yaml:"parent_comment_id,omitempty"`
	AuthorID        string          `json:"author_id" yaml:"author_id"`
	AuthorType      string          `json:"author_type" yaml:"author_type"`
	Content         string          `json:"content" yaml:"content"`
	Metadata        json.RawMessage `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	CreatedAt       time.Time       `json:"created_at" yaml:"created_at"`
	EditedAt        *time.Time      `json:"edited_at,omitempty" yaml:"edited_at,omitempty"`
	Deleted         bool            `json:"deleted" yaml:"deleted"`
}
