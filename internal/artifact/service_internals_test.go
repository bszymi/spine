package artifact

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

func newBareService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	return &Service{repo: dir, artifactsDir: "/"}
}

func TestWithArtifactsDir(t *testing.T) {
	svc := newBareService(t)
	svc.WithArtifactsDir("spine")
	if svc.artifactsDir != "spine" {
		t.Errorf("expected 'spine', got %q", svc.artifactsDir)
	}
}

func TestStripArtifactsDir_WithDir(t *testing.T) {
	svc := &Service{artifactsDir: "spine"}
	got := svc.stripArtifactsDir("spine/governance/charter.md")
	if got != "governance/charter.md" {
		t.Errorf("expected 'governance/charter.md', got %q", got)
	}
}

func TestStripArtifactsDir_Root(t *testing.T) {
	svc := &Service{artifactsDir: "/"}
	got := svc.stripArtifactsDir("governance/charter.md")
	if got != "governance/charter.md" {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestStripArtifactsDir_Empty(t *testing.T) {
	svc := &Service{artifactsDir: ""}
	got := svc.stripArtifactsDir("governance/charter.md")
	if got != "governance/charter.md" {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestRepoRelativePath_Root(t *testing.T) {
	svc := &Service{artifactsDir: "/"}
	got := svc.repoRelativePath("governance/charter.md")
	if got != "governance/charter.md" {
		t.Errorf("expected 'governance/charter.md', got %q", got)
	}
}

func TestRepoRelativePath_Empty(t *testing.T) {
	svc := &Service{artifactsDir: ""}
	got := svc.repoRelativePath("governance/charter.md")
	if got != "governance/charter.md" {
		t.Errorf("expected 'governance/charter.md', got %q", got)
	}
}

func TestRepoRelativePath_WithDir(t *testing.T) {
	svc := &Service{artifactsDir: "spine"}
	got := svc.repoRelativePath("governance/charter.md")
	if got != "spine/governance/charter.md" {
		t.Errorf("expected 'spine/governance/charter.md', got %q", got)
	}
}

func TestRepoRelativePath_TrimsLeadingSlash(t *testing.T) {
	svc := &Service{artifactsDir: "spine"}
	got := svc.repoRelativePath("/governance/charter.md")
	if got != "spine/governance/charter.md" {
		t.Errorf("expected 'spine/governance/charter.md', got %q", got)
	}
}

func TestSafePath_Valid(t *testing.T) {
	dir := t.TempDir()
	svc := &Service{repo: dir, artifactsDir: "/"}
	// Create a file in the dir.
	f := filepath.Join(dir, "test.md")
	if err := os.WriteFile(f, []byte("content"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := svc.safePath("test.md")
	if err != nil {
		t.Fatalf("safePath: %v", err)
	}
	if !strings.HasSuffix(got, "test.md") {
		t.Errorf("expected path ending with test.md, got %q", got)
	}
}

func TestSafePath_AbsolutePathRejected(t *testing.T) {
	svc := newBareService(t)
	_, err := svc.safePath("/absolute/path.md")
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestSafePath_TraversalRejected(t *testing.T) {
	svc := newBareService(t)
	_, err := svc.safePath("../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

// The error returned from safePath must be generic; the server-side
// log is the only place filesystem detail should appear. See
// TASK-023 item 1.
func TestSafePath_ErrorDoesNotLeakFilesystemPaths(t *testing.T) {
	svc := newBareService(t)
	_, err := svc.safePath("../../etc/passwd")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, leak := range []string{"/etc/passwd", "/tmp/", "/var/", "/home/", "/Users/"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("safePath error leaked filesystem detail %q: %q", leak, msg)
		}
	}
}

func TestNextFollowupID_Regular(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"TASK-001", "TASK-901"},
		{"TASK-050", "TASK-950"},
		{"TASK-099", "TASK-999"},
		{"EPIC-003", "EPIC-903"},
	}
	for _, tt := range tests {
		got := nextFollowupID(tt.input)
		if got != tt.expected {
			t.Errorf("nextFollowupID(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestNextFollowupID_AlreadyFollowup(t *testing.T) {
	// 900-series should increment.
	got := nextFollowupID("TASK-901")
	if got != "TASK-902" {
		t.Errorf("nextFollowupID('TASK-901') = %q, want 'TASK-902'", got)
	}
}

func TestNextFollowupID_InvalidFormat(t *testing.T) {
	got := nextFollowupID("NOHYPHEN")
	if got != "NOHYPHEN-F01" {
		t.Errorf("nextFollowupID('NOHYPHEN') = %q, want 'NOHYPHEN-F01'", got)
	}
}

func TestBuildSuccessorContent(t *testing.T) {
	original := &domain.Artifact{
		ID:    "TASK-001",
		Title: "Original Task",
		Type:  domain.ArtifactTypeTask,
		Metadata: map[string]string{
			"epic":       "EPIC-001",
			"initiative": "INIT-001",
		},
	}

	content := buildSuccessorContent(original, "TASK-901", "tasks/TASK-001.md", "Not acceptable")

	if !strings.Contains(content, "id: TASK-901") {
		t.Error("expected successor ID in content")
	}
	if !strings.Contains(content, "Follow-up: Original Task") {
		t.Error("expected follow-up title")
	}
	if !strings.Contains(content, "type: follow_up_to") {
		t.Error("expected follow_up_to link")
	}
	if !strings.Contains(content, "/tasks/TASK-001.md") {
		t.Error("expected link target with leading slash")
	}
	if !strings.Contains(content, "Not acceptable") {
		t.Error("expected rationale in content")
	}
	if !strings.Contains(content, "epic: EPIC-001") {
		t.Error("expected epic metadata")
	}
}

func TestBuildSuccessorContent_NoRationale(t *testing.T) {
	original := &domain.Artifact{
		ID:       "TASK-001",
		Title:    "Original",
		Type:     domain.ArtifactTypeTask,
		Metadata: map[string]string{},
	}
	content := buildSuccessorContent(original, "TASK-901", "tasks/TASK-001.md", "")

	if strings.Contains(content, "Rejection Rationale") {
		t.Error("expected no Rejection Rationale section for empty rationale")
	}
}

func TestInsertLink_NewLinksSection(t *testing.T) {
	content := `---
type: Task
title: My Task
status: pending
---

Content.
`
	result := insertLink(content, "follow_up_to", "tasks/TASK-001.md")

	if !strings.Contains(result, "links:") {
		t.Error("expected links: section to be added")
	}
	if !strings.Contains(result, "type: follow_up_to") {
		t.Error("expected link type in result")
	}
	if !strings.Contains(result, "/tasks/TASK-001.md") {
		t.Error("expected target with leading slash")
	}
}

func TestInsertLink_ExistingLinksSection(t *testing.T) {
	content := `---
type: Task
title: My Task
status: pending
links:
  - type: parent
    target: /epics/EPIC-001.md
---

Content.
`
	result := insertLink(content, "follow_up_to", "tasks/TASK-001.md")

	// Should add to existing links section.
	if !strings.Contains(result, "type: follow_up_to") {
		t.Error("expected new link to be added")
	}
	// Original link should still be there.
	if !strings.Contains(result, "type: parent") {
		t.Error("expected original link to be preserved")
	}
}

// ── splitFrontMatter tests ──

func TestSplitFrontMatter_CRLFOpening(t *testing.T) {
	// Covers the ---\r\n opening branch.
	content := "---\r\ntype: Task\ntitle: T\n---\n\nBody."
	fm, body, err := splitFrontMatter([]byte(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(fm), "type: Task") {
		t.Errorf("expected front matter to contain type, got %q", fm)
	}
	if body != "Body." {
		t.Errorf("expected body 'Body.', got %q", body)
	}
}

func TestSplitFrontMatter_NoClosingDelimiter(t *testing.T) {
	content := "---\ntype: Task\ntitle: T\n"
	_, _, err := splitFrontMatter([]byte(content))
	if err == nil {
		t.Fatal("expected error for missing closing delimiter")
	}
}

func TestSplitFrontMatter_EmbeddedDashesInValue(t *testing.T) {
	// "---" embedded inside a value line (not at start of line) is not treated
	// as a closing delimiter — parser finds it but rejects it, then returns error
	// because no valid closing delimiter follows.
	content := "---\ntype: Task\nsome---thing\n---\n"
	_, _, err := splitFrontMatter([]byte(content))
	// The first "---" found is inside "some---thing" (not on its own line),
	// so it is rejected and the parse fails.
	if err == nil {
		t.Fatal("expected error for embedded dashes not on own line")
	}
}

func TestSplitFrontMatter_Valid(t *testing.T) {
	content := "---\ntype: Task\ntitle: T\n---\n\nBody text."
	fm, body, err := splitFrontMatter([]byte(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(fm), "type: Task") {
		t.Errorf("expected front matter to contain type, got %q", fm)
	}
	if body != "Body text." {
		t.Errorf("expected body 'Body text.', got %q", body)
	}
}

func TestSplitFrontMatter_NoOpening(t *testing.T) {
	content := "no front matter here"
	_, _, err := splitFrontMatter([]byte(content))
	if err == nil {
		t.Fatal("expected error for missing opening delimiter")
	}
}

// ── safePathIn tests ──

func TestSafePathIn_Traversal(t *testing.T) {
	svc := newBareService(t)
	_, err := svc.safePathIn(svc.repo, "../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestSafePathIn_Absolute(t *testing.T) {
	svc := newBareService(t)
	_, err := svc.safePathIn(svc.repo, "/etc/passwd")
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestSafePathIn_Valid(t *testing.T) {
	dir := t.TempDir()
	svc := &Service{repo: dir, artifactsDir: "/"}
	f := filepath.Join(dir, "valid.md")
	if err := os.WriteFile(f, []byte("content"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := svc.safePathIn(dir, "valid.md")
	if err != nil {
		t.Fatalf("safePathIn: %v", err)
	}
	if !strings.HasSuffix(got, "valid.md") {
		t.Errorf("expected path ending with valid.md, got %q", got)
	}
}

// ── result helper tests ──

func TestResult_Passed(t *testing.T) {
	r := result(nil, nil)
	if r.Status != "passed" {
		t.Errorf("expected 'passed', got %q", r.Status)
	}
}

func TestResult_Failed(t *testing.T) {
	errs := []domain.ValidationError{{Field: "type", Message: "missing"}}
	r := result(errs, nil)
	if r.Status != "failed" {
		t.Errorf("expected 'failed', got %q", r.Status)
	}
}

func TestResult_Warnings(t *testing.T) {
	warns := []domain.ValidationError{{Field: "title", Message: "suggestion"}}
	r := result(nil, warns)
	if r.Status != "warnings" {
		t.Errorf("expected 'warnings', got %q", r.Status)
	}
}

// ── ValidateField edge cases (white-box) ──

func TestValidateField_UnknownType(t *testing.T) {
	a := &domain.Artifact{Type: domain.ArtifactType("UnknownXYZ")}
	err := ValidateField(a, "type")
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestValidateField_IDMissingForTask(t *testing.T) {
	a := &domain.Artifact{Type: domain.ArtifactTypeTask, ID: ""}
	err := ValidateField(a, "id")
	if err == nil {
		t.Fatal("expected error for missing task ID")
	}
}

// ── Dummy usage to avoid unused import warning ──

var _ = fmt.Sprintf
