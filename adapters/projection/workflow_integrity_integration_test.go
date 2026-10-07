//go:build integration

package projection_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/projection"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/testutil"
)

// INIT-023 TASK-011 against real PostgreSQL. Written with the
// pre-existing projection API only, so the same file also runs against
// Spine 61994fe to show the before-state (recorded in the task's
// Implementation Notes).

type pgRecorder struct {
	mu     sync.Mutex
	events []domain.Event
}

func (r *pgRecorder) Emit(_ context.Context, e domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *pgRecorder) Subscribe(context.Context, domain.EventType, event.EventHandler) error {
	return nil
}

func (r *pgRecorder) rejections(t *testing.T) []map[string]any {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for i := range r.events {
		if r.events[i].Type != "projection_workflow_rejected" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(r.events[i].Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

const pgValidWorkflow = `id: task-default
name: Default Task Workflow
version: "1.0"
status: Active
description: Human-reviewed task workflow
applies_to: [Task]
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    outcomes:
      - {id: completed, name: Done, next_step: review}
  - id: review
    name: Human Code Review
    type: review
    outcomes:
      - {id: accepted, name: Accepted, next_step: end}
`

func headOf(t *testing.T, c *git.CLIClient) string {
	t.Helper()
	h, err := c.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// AC 29: a body PostgreSQL cannot store (U+0000 in a string — refused in
// jsonb and text columns) is a round_trip rejection, not a wedge. Before
// the fix the upsert failed, the sync stayed in `error` and
// last_synced_commit never advanced.
func TestIntegration_NULWorkflowIsRejectionNotWedge(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	db := store.NewTestStore(t)
	defer db.CleanupTestData(ctx, t)
	rec := &pgRecorder{}
	svc := projection.NewService(client, db, rec, time.Second)

	testutil.WriteFile(t, repo, "workflows/task-default.yaml", pgValidWorkflow)
	testutil.GitAdd(t, repo, ".", "c1")
	c1 := headOf(t, client)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}

	testutil.WriteFile(t, repo, "workflows/task-default.yaml",
		strings.Replace(pgValidWorkflow, "name: Default Task Workflow", `name: "a\0b"`, 1))
	testutil.GitAdd(t, repo, ".", "c2")
	c2 := headOf(t, client)
	syncErr := svc.IncrementalSync(ctx)

	state, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if syncErr != nil || state.Status != "idle" || state.LastSyncedCommit != c2 {
		t.Fatalf("sync must advance past a content rejection: err=%v state=%+v (c1=%s c2=%s)", syncErr, state, c1, c2)
	}
	row, err := db.GetWorkflowProjection(ctx, "workflows/task-default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if row.SourceCommit != c1 || row.Status != "Quarantined" || row.Name != "Default Task Workflow" {
		t.Fatalf("the c1 row must be kept, quarantined: %+v", row)
	}
	active, err := db.ListActiveWorkflowProjections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("a quarantined row must not be listed as Active: %+v", active)
	}
	rej := rec.rejections(t)
	if len(rej) != 1 || rej[0]["check"] != "round_trip" || rej[0]["action"] != "kept" {
		t.Fatalf("expected one round_trip/kept rejection, got %+v", rej)
	}
}

// AC 10(g) against PostgreSQL: the store's all-rows capability lets the
// sweep remove a smuggled row that was later set Deprecated (in-flight
// runs load rows by path with no status filter).
func TestIntegration_SweepRemovesDeprecatedSmuggledRow(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	db := store.NewTestStore(t)
	defer db.CleanupTestData(ctx, t)

	// The folded look-alike key (U+017F) a strict reader refuses.
	smuggled := pgValidWorkflow + "ſteps:\n  - id: execute\n    name: Execute Task\n    type: manual\n    outcomes:\n      - {id: completed, name: Done, next_step: end}\n"
	testutil.WriteFile(t, repo, "workflows/task-default.yaml", smuggled)
	testutil.GitAdd(t, repo, ".", "c1")
	head := headOf(t, client)

	// The row as the pre-fix projection stored it (json.Marshal sorts
	// keys, so the folded key is decoded last and wins), set Deprecated.
	def := `{"applies_to":["Task"],"description":"d","entry_step":"execute","id":"task-default","name":"Default Task Workflow",` +
		`"status":"Deprecated","steps":[{"id":"execute","name":"Execute Task","outcomes":[{"id":"completed","name":"Done","next_step":"review"}],"type":"manual"}],` +
		`"version":"1.0","ſteps":[{"id":"execute","name":"Execute Task","outcomes":[{"id":"completed","name":"Done","next_step":"end"}],"type":"manual"}]}`
	if err := db.UpsertWorkflowProjection(ctx, &store.WorkflowProjection{
		WorkflowPath: "workflows/task-default.yaml", WorkflowID: "task-default", Name: "Default Task Workflow",
		Version: "1.0", Status: "Deprecated", AppliesTo: []byte(`["Task"]`), Definition: []byte(def), SourceCommit: head,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSyncState(ctx, &store.SyncState{LastSyncedCommit: head, Status: "idle"}); err != nil {
		t.Fatal(err)
	}

	svc := projection.NewService(client, db, nil, time.Second)
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetWorkflowProjection(ctx, "workflows/task-default.yaml"); err == nil {
		t.Fatal("the Deprecated smuggled row must be removed by the sweep")
	}
}
