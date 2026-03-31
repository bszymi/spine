//go:build scenario

package scenarios_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// --- Scenario 1: Root directory backward compat ---

func TestArtifactsDir_RootBackwardCompat(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "artifacts-dir-root-backward-compat",
		Description: "artifacts_dir: / works identically to pre-config behavior",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []engine.Step{
			{
				Name: "create-artifact-at-root",
				Action: func(sc *engine.ScenarioContext) error {
					content := "---\ntype: Governance\ntitle: Root Test\nstatus: Living Document\nversion: \"0.1\"\n---\n# Root Test\n"
					result, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/root-test.md", content)
					if err != nil {
						return err
					}
					sc.Set("artifact_path", result.Artifact.Path)
					return nil
				},
			},
			{
				Name: "verify-file-at-repo-root",
				Action: func(sc *engine.ScenarioContext) error {
					path := sc.MustGet("artifact_path").(string)
					if !sc.Repo.FileExists(path) {
						sc.T.Errorf("expected file %s at repo root", path)
					}
					return nil
				},
			},
			engine.SyncProjections(),
			{
				Name: "verify-projection-synced",
				Action: func(sc *engine.ScenarioContext) error {
					path := sc.MustGet("artifact_path").(string)
					proj, err := sc.Runtime.Store.GetArtifactProjection(sc.Ctx, path)
					if err != nil {
						return fmt.Errorf("get projection: %w", err)
					}
					if proj.Title != "Root Test" {
						sc.T.Errorf("expected title Root Test, got %s", proj.Title)
					}
					return nil
				},
			},
		},
	})
}

// --- Scenario 2: Subdirectory artifacts ---

func TestArtifactsDir_Subdirectory(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "artifacts-dir-subdirectory",
		Description: "artifacts_dir: spine places artifacts in spine/ subdirectory",
		EnvOpts: []harness.EnvOption{
			harness.WithArtifactsDir("spine"),
		},
		Steps: []engine.Step{
			{
				Name: "seed-spine-directory",
				Action: func(sc *engine.ScenarioContext) error {
					// Create the spine/governance directory
					dir := filepath.Join(sc.Repo.Dir, "spine", "governance")
					return os.MkdirAll(dir, 0o755)
				},
			},
			{
				Name: "create-artifact-in-subdirectory",
				Action: func(sc *engine.ScenarioContext) error {
					content := "---\ntype: Governance\ntitle: Subdir Test\nstatus: Living Document\nversion: \"0.1\"\n---\n# Subdir Test\n"
					result, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/subdir-test.md", content)
					if err != nil {
						return err
					}
					sc.Set("artifact_path", result.Artifact.Path)
					return nil
				},
			},
			{
				Name: "verify-file-in-subdirectory",
				Action: func(sc *engine.ScenarioContext) error {
					// File should be at spine/governance/subdir-test.md in the repo
					repoPath := filepath.Join("spine", "governance", "subdir-test.md")
					if !sc.Repo.FileExists(repoPath) {
						sc.T.Errorf("expected file at %s", repoPath)
					}
					// Artifact path should remain artifacts-relative
					path := sc.MustGet("artifact_path").(string)
					if path != "governance/subdir-test.md" {
						sc.T.Errorf("expected artifact path governance/subdir-test.md, got %s", path)
					}
					return nil
				},
			},
			{
				Name: "read-artifact-back",
				Action: func(sc *engine.ScenarioContext) error {
					a, err := sc.Runtime.Artifacts.Read(sc.Ctx, "governance/subdir-test.md", "")
					if err != nil {
						return fmt.Errorf("read artifact: %w", err)
					}
					if a.Title != "Subdir Test" {
						sc.T.Errorf("expected title Subdir Test, got %s", a.Title)
					}
					return nil
				},
			},
		},
	})
}

// --- Scenario 5: Missing .spine.yaml defaults to root ---

func TestArtifactsDir_MissingConfig(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "artifacts-dir-missing-config-defaults-root",
		Description: "Without .spine.yaml, artifacts_dir defaults to / (root)",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			// No WithArtifactsDir — defaults to "/"
		},
		Steps: []engine.Step{
			{
				Name: "create-artifact-default-root",
				Action: func(sc *engine.ScenarioContext) error {
					content := "---\ntype: Governance\ntitle: Default Root\nstatus: Living Document\nversion: \"0.1\"\n---\n# Default Root\n"
					_, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/default-root.md", content)
					return err
				},
			},
			{
				Name: "verify-at-repo-root",
				Action: func(sc *engine.ScenarioContext) error {
					if !sc.Repo.FileExists("governance/default-root.md") {
						sc.T.Error("expected file at repo root")
					}
					return nil
				},
			},
		},
	})
}
