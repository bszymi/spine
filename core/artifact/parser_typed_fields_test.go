package artifact_test

import (
	"testing"

	"github.com/bszymi/spine/core/artifact"
)

// INIT-023 TASK-007 AC 9 (from the audit's skeptic-precond probe): the
// parser contract is that the typed front-matter fields id, type, title
// and status live on the Artifact struct and are NOT duplicated into
// Metadata. The engine's strict precondition evaluation reads the typed
// fields for those keys; this pins the parser side so neither half
// drifts silently.
func TestParse_TypedFieldsNotInMetadata(t *testing.T) {
	src := []byte("---\nid: TASK-001\ntype: Task\ntitle: T\nstatus: In Progress\nepic: /e.md\ninitiative: /i.md\n---\n# body\n")
	a, err := artifact.Parse("initiatives/x/tasks/TASK-001.md", src)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "In Progress" || a.ID != "TASK-001" || a.Type != "Task" || a.Title != "T" {
		t.Fatalf("typed fields = %q/%q/%q/%q", a.ID, a.Type, a.Title, a.Status)
	}
	for _, key := range []string{"id", "type", "title", "status"} {
		if _, ok := a.Metadata[key]; ok {
			t.Fatalf("Metadata[%q] is present; the parser must keep typed fields out of Metadata", key)
		}
	}
}
