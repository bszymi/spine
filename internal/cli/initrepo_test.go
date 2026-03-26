package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/cli"
)

func TestInitRepo_CreatesDirectories(t *testing.T) {
	dir := t.TempDir()

	if err := cli.InitRepo(dir); err != nil {
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

	if err := cli.InitRepo(dir); err != nil {
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
	if err := cli.InitRepo(dir); err != nil {
		t.Fatalf("first InitRepo: %v", err)
	}

	// Write custom content to an existing file.
	charterPath := filepath.Join(dir, "governance/charter.md")
	custom := []byte("custom content")
	if err := os.WriteFile(charterPath, custom, 0o644); err != nil {
		t.Fatalf("write custom: %v", err)
	}

	// Second run — should not overwrite.
	if err := cli.InitRepo(dir); err != nil {
		t.Fatalf("second InitRepo: %v", err)
	}

	content, _ := os.ReadFile(charterPath)
	if string(content) != "custom content" {
		t.Error("expected existing file to not be overwritten")
	}
}

func TestInitRepo_InitializesGit(t *testing.T) {
	dir := t.TempDir()

	if err := cli.InitRepo(dir); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		t.Error("expected .git directory to exist")
	}
}

func TestInitRepo_SkipsGitIfAlreadyInitialized(t *testing.T) {
	dir := t.TempDir()

	// Pre-create .git directory.
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	// Should not fail or re-initialize.
	if err := cli.InitRepo(dir); err != nil {
		t.Fatalf("InitRepo with existing .git: %v", err)
	}
}
