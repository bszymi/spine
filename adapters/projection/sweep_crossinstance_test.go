package projection

// INIT-023 wave 2 cross-task review: the first-sync workflow-row sweep
// judges stored rows at a head the instance resolved before it listed
// them. In SMP several instances project one workspace (a Service per
// replica per warm workspace, and a fresh instance — so a fresh sweep —
// on every re-warm or pool rotation). A row another instance projected at
// a newer head in that window is absent at the stale head, so the sweep
// deletes it; when the sweeping instance then took the up-to-date early
// return it left sync_state at the other instance's newer head, nothing
// re-diffed the window, and the row stayed lost.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
)

// staleHeadGit resolves HEAD to a fixed, older commit, and runs before()
// once just before answering: the other instance's sync that lands between
// this instance's head resolution and its sweep's row listing.
type staleHeadGit struct {
	git.GitClient
	head   string
	once   sync.Once
	before func()
}

func (g *staleHeadGit) Head(context.Context) (string, error) {
	g.once.Do(g.before)
	return g.head, nil
}

// Mutation: taking the early return without re-recording sync_state after
// a sweep that wrote rows fails "the deleted row comes back".
func TestSweep_StaleHeadInstanceDoesNotLoseANewerRow(t *testing.T) {
	const newPath = "workflows/new-flow.yaml"
	repo := newScratchRepo(t)
	repo.write(wfPath, reviewedWorkflow)
	c1 := repo.commit("c1")
	st := allRowsStore{newMapStore()}

	// Replica B is warm and in sync at c1 (its sweep already ran).
	b := NewService(repo.git, st, nil, 0)
	if err := b.FullRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A workflow writer pushes c2, which adds new-flow.
	repo.write(newPath, strings.Replace(
		strings.Replace(reviewedWorkflow, "id: task-default", "id: new-flow", 1),
		"applies_to: [Task]", "applies_to: [Epic]", 1))
	c2 := repo.commit("c2: add new-flow")

	// Replica A re-warms the workspace: it reads sync_state c1/idle and
	// resolves HEAD as c1; before its sweep lists the rows, B's loop
	// projects c2 (new-flow's row) and records c2/idle.
	var bErr error
	a := NewService(&staleHeadGit{GitClient: repo.git, head: c1, before: func() {
		bErr = b.IncrementalSync(context.Background())
	}}, st, nil, 0)
	if err := a.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bErr != nil {
		t.Fatal(bErr)
	}
	if st.row(newPath) != nil {
		t.Fatal("precondition: A's sweep at its stale head deletes the row B projected at c2")
	}

	// The next ticks — B's loop, and a fresh instance — must restore it.
	if err := b.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := NewService(repo.git, st, nil, 0).IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := st.row(newPath); r == nil || r.Status != "Active" || r.SourceCommit != c2 {
		t.Fatalf("the deleted row comes back: got %+v", r)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("sync state: %+v", got)
	}
	if r := st.row(wfPath); r == nil || r.Status != "Active" {
		t.Fatalf("the unchanged workflow is untouched: %+v", r)
	}
}

// A sweep that wrote nothing keeps the early return free of writes: an
// instance at an up-to-date, idle state does not touch sync_state.
func TestSweep_NoChangeKeepsTheEarlyReturnReadOnly(t *testing.T) {
	repo := newScratchRepo(t)
	repo.write(wfPath, reviewedWorkflow)
	head := repo.commit("c1")
	st := allRowsStore{newMapStore()}
	if err := NewService(repo.git, st, nil, 0).FullRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A marker no sync_state write would keep.
	st.mu.Lock()
	st.state = &store.SyncState{LastSyncedCommit: head, Status: "idle", ErrorDetail: "marker"}
	st.mu.Unlock()
	if err := NewService(repo.git, st, nil, 0).IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.LastSyncedCommit != head || got.Status != "idle" || got.ErrorDetail != "marker" {
		t.Fatalf("a sweep with no change must not write sync_state: %+v", got)
	}
}
