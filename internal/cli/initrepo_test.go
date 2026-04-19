package cli_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/branchprotect/config"
	"github.com/bszymi/spine/internal/cli"
	"github.com/bszymi/spine/internal/workflow"
)

// rootOpts creates InitOpts for root-level artifacts (backward compat).
func rootOpts() cli.InitOpts {
	return cli.InitOpts{ArtifactsDir: "/", NoBranch: true}
}

func TestInitRepo_CreatesDirectories(t *testing.T) {
	dir := t.TempDir()

	if err := cli.InitRepo(dir, rootOpts()); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	expectedDirs := []string{"governance", "initiatives", "architecture", "product", "workflows", "templates", "tmp"}
	for _, d := range expectedDirs {
		p := filepath.Join(dir, d)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", d)
		}
	}
}

func TestInitRepo_CreatesSeedFiles(t *testing.T) {
	dir := t.TempDir()

	if err := cli.InitRepo(dir, rootOpts()); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	expectedFiles := []string{
		"governance/charter.md",
		"governance/constitution.md",
		"governance/guidelines.md",
		"governance/repository-structure.md",
		"governance/naming-conventions.md",
		"templates/task-template.md",
		"templates/epic-template.md",
		"templates/initiative-template.md",
		"templates/adr-template.md",
	}
	for _, f := range expectedFiles {
		p := filepath.Join(dir, f)
		content, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("expected file %s: %v", f, err)
			continue
		}
		// Verify it has YAML front matter.
		if !strings.HasPrefix(string(content), "---\n") {
			t.Errorf("expected %s to have YAML front matter", f)
		}
	}
}

func TestInitRepo_SeedsWorkflowLifecycle(t *testing.T) {
	dir := t.TempDir()

	if err := cli.InitRepo(dir, rootOpts()); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	p := filepath.Join(dir, "workflows/workflow-lifecycle.yaml")
	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("expected workflow-lifecycle.yaml: %v", err)
	}

	wf, err := workflow.Parse("workflows/workflow-lifecycle.yaml", content)
	if err != nil {
		t.Fatalf("parse seeded workflow: %v", err)
	}

	if wf.ID != "workflow-lifecycle" {
		t.Errorf("expected id workflow-lifecycle, got %s", wf.ID)
	}
	if wf.Mode != "creation" {
		t.Errorf("expected mode creation, got %s", wf.Mode)
	}

	result := workflow.Validate(wf)
	if result.Status != "passed" {
		t.Errorf("seeded workflow failed validation: %+v", result.Errors)
	}
}

func TestInitRepo_Idempotent(t *testing.T) {
	dir := t.TempDir()

	// First run.
	if err := cli.InitRepo(dir, rootOpts()); err != nil {
		t.Fatalf("first InitRepo: %v", err)
	}

	// Write custom content to an existing file.
	charterPath := filepath.Join(dir, "governance/charter.md")
	custom := []byte("custom content")
	if err := os.WriteFile(charterPath, custom, 0o644); err != nil {
		t.Fatalf("write custom: %v", err)
	}

	// Second run — should not overwrite.
	if err := cli.InitRepo(dir, rootOpts()); err != nil {
		t.Fatalf("second InitRepo: %v", err)
	}

	content, _ := os.ReadFile(charterPath)
	if string(content) != "custom content" {
		t.Error("expected existing file to not be overwritten")
	}
}

func TestInitRepo_InitializesGit(t *testing.T) {
	dir := t.TempDir()

	if err := cli.InitRepo(dir, rootOpts()); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		t.Error("expected .git directory to exist")
	}
}

func TestInitRepo_SkipsGitIfAlreadyInitialized(t *testing.T) {
	dir := t.TempDir()

	// Pre-init Git properly (not just mkdir .git).
	cmd := exec.Command("git", "init", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// Should not fail or re-initialize.
	if err := cli.InitRepo(dir, rootOpts()); err != nil {
		t.Fatalf("InitRepo with existing .git: %v", err)
	}
}

func TestInitRepo_CreatesSpineYAML(t *testing.T) {
	dir := t.TempDir()

	if err := cli.InitRepo(dir, rootOpts()); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, ".spine.yaml"))
	if err != nil {
		t.Fatalf("read .spine.yaml: %v", err)
	}
	if !strings.Contains(string(content), "artifacts_dir: /") {
		t.Errorf("expected artifacts_dir: / in .spine.yaml, got: %s", content)
	}
}

