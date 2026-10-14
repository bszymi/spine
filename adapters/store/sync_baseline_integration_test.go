//go:build integration

package store_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/adapters/store"
)

// INIT-023 codex 6.1 catch-up: SetSyncBaseline is the optional capability
// the projection's swept early return asserts. It moves last_synced_commit
// only: status and error_detail stay as the last writer set them, and a
// state with no baseline is left to rebuild.
func TestSetSyncBaseline_MovesOnlyTheBaseline(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	defer s.CleanupTestData(ctx, t)

	var setter interface {
		SetSyncBaseline(ctx context.Context, commit string) error
	} = s

	// No state row: nothing is created.
	if err := setter.SetSyncBaseline(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSyncState(ctx); err != nil || got != nil {
		t.Fatalf("no state row: SetSyncBaseline must not create one: %+v, %v", got, err)
	}

	for _, tc := range []struct {
		name string
		from store.SyncState
		want store.SyncState
	}{
		{"idle", store.SyncState{LastSyncedCommit: "c2", Status: "idle"},
			store.SyncState{LastSyncedCommit: "c1", Status: "idle"}},
		{"rebuilding", store.SyncState{LastSyncedCommit: "c2", Status: "rebuilding"},
			store.SyncState{LastSyncedCommit: "c1", Status: "rebuilding"}},
		{"error", store.SyncState{LastSyncedCommit: "c2", Status: "error", ErrorDetail: "1 projection errors during rebuild"},
			store.SyncState{LastSyncedCommit: "c1", Status: "error", ErrorDetail: "1 projection errors during rebuild"}},
		{"syncing", store.SyncState{LastSyncedCommit: "c2", Status: "syncing"},
			store.SyncState{LastSyncedCommit: "c1", Status: "syncing"}},
		{"no_baseline", store.SyncState{Status: "error", ErrorDetail: "delete projections: x"},
			store.SyncState{Status: "error", ErrorDetail: "delete projections: x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.UpdateSyncState(ctx, &tc.from); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetSyncState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := setter.SetSyncBaseline(ctx, "c1"); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetSyncState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got.LastSyncedCommit != tc.want.LastSyncedCommit || got.Status != tc.want.Status || got.ErrorDetail != tc.want.ErrorDetail {
				t.Fatalf("after SetSyncBaseline: %+v, want %+v", got, tc.want)
			}
			if tc.name == "no_baseline" && !got.LastSyncedAt.Equal(*before.LastSyncedAt) {
				t.Fatalf("a state with no baseline is not written: last_synced_at %v -> %v", before.LastSyncedAt, got.LastSyncedAt)
			}
		})
	}
}
