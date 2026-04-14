package artifact_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
)

func setupIDAllocatorRepo(t *testing.T) (*git.CLIClient, string) {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)

	// Create an epic with 3 tasks.
	testutil.WriteFile(t, repo, "initiatives/INIT-001-test/epics/EPIC-001-test/tasks/TASK-001-first.md",
		"---\nid: TASK-001\ntype: Task\ntitle: First\nstatus: Pending\n---\n# TASK-001\n")
	testutil.WriteFile(t, repo, "initiatives/INIT-001-test/epics/EPIC-001-test/tasks/TASK-002-second.md",
		"---\nid: TASK-002\ntype: Task\ntitle: Second\nstatus: Pending\n---\n# TASK-002\n")
	testutil.WriteFile(t, repo, "initiatives/INIT-001-test/epics/EPIC-001-test/tasks/TASK-005-fifth.md",
		"---\nid: TASK-005\ntype: Task\ntitle: Fifth\nstatus: Pending\n---\n# TASK-005\n")

	// A follow-up task in the 900-series.
	testutil.WriteFile(t, repo, "initiatives/INIT-001-test/epics/EPIC-001-test/tasks/TASK-901-followup.md",
		"---\nid: TASK-901\ntype: Task\ntitle: Followup\nstatus: Draft\n---\n# TASK-901\n")

	// Create some epics under an initiative.
	testutil.WriteFile(t, repo, "initiatives/INIT-001-test/epics/EPIC-001-test/epic.md",
		"---\nid: EPIC-001\ntype: Epic\ntitle: Test\nstatus: Pending\n---\n# EPIC-001\n")
	testutil.WriteFile(t, repo, "initiatives/INIT-001-test/epics/EPIC-003-other/epic.md",
		"---\nid: EPIC-003\ntype: Epic\ntitle: Other\nstatus: Pending\n---\n# EPIC-003\n")

	// Create ADRs.
	testutil.WriteFile(t, repo, "architecture/adr/ADR-0001-first.md",
		"---\nid: ADR-0001\ntype: ADR\ntitle: First\nstatus: Accepted\n---\n# ADR-0001\n")
	testutil.WriteFile(t, repo, "architecture/adr/ADR-0003-third.md",
		"---\nid: ADR-0003\ntype: ADR\ntitle: Third\nstatus: Accepted\n---\n# ADR-0003\n")

	testutil.GitAdd(t, repo, ".", "add test artifacts")
	return client, repo
}

// ── NextID tests ──

func TestNextID_Sequential(t *testing.T) {
	client, _ := setupIDAllocatorRepo(t)
	ctx := context.Background()

	id, err := artifact.NextID(ctx, client, "initiatives/INIT-001-test/epics/EPIC-001-test/tasks", domain.ArtifactTypeTask, "HEAD")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	// TASK-001, TASK-002, TASK-005 exist (TASK-901 excluded). Max is 5, next is 6.
	if id != "TASK-006" {
		t.Errorf("expected TASK-006, got %s", id)
	}
}

func TestNextID_EmptyDirectory(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)

	testutil.WriteFile(t, repo, "initiatives/INIT-001-test/epics/EPIC-002-empty/epic.md",
		"---\nid: EPIC-002\ntype: Epic\ntitle: Empty\nstatus: Pending\n---\n# EPIC-002\n")
	testutil.GitAdd(t, repo, ".", "add empty epic")

	ctx := context.Background()
	id, err := artifact.NextID(ctx, client, "initiatives/INIT-001-test/epics/EPIC-002-empty/tasks", domain.ArtifactTypeTask, "HEAD")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if id != "TASK-001" {
		t.Errorf("expected TASK-001, got %s", id)
	}
}

func TestNextID_GapsPreserved(t *testing.T) {
	client, _ := setupIDAllocatorRepo(t)
	ctx := context.Background()

	// EPIC-001 and EPIC-003 exist (gap at 002). Max is 3, next is 4.
	id, err := artifact.NextID(ctx, client, "initiatives/INIT-001-test/epics", domain.ArtifactTypeEpic, "HEAD")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if id != "EPIC-004" {
		t.Errorf("expected EPIC-004, got %s", id)
	}
}

func TestNextID_ExcludesFollowUps(t *testing.T) {
	client, _ := setupIDAllocatorRepo(t)
	ctx := context.Background()

	// TASK-901 exists but should be excluded. Max regular is 5.
	id, err := artifact.NextID(ctx, client, "initiatives/INIT-001-test/epics/EPIC-001-test/tasks", domain.ArtifactTypeTask, "HEAD")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if id != "TASK-006" {
		t.Errorf("expected TASK-006, got %s", id)
	}
}

func TestNextID_ADR_FourDigitPadding(t *testing.T) {
	client, _ := setupIDAllocatorRepo(t)
	ctx := context.Background()

	// ADR-0001 and ADR-0003 exist. Max is 3, next is 4.
	id, err := artifact.NextID(ctx, client, "architecture/adr", domain.ArtifactTypeADR, "HEAD")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if id != "ADR-0004" {
		t.Errorf("expected ADR-0004, got %s", id)
	}
}

func TestNextID_UnsupportedType(t *testing.T) {
	client, _ := setupIDAllocatorRepo(t)
	ctx := context.Background()

	_, err := artifact.NextID(ctx, client, "governance", domain.ArtifactTypeGovernance, "HEAD")
	if err == nil {
		t.Error("expected error for unsupported type, got nil")
	}
}

