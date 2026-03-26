package cli_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bszymi/spine/internal/cli"
)

func TestQueryArtifacts_SendsFilters(t *testing.T) {
	var receivedPath string
	var receivedParams map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedParams = map[string]string{
			"type":   r.URL.Query().Get("type"),
			"status": r.URL.Query().Get("status"),
			"parent": r.URL.Query().Get("parent"),
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	defer ts.Close()

	client := cli.NewClient(ts.URL, "")
	err := cli.QueryArtifacts(context.Background(), client, "Task", "Completed", "/init/epic.md", cli.FormatJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedPath != "/api/v1/query/artifacts" {
		t.Errorf("expected path /api/v1/query/artifacts, got %s", receivedPath)
	}
	if receivedParams["type"] != "Task" {
		t.Errorf("expected type=Task, got %s", receivedParams["type"])
	}
	if receivedParams["status"] != "Completed" {
		t.Errorf("expected status=Completed, got %s", receivedParams["status"])
	}
}

func TestQueryRuns_SendsFilters(t *testing.T) {
	var receivedParams map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedParams = map[string]string{
			"task":   r.URL.Query().Get("task"),
			"status": r.URL.Query().Get("status"),
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	defer ts.Close()

	client := cli.NewClient(ts.URL, "")
	err := cli.QueryRuns(context.Background(), client, "tasks/task.md", "active", cli.FormatJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedParams["task"] != "tasks/task.md" {
		t.Errorf("expected task=tasks/task.md, got %s", receivedParams["task"])
	}
	if receivedParams["status"] != "active" {
		t.Errorf("expected status=active, got %s", receivedParams["status"])
	}
}

func TestQueryGraph_SendsPathAndDepth(t *testing.T) {
	var receivedParams map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedParams = map[string]string{
			"path":  r.URL.Query().Get("path"),
			"depth": r.URL.Query().Get("depth"),
		}
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer ts.Close()

	client := cli.NewClient(ts.URL, "")
	err := cli.QueryGraph(context.Background(), client, "initiatives/init.md", 3, cli.FormatJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedParams["path"] != "initiatives/init.md" {
		t.Errorf("expected path=initiatives/init.md, got %s", receivedParams["path"])
	}
	if receivedParams["depth"] != "3" {
		t.Errorf("expected depth=3, got %s", receivedParams["depth"])
	}
}

func TestQueryHistory_SendsPath(t *testing.T) {
	var receivedPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Query().Get("path")
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer ts.Close()

	client := cli.NewClient(ts.URL, "")
	err := cli.QueryHistory(context.Background(), client, "tasks/task.md", cli.FormatJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedPath != "tasks/task.md" {
		t.Errorf("expected path=tasks/task.md, got %s", receivedPath)
	}
}

func TestQuery_FailsGracefully(t *testing.T) {
	// Non-existent server — should return error, not panic.
	client := cli.NewClient("http://localhost:1", "")
	err := cli.QueryArtifacts(context.Background(), client, "", "", "", cli.FormatJSON)
	if err == nil {
		t.Error("expected error for unreachable server")
	}
}
