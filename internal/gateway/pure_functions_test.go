package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workspace"
)

// ── appendThreadRefs ──

func TestAppendThreadRefs(t *testing.T) {
	got := appendThreadRefs("Because it failed.", []string{"https://example.com/1", "https://example.com/2"})
	if !strings.Contains(got, "Discussion threads:") {
		t.Error("expected 'Discussion threads:' in result")
	}
	if !strings.Contains(got, "https://example.com/1") {
		t.Error("expected first thread ref in result")
	}
}

// ── updateFrontMatterStatus ──

func TestUpdateFrontMatterStatus_Valid(t *testing.T) {
	content := "---\nid: TASK-001\nstatus: pending\ntitle: T\n---\n\nBody."
	got := updateFrontMatterStatus(content, "completed")
	if !strings.Contains(got, "status: completed") {
		t.Errorf("expected 'status: completed', got: %s", got)
	}
	if strings.Contains(got, "status: pending") {
		t.Error("expected old status 'pending' to be replaced")
	}
}

func TestUpdateFrontMatterStatus_NoFrontMatter(t *testing.T) {
	content := "No front matter here."
	got := updateFrontMatterStatus(content, "completed")
	if got != content {
		t.Errorf("expected unchanged content, got %q", got)
	}
}

func TestUpdateFrontMatterStatus_NoClosingDelimiter(t *testing.T) {
	content := "---\nstatus: pending\nno closing delimiter"
	got := updateFrontMatterStatus(content, "completed")
	if got != content {
		t.Errorf("expected unchanged content when no closing delimiter, got %q", got)
	}
}

// ── buildInitialContent ──

func TestBuildInitialContent_Task(t *testing.T) {
	parentMeta := map[string]string{"initiative": "/initiatives/INIT-001/initiative.md"}
	content := buildInitialContent(domain.ArtifactTypeTask, "TASK-001", "My Task", "/epics/EPIC-001/epic.md", parentMeta)
	if !strings.Contains(content, "id: TASK-001") {
		t.Error("expected id in content")
	}
	if !strings.Contains(content, "type: Task") {
		t.Error("expected type in content")
	}
	if !strings.Contains(content, "epic: /epics/EPIC-001/epic.md") {
		t.Error("expected epic field in task content")
	}
	if !strings.Contains(content, "initiative: /initiatives/INIT-001/initiative.md") {
		t.Error("expected initiative field inherited from parent epic")
	}
	if !strings.Contains(content, "type: parent") {
		t.Error("expected parent link in content")
	}
	today := time.Now().Format("2006-01-02")
	if !strings.Contains(content, "created: "+today) {
		t.Error("expected created date in content")
	}
}

func TestBuildInitialContent_Epic(t *testing.T) {
	content := buildInitialContent(domain.ArtifactTypeEpic, "EPIC-001", "My Epic", "/initiatives/INIT-001/initiative.md", nil)
	if !strings.Contains(content, "initiative: /initiatives/INIT-001/initiative.md") {
		t.Error("expected initiative field in epic content")
	}
	if !strings.Contains(content, "type: parent") {
		t.Error("expected parent link in content")
	}
}

func TestBuildInitialContent_Initiative(t *testing.T) {
	content := buildInitialContent(domain.ArtifactTypeInitiative, "INIT-001", "My Initiative", "", nil)
	if !strings.Contains(content, "id: INIT-001") {
		t.Error("expected id in content")
	}
	// No parent path → no links section.
	if strings.Contains(content, "type: parent") {
		t.Error("expected no parent link for initiative without parent")
	}
}

func TestBuildInitialContent_ParentWithoutLeadingSlash(t *testing.T) {
	// Parent path without leading slash should get one added.
	content := buildInitialContent(domain.ArtifactTypeTask, "TASK-001", "T", "epics/EPIC-001/epic.md", nil)
	if !strings.Contains(content, "epic: /epics/EPIC-001/epic.md") {
		t.Errorf("expected leading slash prepended, got: %s", content)
	}
}

// ── WorkspaceConfigFromContext ──

func TestWorkspaceConfigFromContext_Missing(t *testing.T) {
	cfg := WorkspaceConfigFromContext(context.Background())
	if cfg != nil {
		t.Errorf("expected nil for empty context, got %v", cfg)
	}
}

func TestWorkspaceConfigFromContext_Present(t *testing.T) {
	expected := &workspace.Config{ID: "ws-1", DisplayName: "WS One"}
	ctx := context.WithValue(context.Background(), workspaceKey{}, expected)
	got := WorkspaceConfigFromContext(ctx)
	if got == nil || got.ID != "ws-1" {
		t.Errorf("expected ws-1, got %v", got)
	}
}

// ── withDefault ──

func TestWithDefault_Positive(t *testing.T) {
	got := withDefault(5, 10)
	if got != 5 {
		t.Errorf("expected 5, got %v", got)
	}
}

func TestWithDefault_Zero(t *testing.T) {
	got := withDefault(0, 10)
	if got != 10 {
		t.Errorf("expected 10, got %v", got)
	}
}
