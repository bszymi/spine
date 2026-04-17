package engine

import (
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

func TestGenerateBranchName_StandardRun(t *testing.T) {
	name := generateBranchName(domain.RunModeStandard, "TASK-003", "initiatives/init-001/epics/epic-001/tasks/task-003-git-push.md", "run-0a5d0f6d")
	want := "spine/run/task-003-epic-001-git-push"
	if name != want {
		t.Errorf("got %q, want %q", name, want)
	}
}

func TestGenerateBranchName_PlanningRun(t *testing.T) {
	name := generateBranchName(domain.RunModePlanning, "INIT-001", "initiatives/init-001/initiative.md", "run-abcd1234")
	want := "spine/plan/init-001-initiative"
	if name != want {
		t.Errorf("got %q, want %q", name, want)
	}
}

func TestGenerateBranchName_TaskWithEpicContext(t *testing.T) {
	name := generateBranchName(domain.RunModeStandard, "TASK-001",
		"initiatives/init-001/epics/EPIC-009/tasks/TASK-001-credential-schema-and-storage.md",
		"run-4390cc54")
	want := "spine/run/task-001-epic-009-credential-schema-and-storage"
	if name != want {
		t.Errorf("got %q, want %q", name, want)
	}
}

func TestGenerateBranchName_EpicUnchanged(t *testing.T) {
	name := generateBranchName(domain.RunModeStandard, "EPIC-001",
		"initiatives/init-001/epics/epic-001/epic.md", "run-abcd1234")
	want := "spine/run/epic-001-epic"
	if name != want {
		t.Errorf("got %q, want %q", name, want)
	}
}

func TestGenerateBranchName_LongSlugTruncated(t *testing.T) {
	name := generateBranchName(domain.RunModeStandard, "TASK-099",
		"initiatives/init-001/epics/epic-001/tasks/task-099-this-is-a-very-long-task-name-that-exceeds-the-maximum-allowed-length.md",
		"run-abcd1234")
	// Should be truncated to maxSlugLength
	slug := name[len("spine/run/"):]
	if len(slug) > maxSlugLength {
		t.Errorf("slug too long: %d chars, max %d: %q", len(slug), maxSlugLength, slug)
	}
	// Should not end with a hyphen
	if slug[len(slug)-1] == '-' {
		t.Errorf("slug should not end with hyphen: %q", slug)
	}
}

func TestGenerateBranchNameWithSuffix(t *testing.T) {
	name := generateBranchNameWithSuffix(domain.RunModePlanning, "INIT-001", "initiatives/init-001/initiative.md", "run-0a5d0f6d")
	want := "spine/plan/init-001-initiative-0a5d0f6d"
	if name != want {
		t.Errorf("got %q, want %q", name, want)
	}
}

// TestGenerateBranchNameWithSuffix_WorkflowPlanningNamespace asserts that
// workflow-lifecycle planning runs produce branch names under a distinct
// namespace that cannot collide with artifact-creation planning or task
// runs: `spine/plan/` + workflow-id-derived slug. Paired with the fact
// that workflow paths live under `workflows/` which is disjoint from
// `initiatives/` + `epics/` + `tasks/`.
func TestGenerateBranchNameWithSuffix_WorkflowPlanningNamespace(t *testing.T) {
	// Workflow planning run.
	wfName := generateBranchNameWithSuffix(
		domain.RunModePlanning,
		"workflow-lifecycle",
		"workflows/workflow-lifecycle.yaml",
		"run-0a5d0f6d",
	)

	// Artifact planning run with the same run ID — must NOT equal the above.
	artName := generateBranchNameWithSuffix(
		domain.RunModePlanning,
		"INIT-001",
		"initiatives/init-001/initiative.md",
		"run-0a5d0f6d",
	)

	if wfName == artName {
		t.Fatalf("workflow and artifact planning runs must produce distinct branch names: %q vs %q",
			wfName, artName)
	}
	if !strings.HasPrefix(wfName, "spine/plan/") {
		t.Errorf("workflow planning branch must use spine/plan/ prefix, got %q", wfName)
	}
	if !strings.Contains(wfName, "workflow-lifecycle") {
		t.Errorf("workflow planning branch should embed workflow id, got %q", wfName)
	}
}

func TestGenerateBranchNameWithSuffix_TaskWithEpic(t *testing.T) {
	name := generateBranchNameWithSuffix(domain.RunModeStandard, "TASK-001",
		"initiatives/init-001/epics/EPIC-009/tasks/TASK-001-credential-schema.md",
		"run-4390cc54")
	want := "spine/run/task-001-epic-009-credential-schema-4390cc54"
	if name != want {
		t.Errorf("got %q, want %q", name, want)
	}
}

func TestSlugFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"initiatives/init-001/initiative.md", "initiative"},
		{"initiatives/init-001/epics/epic-001/tasks/task-003-git-push.md", "epic-001-task-003-git-push"},
		{"governance/charter.md", "charter"},
		{"workflows/task-default.yaml", "task-default"},
		{"initiatives/init-001/epics/epic-001/epic.md", "epic"},
		{"initiatives/init-001/epics/EPIC-009/tasks/TASK-001-credential-schema.md", "epic-009-task-001-credential-schema"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := slugFromPath(tt.path)
			if got != tt.want {
				t.Errorf("slugFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World", "hello-world"},
		{"INIT-001", "init-001"},
		{"task_003_git-push", "task-003-git-push"},
		{"--leading-trailing--", "leading-trailing"},
		{"Special!@#$%Chars", "special-chars"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitize(tt.input)
			if got != tt.want {
				t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRunIDSuffix(t *testing.T) {
	if got := runIDSuffix("run-0a5d0f6d"); got != "0a5d0f6d" {
		t.Errorf("got %q, want 0a5d0f6d", got)
	}
	if got := runIDSuffix("custom-id"); got != "custom-id" {
		t.Errorf("got %q, want custom-id", got)
	}
}

func TestGenerateBranchName_NoArtifactID(t *testing.T) {
	// When artifact has no ID (e.g., Governance), just use the slug
	name := generateBranchName(domain.RunModeStandard, "", "governance/charter.md", "run-abcd1234")
	want := "spine/run/charter"
	if name != want {
		t.Errorf("got %q, want %q", name, want)
	}
}
