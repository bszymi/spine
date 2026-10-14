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
// a sweep that wrote rows fails "the deleted row comes back", through the
// store's SetSyncBaseline capability and through the plain write a store
// without it gets.
func TestSweep_StaleHeadInstanceDoesNotLoseANewerRow(t *testing.T) {
	for name, newStore := range map[string]func() (store.Store, *mapStore){
		"baseline_capability": func() (store.Store, *mapStore) {
			ms := newMapStore()
			return allRowsStore{ms}, ms
		},
		"plain_store": func() (store.Store, *mapStore) {
			ms := newMapStore()
			return ms, ms
		},
	} {
		t.Run(name, func(t *testing.T) {
			st, ms := newStore()
			staleHeadInstanceDoesNotLoseANewerRow(t, st, ms)
		})
	}
}

func staleHeadInstanceDoesNotLoseANewerRow(t *testing.T, st store.Store, ms *mapStore) {
	const newPath = "workflows/new-flow.yaml"
	repo := newScratchRepo(t)
	repo.write(wfPath, reviewedWorkflow)
	c1 := repo.commit("c1")

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
	if ms.row(newPath) != nil {
		t.Fatal("precondition: A's sweep at its stale head deletes the row B projected at c2")
	}

	// The next ticks — B's loop, and a fresh instance — must restore it.
	if err := b.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := NewService(repo.git, st, nil, 0).IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := ms.row(newPath); r == nil || r.Status != "Active" || r.SourceCommit != c2 {
		t.Fatalf("the deleted row comes back: got %+v", r)
	}
	if got := ms.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("sync state: %+v", got)
	}
	if r := ms.row(wfPath); r == nil || r.Status != "Active" {
		t.Fatalf("the unchanged workflow is untouched: %+v", r)
	}
}

// INIT-023 codex 6.1 catch-up: the swept early return re-records only the
// baseline. A status another instance wrote between this sync's read of
// idle@head and that write — its operator rebuild starting, or failing —
// is kept; a state with no baseline (a rebuild's partial wipe) is left as
// it is, so the next sync rebuilds. Before, the write was an unconditional
// idle@head: a rebuild in flight looked finished over a half-written
// projection, and a same-head rebuild error was erased, so the next tick
// took the up-to-date return and skipped the forced branch-protection
// retry.
//
// Mutation: writing UpdateSyncState{head, idle} on the swept path (the
// code before the catch-up) fails every sub-test.
func TestSweep_BaselineResetKeepsAConcurrentStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		other    func(c2 string) store.SyncState // what the other instance writes
		wantBase func(c1, c2 string) string      // last_synced_commit after A
	}{
		{
			name:     "rebuilding",
			other:    func(c2 string) store.SyncState { return store.SyncState{LastSyncedCommit: c2, Status: "rebuilding"} },
			wantBase: func(c1, _ string) string { return c1 },
		},
		{
			name: "error",
			other: func(c2 string) store.SyncState {
				return store.SyncState{LastSyncedCommit: c2, Status: "error", ErrorDetail: "1 projection errors during rebuild"}
			},
			wantBase: func(c1, _ string) string { return c1 },
		},
		{
			name: "no_baseline",
			other: func(string) store.SyncState {
				return store.SyncState{Status: "error", ErrorDetail: "delete projections: simulated"}
			},
			wantBase: func(string, string) string { return "" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const newPath = "workflows/new-flow.yaml"
			repo := newScratchRepo(t)
			repo.write(wfPath, reviewedWorkflow)
			c1 := repo.commit("c1")
			st := allRowsStore{newMapStore()}
			b := NewService(repo.git, st, nil, 0)
			if err := b.FullRebuild(context.Background()); err != nil {
				t.Fatal(err)
			}
			repo.write(newPath, strings.Replace(
				strings.Replace(reviewedWorkflow, "id: task-default", "id: new-flow", 1),
				"applies_to: [Task]", "applies_to: [Epic]", 1))
			c2 := repo.commit("c2: add new-flow")

			// A reads idle@c1 and resolves HEAD as c1; before its sweep
			// lists the rows, B projects c2 and then records the status
			// under test.
			want := tc.other(c2)
			var bErr error
			a := NewService(&staleHeadGit{GitClient: repo.git, head: c1, before: func() {
				if bErr = b.IncrementalSync(context.Background()); bErr == nil {
					bErr = st.UpdateSyncState(context.Background(), &want)
				}
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
			want.LastSyncedCommit = tc.wantBase(c1, c2)
			if got := st.syncState(); got.LastSyncedCommit != want.LastSyncedCommit ||
				got.Status != want.Status || got.ErrorDetail != want.ErrorDetail {
				t.Fatalf("sync state after A's swept early return: %+v, want %+v", got, want)
			}

			if tc.name != "error" {
				return
			}
			// The kept error is retried from A's baseline: the window
			// c1..c2 is re-diffed (restoring the row) and the
			// branch-protection projection is forced.
			bpBefore := st.bpUpserts
			if err := NewService(repo.git, st, nil, 0).IncrementalSync(context.Background()); err != nil {
				t.Fatal(err)
			}
			if r := st.row(newPath); r == nil || r.Status != "Active" || r.SourceCommit != c2 {
				t.Fatalf("the deleted row comes back: got %+v", r)
			}
			if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
				t.Fatalf("sync state after the retry: %+v", got)
			}
			if st.bpUpserts == bpBefore {
				t.Fatal("the retry of an error state re-projects branch protection")
			}
		})
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
