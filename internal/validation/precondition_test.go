package validation_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
)

func TestPreconditionNoPreconditions(t *testing.T) {
	fs := newFakeStore()
	engine := validation.NewEngine(fs)
	step := domain.StepDefinition{ID: "step1"}

	result := validation.EvaluatePreconditions(context.Background(), engine, step, "some/path.md")
	if result.Status != "passed" {
		t.Errorf("expected passed, got %s", result.Status)
	}
}

func TestPreconditionUnknownType(t *testing.T) {
	fs := newFakeStore()
	engine := validation.NewEngine(fs)
	step := domain.StepDefinition{
		ID: "step1",
		Preconditions: []domain.Precondition{
			{Type: "unknown_type"},
		},
	}

	result := validation.EvaluatePreconditions(context.Background(), engine, step, "some/path.md")
	if result.Status != "passed" {
		t.Errorf("expected passed for unknown precondition type, got %s", result.Status)
	}
}

func TestPreconditionCrossArtifactValidPasses(t *testing.T) {
	fs := newFakeStore()
	// Add a clean artifact
	addArtifact(fs, "initiatives/test/task.md", "Task", "Pending", nil, nil)

	engine := validation.NewEngine(fs)
	step := domain.StepDefinition{
		ID: "step1",
		Preconditions: []domain.Precondition{
			{Type: "cross_artifact_valid"},
		},
	}

	result := validation.EvaluatePreconditions(context.Background(), engine, step, "initiatives/test/task.md")
	if result.Status == "failed" {
		t.Errorf("expected non-failed result for clean artifact, got failed: %v", result.Errors)
	}
}

func TestPreconditionCrossArtifactValidFails(t *testing.T) {
	fs := newFakeStore()
	// Add an artifact with broken parent reference
	addArtifact(fs, "initiatives/test/task.md", "Task", "Pending",
		map[string]string{"epic": "/nonexistent/epic.md", "initiative": "/nonexistent/init.md"}, nil)

	engine := validation.NewEngine(fs)
	step := domain.StepDefinition{
		ID: "step1",
		Preconditions: []domain.Precondition{
			{Type: "cross_artifact_valid"},
		},
	}

	result := validation.EvaluatePreconditions(context.Background(), engine, step, "initiatives/test/task.md")
	if result.Status != "failed" {
		t.Errorf("expected failed for artifact with broken parent, got %s", result.Status)
	}
	if len(result.Errors) == 0 {
		t.Error("expected errors in result")
	}
}

func TestPreconditionCustomArtifactPath(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "initiatives/other/task.md", "Task", "Pending", nil, nil)

	engine := validation.NewEngine(fs)
	step := domain.StepDefinition{
		ID: "step1",
		Preconditions: []domain.Precondition{
			{Type: "cross_artifact_valid", Config: map[string]string{
				"artifact_path": "initiatives/other/task.md",
			}},
		},
	}

	result := validation.EvaluatePreconditions(context.Background(), engine, step, "initiatives/test/task.md")
	// Should validate the custom path, not the task path
	if result.Status == "failed" {
		t.Errorf("expected non-failed for clean custom path, got failed: %v", result.Errors)
	}
}

func TestPreconditionWarningsDoNotBlock(t *testing.T) {
	fs := newFakeStore()
	// Add a task with empty content (triggers SA-001 warning) but no errors
	fs.artifacts["initiatives/test/task.md"] = &store.ArtifactProjection{
		ArtifactPath: "initiatives/test/task.md",
		ArtifactType: "Task",
		Status:       "Pending",
		Content:      "", // triggers SA-001 warning
	}

	engine := validation.NewEngine(fs)
	step := domain.StepDefinition{
		ID: "step1",
		Preconditions: []domain.Precondition{
			{Type: "cross_artifact_valid"},
		},
	}

	result := validation.EvaluatePreconditions(context.Background(), engine, step, "initiatives/test/task.md")
	if result.Status == "failed" {
		t.Errorf("expected warnings to not block, got failed: %v", result.Errors)
	}
}
