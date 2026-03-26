package engine

import (
	"fmt"

	"github.com/bszymi/spine/internal/scenariotest/assert"
)

// Composable step builders for common scenario operations.
// Each builder returns a Step that can be used in any scenario.

// CreateArtifact returns a step that creates an artifact at the given path.
// Stores the artifact path in state under the given stateKey.
func CreateArtifact(path, content, stateKey string) Step {
	return Step{
		Name: "create-artifact-" + path,
		Action: func(sc *ScenarioContext) error {
			a, err := sc.Runtime.Artifacts.Create(sc.Ctx, path, content)
			if err != nil {
				return fmt.Errorf("create artifact %s: %w", path, err)
			}
			sc.Set(stateKey, a.Path)
			return nil
		},
	}
}

// WriteAndCommit returns a step that writes a file to the repo and commits it.
// This bypasses the artifact service — use for setup that doesn't need
// governance (e.g., seeding raw files).
func WriteAndCommit(path, content, commitMsg string) Step {
	return Step{
		Name: "write-" + path,
		Action: func(sc *ScenarioContext) error {
			sc.Repo.WriteArtifact(sc.T, path, content)
			sc.Repo.CommitAll(sc.T, commitMsg)
			return nil
		},
	}
}

// SyncProjections returns a step that runs a full projection rebuild.
func SyncProjections() Step {
	return Step{
		Name: "sync-projections",
		Action: func(sc *ScenarioContext) error {
			return sc.Runtime.Projections.FullRebuild(sc.Ctx)
		},
	}
}

// AssertFileExists returns a step that asserts a file exists in the repo.
func AssertFileExists(path string) Step {
	return Step{
		Name: "assert-file-exists-" + path,
		Action: func(sc *ScenarioContext) error {
			assert.FileExists(sc.T, sc.Repo, path)
			return nil
		},
	}
}

// AssertFileContains returns a step that asserts a file contains a substring.
func AssertFileContains(path, substring string) Step {
	return Step{
		Name: "assert-file-contains-" + path,
		Action: func(sc *ScenarioContext) error {
			assert.FileContains(sc.T, sc.Repo, path, substring)
			return nil
		},
	}
}

// AssertProjection returns a step that asserts a projection field value.
func AssertProjection(path, field, expected string) Step {
	return Step{
		Name: fmt.Sprintf("assert-projection-%s-%s", path, field),
		Action: func(sc *ScenarioContext) error {
			assert.ArtifactProjectionField(sc.T, sc.DB, sc.Ctx, path, field, expected)
			return nil
		},
	}
}

// ExpectError returns a step that expects the given action to fail.
// If the action succeeds, the step fails the test. If it fails with
// an error, the step succeeds (the error is the expected outcome).
// Optionally stores the error message in state under stateKey.
func ExpectError(name string, action func(sc *ScenarioContext) error, stateKey string) Step {
	return Step{
		Name: "expect-error-" + name,
		Action: func(sc *ScenarioContext) error {
			err := action(sc)
			if err == nil {
				sc.T.Fatalf("expected error in %q but succeeded", name)
			}
			if stateKey != "" {
				sc.Set(stateKey, err.Error())
			}
			return nil
		},
	}
}

// Compose returns a single step that executes multiple steps sequentially.
// Useful for grouping related operations into a reusable unit.
func Compose(name string, steps ...Step) Step {
	return Step{
		Name: name,
		Action: func(sc *ScenarioContext) error {
			for _, step := range steps {
				if err := step.Action(sc); err != nil {
					return fmt.Errorf("sub-step %q: %w", step.Name, err)
				}
			}
			return nil
		},
	}
}

// Steps concatenates multiple step slices into a single slice.
// Useful for building scenarios from reusable step sequences.
func Steps(groups ...[]Step) []Step {
	var result []Step
	for _, g := range groups {
		result = append(result, g...)
	}
	return result
}

// CommonSetup returns steps that seed governance, write an artifact,
// and sync projections — a frequently reused setup sequence.
func CommonSetup(artifactPath, content, stateKey string) []Step {
	return []Step{
		CreateArtifact(artifactPath, content, stateKey),
		SyncProjections(),
	}
}