func TestInitRepo_SubdirectoryArtifacts(t *testing.T) {
	dir := t.TempDir()

	if err := cli.InitRepo(dir, cli.InitOpts{ArtifactsDir: "spine", NoBranch: true}); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	// .spine.yaml at root
	content, err := os.ReadFile(filepath.Join(dir, ".spine.yaml"))
	if err != nil {
		t.Fatalf("read .spine.yaml: %v", err)
	}
	if !strings.Contains(string(content), "artifacts_dir: spine") {
		t.Errorf("expected artifacts_dir: spine, got: %s", content)
	}

	// Seed files in spine/ subdirectory
	if _, err := os.Stat(filepath.Join(dir, "spine", "governance", "charter.md")); err != nil {
		t.Error("expected spine/governance/charter.md to exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "spine", "workflows")); err != nil {
		t.Error("expected spine/workflows/ to exist")
	}
}

func TestInitRepo_SeedsBranchProtectionAtRepoRoot(t *testing.T) {
	// Whether or not the artifactsDir is a subdirectory, the branch-
	// protection config always lives at the repo root (ADR-009 §2.2).
	cases := []struct {
		name    string
		opts    cli.InitOpts
		wantDir string
	}{
		{"root artifacts", rootOpts(), "."},
		{"subdirectory artifacts", cli.InitOpts{ArtifactsDir: "spine", NoBranch: true}, "spine"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := cli.InitRepo(dir, tc.opts); err != nil {
				t.Fatalf("InitRepo: %v", err)
			}
			// Always at repo root, never under artifactsDir.
			path := filepath.Join(dir, ".spine", "branch-protection.yaml")
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected %s: %v", path, err)
			}
			if tc.wantDir != "." {
				misplaced := filepath.Join(dir, tc.wantDir, ".spine", "branch-protection.yaml")
				if _, err := os.Stat(misplaced); err == nil {
					t.Fatalf("unexpected duplicate at %s", misplaced)
				}
			}
		})
	}
}

func TestInitRepo_SeedBranchProtectionMatchesBootstrapDefaults(t *testing.T) {
	// Pin: the on-disk seed must parse back to the same rules
	// branchprotect.BootstrapDefaults() returns. Without this, the
	// policy layer (which relies on the projection handler to write
	// bootstrap rows when the file is missing) and init-repo could
	// drift silently — a new repo's "defaults" would not match the
	// "defaults" applied to a repo that never ran init-repo.
	dir := t.TempDir()
	if err := cli.InitRepo(dir, rootOpts()); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	seedPath := filepath.Join(dir, ".spine", "branch-protection.yaml")
	content, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	cfg, err := config.Parse(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("parse seed: %v\n%s", err, content)
	}
	if cfg.Version != config.SupportedVersion {
		t.Fatalf("seed version = %d, want %d", cfg.Version, config.SupportedVersion)
	}
	if !reflect.DeepEqual(cfg.Rules, branchprotect.BootstrapDefaults()) {
		t.Fatalf("seed rules diverged from BootstrapDefaults()\ngot:  %+v\nwant: %+v", cfg.Rules, branchprotect.BootstrapDefaults())
	}
}

func TestInitRepo_FailsWhenExistingSeedIsIgnored(t *testing.T) {
	// Regression: a previous run may have written the seed before
	// .gitignore was tightened. init-repo must still refuse on
	// subsequent runs rather than short-circuit on the
	// preserve-existing guard and let commitOnBranch silently skip
	// the untracked config.
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// Pre-seed both the config AND a later-tightened ignore rule.
	if err := os.MkdirAll(filepath.Join(dir, ".spine"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seedPath := filepath.Join(dir, ".spine", "branch-protection.yaml")
	if err := os.WriteFile(seedPath, []byte("version: 1\nrules: []\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".spine/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	err := cli.InitRepo(dir, rootOpts())
	if err == nil {
		t.Fatal("expected error when existing seed is ignored, got nil")
	}
	if !strings.Contains(err.Error(), ".gitignore") {
		t.Fatalf("error does not mention .gitignore: %v", err)
	}
}

func TestInitRepo_FailsClearlyWhenBranchProtectionIgnored(t *testing.T) {
	// If the repository's .gitignore excludes `.spine/`, the seed
	// would land on disk but `git add .` in commitOnBranch would
	// silently skip it — leaving the authoritative branch without a
	// protection config. init-repo must surface that clearly instead.
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	ignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(ignorePath, []byte(".spine/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	err := cli.InitRepo(dir, rootOpts())
	if err == nil {
		t.Fatal("expected error when .spine/ is gitignored, got nil")
	}
	if !strings.Contains(err.Error(), ".spine/branch-protection.yaml") {
		t.Fatalf("error does not mention the seed path: %v", err)
	}
	if !strings.Contains(err.Error(), ".gitignore") {
		t.Fatalf("error does not mention .gitignore: %v", err)
	}
}

func TestInitRepo_BranchProtectionIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := cli.InitRepo(dir, rootOpts()); err != nil {
		t.Fatalf("first InitRepo: %v", err)
	}

	// Simulate an operator edit — tighter ruleset than the defaults.
	seedPath := filepath.Join(dir, ".spine", "branch-protection.yaml")
	custom := []byte("version: 1\nrules:\n  - branch: staging\n    protections: [no-delete]\n")
	if err := os.WriteFile(seedPath, custom, 0o644); err != nil {
		t.Fatalf("write custom: %v", err)
	}

	if err := cli.InitRepo(dir, rootOpts()); err != nil {
		t.Fatalf("second InitRepo: %v", err)
	}

	got, _ := os.ReadFile(seedPath)
	if !bytes.Equal(got, custom) {
		t.Fatalf("seed overwritten operator edit\ngot:\n%s\nwant:\n%s", got, custom)
	}
}
