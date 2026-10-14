//go:build integration

package projection_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/projection"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/testutil"
)

// INIT-023 codex 6.1 catch-up against real PostgreSQL: jsonb keeps an
// integer above 2^53 exactly, so the definition the store hands back must
// verify, and the sweep must judge it unchanged. A fresh Service (a
// restart) runs the first-sync sweep against the row as jsonb returns it;
// the rejected edit after it must keep that row quarantined.
//
// Mutation: VerifyProjectionDefinition decoding through float64 (main
// before the catch-up) fails "the stored row must verify". sameJSON
// decoding through float64 does not fail here (both sides round alike);
// the unit test TestSweep_ReplacesARowThatDiffersOnlyInALargeInteger pins
// it.
func TestIntegration_LargeIntegerRowSurvivesSweepAndRejection(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	db := store.NewTestStore(t)
	defer db.CleanupTestData(ctx, t)

	const path = "workflows/task-default.yaml"
	body := strings.Replace(pgValidWorkflow, "    type: manual\n",
		"    type: manual\n    retry: {limit: 9007199254740993, backoff: fixed}\n", 1)
	testutil.WriteFile(t, repo, path, body)
	testutil.GitAdd(t, repo, ".", "c1")
	c1 := headOf(t, client)
	if err := projection.NewService(client, db, &pgRecorder{}, time.Second).FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetWorkflowProjection(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(row.Definition), "9007199254740993") {
		t.Fatalf("jsonb must keep the integer exactly: %s", row.Definition)
	}
	if err := workflow.VerifyProjectionDefinition(row.Definition); err != nil {
		t.Fatalf("the stored row must verify: %v", err)
	}

	// A restart: the first sync sweeps the row as jsonb returns it.
	testutil.WriteFile(t, repo, "README.md", "unrelated change\n")
	testutil.GitAdd(t, repo, ".", "c2: unrelated")
	rec := &pgRecorder{}
	restarted := projection.NewService(client, db, rec, time.Second)
	if err := restarted.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	if row, err = db.GetWorkflowProjection(ctx, path); err != nil || row.SourceCommit != c1 || row.Status != "Active" {
		t.Fatalf("the sweep leaves an unchanged row untouched: %+v, %v", row, err)
	}

	if rej := rec.rejections(t); len(rej) != 0 {
		t.Fatalf("the sweep rejects nothing: %+v", rej)
	}

	// A rejected edit, on the same instance (its sweep is done), keeps the
	// verified row, quarantined.
	testutil.WriteFile(t, repo, path, strings.Replace(body, "steps:", "ſteps:", 1))
	testutil.GitAdd(t, repo, ".", "c3: a rejected edit")
	if err := restarted.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	if row, err = db.GetWorkflowProjection(ctx, path); err != nil || row.SourceCommit != c1 || row.Status != "Quarantined" {
		t.Fatalf("the c1 row must be kept, quarantined: %+v, %v", row, err)
	}
	if rej := rec.rejections(t); len(rej) != 1 || rej[0]["action"] != "kept" {
		t.Fatalf("one rejection, action kept: %+v", rej)
	}
}
