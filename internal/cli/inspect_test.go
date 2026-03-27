package cli_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bszymi/spine/internal/cli"
)

func TestInspectRun_JSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"run_id": "run-abc", "status": "active", "task_path": "tasks/t.md",
			"workflow_id": "wf-1", "workflow_version": "1.0",
			"current_step_id": "start", "trace_id": "trace-123456789012",
			"step_executions": []any{
				map[string]any{"execution_id": "run-abc-start-1", "step_id": "start", "status": "completed", "attempt": 1, "outcome_id": "done"},
			},
		})
	}))
	defer ts.Close()

	client := cli.NewClient(ts.URL, "")
	err := cli.InspectRun(context.Background(), client, "run-abc", cli.FormatJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInspectRun_Table(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"run_id": "run-abc", "status": "failed", "task_path": "tasks/t.md",
			"workflow_id": "wf-1", "workflow_version": "1.0",
			"current_step_id": "review", "trace_id": "trace-123456789012",
			"step_executions": []any{
				map[string]any{
					"execution_id": "run-abc-review-1", "step_id": "review", "status": "failed", "attempt": 1,
					"error_detail": map[string]any{"classification": "permanent_error", "message": "actor crashed"},
				},
			},
		})
	}))
	defer ts.Close()

	client := cli.NewClient(ts.URL, "")
	err := cli.InspectRun(context.Background(), client, "run-abc", cli.FormatTable)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAll_JSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"status":          "passed",
			"total_artifacts": 5,
			"passed":          5,
			"warnings":        0,
			"failed":          0,
			"results":         []any{},
		})
	}))
	defer ts.Close()

	client := cli.NewClient(ts.URL, "")
	err := cli.ValidateAll(context.Background(), client, cli.FormatJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAll_WithIssues(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"status":          "failed",
			"total_artifacts": 5,
			"passed":          4,
			"warnings":        0,
			"failed":          1,
			"results": []any{
				map[string]any{
					"status": "failed",
					"errors": []any{
						map[string]any{"rule_id": "SI-001", "severity": "error", "message": "parent missing", "classification": "structural_error"},
					},
				},
			},
		})
	}))
	defer ts.Close()

	client := cli.NewClient(ts.URL, "")
	err := cli.ValidateAll(context.Background(), client, cli.FormatTable)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateArtifact(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"status":   "passed",
			"errors":   []any{},
			"warnings": []any{},
		})
	}))
	defer ts.Close()

	client := cli.NewClient(ts.URL, "")
	err := cli.ValidateArtifact(context.Background(), client, "tasks/task.md", cli.FormatTable)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestColorStatus(t *testing.T) {
	// Just verify no panics on various statuses.
	for _, s := range []string{"completed", "failed", "active", "waiting", "unknown", ""} {
		_ = cli.ColorStatus(s)
	}
}
