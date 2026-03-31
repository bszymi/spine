package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/cli"
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
