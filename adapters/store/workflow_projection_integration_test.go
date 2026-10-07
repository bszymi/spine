//go:build integration

package store_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/adapters/store"
)

// INIT-023 TASK-011: ListWorkflowProjections is the optional all-rows
// capability the projection's workflow-row sweep asserts. It returns
// every row regardless of status, ordered by path, while
// ListActiveWorkflowProjections keeps filtering on status = 'Active'.
// The Quarantined marker needs no schema change (status is plain text).
func TestListWorkflowProjections_AllStatuses(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	defer s.CleanupTestData(ctx, t)

	for _, r := range []struct{ path, status string }{
		{"workflows/b.yaml", "Active"},
		{"workflows/a.yaml", "Quarantined"},
		{"workflows/c.yaml", "Deprecated"},
		{"workflows/steps/x.yaml", ""},
	} {
		if err := s.UpsertWorkflowProjection(ctx, &store.WorkflowProjection{
			WorkflowPath: r.path, WorkflowID: r.path, Name: "n", Version: "1", Status: r.status,
			AppliesTo: []byte(`["Task"]`), Definition: []byte(`{"id":"x"}`), SourceCommit: "c",
		}); err != nil {
			t.Fatalf("upsert %s: %v", r.path, err)
		}
	}

	var lister interface {
		ListWorkflowProjections(ctx context.Context) ([]store.WorkflowProjection, error)
	} = s
	all, err := lister.ListWorkflowProjections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := range all {
		got = append(got, all[i].WorkflowPath+"="+all[i].Status)
	}
	want := []string{"workflows/a.yaml=Quarantined", "workflows/b.yaml=Active", "workflows/c.yaml=Deprecated", "workflows/steps/x.yaml="}
	if len(got) != len(want) {
		t.Fatalf("ListWorkflowProjections = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListWorkflowProjections = %v, want %v", got, want)
		}
	}

	active, err := s.ListActiveWorkflowProjections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].WorkflowPath != "workflows/b.yaml" {
		t.Fatalf("ListActiveWorkflowProjections = %+v, want only the Active row", active)
	}
}
