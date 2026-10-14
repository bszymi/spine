package projection

import (
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/workflow"
)

// INIT-023 codex 6.1 catch-up: a workflow carrying an integer above 2^53
// (exact in the stored JSONB, not in a float64) is stored by
// ProjectionDefinition, so every path that judges the stored row must read
// that integer exactly too.

const largeLimit = 9007199254740993 // 2^53+1

// largeLimitWorkflow is reviewedWorkflow with a retry limit of limit on
// its first step.
func largeLimitWorkflow(limit string) string {
	return strings.Replace(reviewedWorkflow,
		"    type: manual\n",
		"    type: manual\n    retry: {limit: "+limit+", backoff: fixed}\n", 1)
}

func storedLimit(t *testing.T, st allRowsStore) int {
	t.Helper()
	r := st.row(wfPath)
	if r == nil {
		t.Fatal("no row")
	}
	wf, err := workflow.UnmarshalProjectionDefinition(r.Definition)
	if err != nil {
		t.Fatal(err)
	}
	return wf.Steps[0].Retry.Limit
}

// A rejected edit keeps the previous row quarantined only when the row
// verifies. The row ProjectionDefinition stored for a large integer used
// to fail VerifyProjectionDefinition, so the rejection deleted it.
//
// Mutation check: VerifyProjectionDefinition decoding the stored JSON with
// json.Unmarshal into `any` fails "the verified row must be kept".
func TestIncrementalSync_RejectedEditKeepsARowWithALargeInteger(t *testing.T) {
	repo, st, router, svc, c1 := newIntegrityFixture(t, largeLimitWorkflow("9007199254740993"))
	if got := storedLimit(t, st); got != largeLimit {
		t.Fatalf("precondition: the stored row holds the exact limit, got %d", got)
	}
	repo.write(wfPath, pocFoldWorkflow)
	c2 := repo.commit("c2: a rejected edit")
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertKeptQuarantined(t, st, c1)
	assertOneRejection(t, router, "parse", "kept", c1, true)
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("a rejection advances the sync: %+v", got)
	}
}

// The sweep replaces a stored row that differs from head's. Comparing the
// definitions as JSON decoded into `any` rounded both integers to the same
// float64, so a row differing only in an integer above 2^53 counted as
// unchanged and was never repaired.
//
// Mutation check: sameJSON decoding with json.Unmarshal into `any` fails
// "the sweep replaces the drifted row".
func TestSweep_ReplacesARowThatDiffersOnlyInALargeInteger(t *testing.T) {
	repo := newScratchRepo(t)
	repo.write(wfPath, largeLimitWorkflow("9007199254740992"))
	repo.commit("c0")
	st := allRowsStore{newMapStore()}
	if err := NewService(repo.git, st, nil, 0).FullRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	// c1 changes the limit by one; the store is marked synced at c1
	// without that change applied (a row left behind, as a dropped Diff
	// did before TASK-011).
	repo.write(wfPath, largeLimitWorkflow("9007199254740993"))
	c1 := repo.commit("c1")
	st.setState(c1, "idle")

	if err := NewService(repo.git, st, nil, 0).IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := storedLimit(t, st); got != largeLimit {
		t.Fatalf("the sweep replaces the drifted row: limit %d, want %d", got, largeLimit)
	}
	if r := st.row(wfPath); r.SourceCommit != c1 || r.Status != "Active" {
		t.Fatalf("replaced row: %+v", r)
	}
}

// An unchanged row with a large integer stays untouched (no re-stamp of
// source_commit, which would strand runs pinned to it).
func TestSweep_KeepsAnUnchangedRowWithALargeInteger(t *testing.T) {
	repo := newScratchRepo(t)
	repo.write(wfPath, largeLimitWorkflow("9007199254740993"))
	c0 := repo.commit("c0")
	st := allRowsStore{newMapStore()}
	if err := NewService(repo.git, st, nil, 0).FullRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	c1 := repo.commit("c1: unrelated")
	st.setState(c1, "idle")
	if err := NewService(repo.git, st, nil, 0).IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := st.row(wfPath); r == nil || r.SourceCommit != c0 {
		t.Fatalf("an unchanged row keeps its source_commit: %+v", r)
	}
}
