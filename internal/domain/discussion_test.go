package domain_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

func TestValidThreadStatuses(t *testing.T) {
	statuses := domain.ValidThreadStatuses()
	if len(statuses) != 3 {
		t.Errorf("expected 3 thread statuses, got %d", len(statuses))
	}

	expected := map[domain.ThreadStatus]bool{
		domain.ThreadStatusOpen:     true,
		domain.ThreadStatusResolved: true,
		domain.ThreadStatusArchived: true,
	}
	for _, s := range statuses {
		if !expected[s] {
			t.Errorf("unexpected thread status: %s", s)
		}
	}
}

func TestValidAnchorTypes(t *testing.T) {
	types := domain.ValidAnchorTypes()
	if len(types) != 4 {
		t.Errorf("expected 4 anchor types, got %d", len(types))
	}

	expected := map[domain.AnchorType]bool{
		domain.AnchorTypeArtifact:          true,
		domain.AnchorTypeRun:               true,
		domain.AnchorTypeStepExecution:     true,
		domain.AnchorTypeDivergenceContext: true,
	}
	for _, a := range types {
		if !expected[a] {
			t.Errorf("unexpected anchor type: %s", a)
		}
	}
}

func TestDiscussionThreadJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	resolved := now.Add(time.Hour)

	thread := domain.DiscussionThread{
		ThreadID:       "thread-001",
		AnchorType:     domain.AnchorTypeArtifact,
		AnchorID:       "initiatives/INIT-001/tasks/TASK-001.md",
		TopicKey:       "acceptance-criteria",
		Title:          "Clarify acceptance criteria",
		Status:         domain.ThreadStatusResolved,
		CreatedBy:      "actor-001",
		CreatedAt:      now,
		ResolvedAt:     &resolved,
		ResolutionType: domain.ResolutionArtifactUpdated,
		ResolutionRefs: json.RawMessage(`["commit-abc123"]`),
	}

	data, err := json.Marshal(thread)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got domain.DiscussionThread
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.ThreadID != thread.ThreadID {
		t.Errorf("ThreadID: got %s, want %s", got.ThreadID, thread.ThreadID)
	}
	if got.AnchorType != thread.AnchorType {
		t.Errorf("AnchorType: got %s, want %s", got.AnchorType, thread.AnchorType)
	}
	if got.Status != domain.ThreadStatusResolved {
		t.Errorf("Status: got %s, want resolved", got.Status)
	}
	if got.ResolutionType != domain.ResolutionArtifactUpdated {
		t.Errorf("ResolutionType: got %s, want artifact_updated", got.ResolutionType)
	}
	if got.ResolvedAt == nil {
		t.Fatal("ResolvedAt should not be nil")
	}
}

func TestCommentJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	comment := domain.Comment{
		CommentID:       "comment-001",
		ThreadID:        "thread-001",
		ParentCommentID: "comment-000",
		AuthorID:        "actor-001",
		AuthorType:      "human",
		Content:         "This looks good.",
		Metadata:        json.RawMessage(`{"source":"cli"}`),
		CreatedAt:       now,
		Deleted:         false,
	}

	data, err := json.Marshal(comment)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got domain.Comment
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.CommentID != comment.CommentID {
		t.Errorf("CommentID: got %s, want %s", got.CommentID, comment.CommentID)
	}
	if got.ParentCommentID != "comment-000" {
		t.Errorf("ParentCommentID: got %s, want comment-000", got.ParentCommentID)
	}
	if got.AuthorType != "human" {
		t.Errorf("AuthorType: got %s, want human", got.AuthorType)
	}
	if got.Deleted != false {
		t.Error("Deleted should be false")
	}
}

func TestCommentOmitsEmptyOptionalFields(t *testing.T) {
	comment := domain.Comment{
		CommentID:  "comment-002",
		ThreadID:   "thread-001",
		AuthorID:   "actor-001",
		AuthorType: "ai_agent",
		Content:    "Automated review.",
		CreatedAt:  time.Now().UTC(),
	}

	data, err := json.Marshal(comment)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}

	if _, ok := raw["parent_comment_id"]; ok {
		t.Error("parent_comment_id should be omitted when empty")
	}
	if _, ok := raw["edited_at"]; ok {
		t.Error("edited_at should be omitted when nil")
	}
	if _, ok := raw["metadata"]; ok {
		t.Error("metadata should be omitted when nil")
	}
}

func TestResolutionTypeConstants(t *testing.T) {
	types := []domain.ResolutionType{
		domain.ResolutionArtifactUpdated,
		domain.ResolutionArtifactCreated,
		domain.ResolutionADRCreated,
		domain.ResolutionDecisionRecorded,
		domain.ResolutionNoAction,
	}

	seen := make(map[domain.ResolutionType]bool)
	for _, rt := range types {
		if rt == "" {
			t.Error("resolution type should not be empty")
		}
		if seen[rt] {
			t.Errorf("duplicate resolution type: %s", rt)
		}
		seen[rt] = true
	}

	if len(types) != 5 {
		t.Errorf("expected 5 resolution types, got %d", len(types))
	}
}
