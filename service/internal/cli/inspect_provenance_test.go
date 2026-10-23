package cli_test

// INIT-024 TASK-007, review finding N-17 (adversarial review): `spine run
// inspect` printed run-branch evidence as "passed" with no marker. It now
// flags every repository the server reports "verified": false.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bszymi/spine/service/internal/cli"
)

func inspectOutput(t *testing.T, evidence map[string]any) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run_id": "run-n17", "status": "active", "task_path": "tasks/t.md",
			"workflow_id": "wf-1", "current_step_id": "review", "trace_id": "trace-123456789012",
			"evidence": evidence,
		})
	}))
	defer ts.Close()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := cli.InspectRun(context.Background(), cli.NewClient(ts.URL, ""), "run-n17", cli.FormatTable)
	w.Close()
	os.Stdout = old
	if runErr != nil {
		t.Fatalf("InspectRun: %v", runErr)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

func TestInspectRun_ShowsUnverifiedEvidence(t *testing.T) {
	out := inspectOutput(t, map[string]any{
		"status":                  "passed",
		"unverified_repositories": []any{"billing"},
		"repositories": []any{
			map[string]any{"repository_id": "billing", "present": true, "status": "passed", "verified": false, "source_ref": "spine/run/run-n17-abc"},
			map[string]any{"repository_id": "spine", "present": true, "status": "passed", "verified": true, "source_ref": "main"},
		},
	})
	// The repository id is printed bold (ANSI codes between it and the
	// colon), so match on the id alone.
	var billing, spine string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "  ") && strings.Contains(line, "billing"):
			billing = line
		case strings.HasPrefix(line, "  ") && strings.Contains(line, "spine") && !strings.Contains(line, "spine/run/"):
			spine = line
		}
	}
	if !strings.Contains(billing, "UNVERIFIED (read from spine/run/run-n17-abc, author-writable)") {
		t.Errorf("run-branch evidence line lacks the marker: %q\n%s", billing, out)
	}
	if strings.Contains(spine, "UNVERIFIED") {
		t.Errorf("verified evidence marked unverified: %q", spine)
	}
	if !strings.Contains(out, "UNVERIFIED for 1 repository") {
		t.Errorf("headline does not say part of the rollup is unverified:\n%s", out)
	}
}

// A server that predates the provenance fields sends no "verified" key:
// no marker is invented.
func TestInspectRun_NoProvenanceFieldsNoMarker(t *testing.T) {
	out := inspectOutput(t, map[string]any{
		"status": "passed",
		"repositories": []any{
			map[string]any{"repository_id": "spine", "present": true, "status": "passed"},
		},
	})
	if strings.Contains(out, "UNVERIFIED") {
		t.Errorf("marker without provenance fields:\n%s", out)
	}
}