func TestNextID_Initiative(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)

	testutil.WriteFile(t, repo, "initiatives/INIT-001-first/initiative.md",
		"---\nid: INIT-001\ntype: Initiative\ntitle: First\nstatus: Completed\n---\n# INIT-001\n")
	testutil.WriteFile(t, repo, "initiatives/INIT-003-third/initiative.md",
		"---\nid: INIT-003\ntype: Initiative\ntitle: Third\nstatus: Pending\n---\n# INIT-003\n")
	testutil.GitAdd(t, repo, ".", "add initiatives")

	ctx := context.Background()
	id, err := artifact.NextID(ctx, client, "initiatives", domain.ArtifactTypeInitiative, "HEAD")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if id != "INIT-004" {
		t.Errorf("expected INIT-004, got %s", id)
	}
}

func TestNextID_IgnoresNonMatchingFiles(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)

	// Add a task, a non-matching file, and a README in the same directory.
	testutil.WriteFile(t, repo, "tasks/TASK-002-real.md",
		"---\nid: TASK-002\ntype: Task\ntitle: Real\nstatus: Pending\n---\n# TASK-002\n")
	testutil.WriteFile(t, repo, "tasks/README.md", "# Tasks\n")
	testutil.WriteFile(t, repo, "tasks/notes.txt", "some notes\n")
	testutil.GitAdd(t, repo, ".", "add mixed files")

	ctx := context.Background()
	id, err := artifact.NextID(ctx, client, "tasks", domain.ArtifactTypeTask, "HEAD")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	// Only TASK-002 should be counted.
	if id != "TASK-003" {
		t.Errorf("expected TASK-003, got %s", id)
	}
}

func TestNextID_DefaultRef(t *testing.T) {
	client, _ := setupIDAllocatorRepo(t)
	ctx := context.Background()

	// Empty ref should default to HEAD.
	id, err := artifact.NextID(ctx, client, "initiatives/INIT-001-test/epics/EPIC-001-test/tasks", domain.ArtifactTypeTask, "")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if id != "TASK-006" {
		t.Errorf("expected TASK-006 with empty ref, got %s", id)
	}
}

// ── Slugify tests ──

func TestSlugify_Basic(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Implement validation", "implement-validation"},
		{"Add API (v2)", "add-api-v2"},
		{"foo--bar", "foo-bar"},
		{"--leading-trailing--", "leading-trailing"},
		{"some_thing", "some-thing"},
		{"Hello World!", "hello-world"},
		{"  spaces  everywhere  ", "spaces-everywhere"},
		{"UPPER CASE", "upper-case"},
		{"already-slugified", "already-slugified"},
		{"123 numbers", "123-numbers"},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := artifact.Slugify(tc.input)
			if got != tc.expected {
				t.Errorf("Slugify(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

// ── BuildArtifactPath tests ──

func TestBuildArtifactPath_Task(t *testing.T) {
	got := artifact.BuildArtifactPath(domain.ArtifactTypeTask, "TASK-006", "implement-validation",
		"initiatives/INIT-003-test/epics/EPIC-003-test/tasks")
	expected := "initiatives/INIT-003-test/epics/EPIC-003-test/tasks/task-006-implement-validation.md"
	if got != expected {
		t.Errorf("got %s, want %s", got, expected)
	}
}

func TestBuildArtifactPath_Epic(t *testing.T) {
	got := artifact.BuildArtifactPath(domain.ArtifactTypeEpic, "EPIC-004", "new-feature",
		"initiatives/INIT-003-test/epics")
	expected := "initiatives/INIT-003-test/epics/epic-004-new-feature/epic.md"
	if got != expected {
		t.Errorf("got %s, want %s", got, expected)
	}
}

func TestBuildArtifactPath_Initiative(t *testing.T) {
	got := artifact.BuildArtifactPath(domain.ArtifactTypeInitiative, "INIT-011", "artifact-creation",
		"initiatives")
	expected := "initiatives/init-011-artifact-creation/initiative.md"
	if got != expected {
		t.Errorf("got %s, want %s", got, expected)
	}
}

func TestBuildArtifactPath_ADR(t *testing.T) {
	got := artifact.BuildArtifactPath(domain.ArtifactTypeADR, "ADR-0007", "event-sourcing",
		"architecture/adr")
	expected := "architecture/adr/adr-0007-event-sourcing.md"
	if got != expected {
		t.Errorf("got %s, want %s", got, expected)
	}
}

func TestBuildArtifactPath_Document(t *testing.T) {
	got := artifact.BuildArtifactPath(domain.ArtifactTypeGovernance, "", "api-standards", "governance")
	expected := "governance/api-standards.md"
	if got != expected {
		t.Errorf("got %s, want %s", got, expected)
	}
}

// ── BuildDocumentPath tests ──

func TestBuildDocumentPath_Governance(t *testing.T) {
	got := artifact.BuildDocumentPath(domain.ArtifactTypeGovernance, "api-standards")
	if got != "governance/api-standards.md" {
		t.Errorf("got %s, want governance/api-standards.md", got)
	}
}

func TestBuildDocumentPath_Architecture(t *testing.T) {
	got := artifact.BuildDocumentPath(domain.ArtifactTypeArchitecture, "caching-strategy")
	if got != "architecture/caching-strategy.md" {
		t.Errorf("got %s, want architecture/caching-strategy.md", got)
	}
}

func TestBuildDocumentPath_Product(t *testing.T) {
	got := artifact.BuildDocumentPath(domain.ArtifactTypeProduct, "pricing-model")
	if got != "product/pricing-model.md" {
		t.Errorf("got %s, want product/pricing-model.md", got)
	}
}

func TestBuildDocumentPath_UnknownType(t *testing.T) {
	// Unknown type falls back to lowercase type name as directory.
	got := artifact.BuildDocumentPath(domain.ArtifactTypeTask, "some-doc")
	if got != "task/some-doc.md" {
		t.Errorf("got %s, want task/some-doc.md", got)
	}
}
