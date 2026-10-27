package projection

import (
	"context"
	"strings"
	"testing"
)

// INIT-024 TASK-002 AC 5 (review finding N-7) at the projection: a
// workflow file whose definition is followed by a second YAML document
// was projected from its first document, the second silently ignored. It
// is now refused by strict Parse, so the INIT-023 TASK-011 rules apply:
// the last verified row is kept, quarantined, and the sync advances.

// trailingDocumentWorkflow is reviewedWorkflow with a version bump (so an
// install would be visible) and a second, review-less document after it.
var trailingDocumentWorkflow = strings.Replace(reviewedWorkflow, `version: "1.0"`, `version: "1.1"`, 1) +
	"---\n" + reviewlessWorkflow

func TestIncrementalSync_TrailingDocumentWorkflowQuarantined(t *testing.T) {
	repo, st, router, svc, c1 := newIntegrityFixture(t, reviewedWorkflow)

	repo.write(wfPath, trailingDocumentWorkflow)
	c2 := repo.commit("c2")
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatalf("IncrementalSync: %v", err)
	}

	row := assertKeptQuarantined(t, st, c1)
	wf := decodeDef(t, row.Definition)
	if wf.Version != "1.0" || !hasStep(wf, "review") {
		t.Fatalf("kept definition = version %q steps %v, want the c1 definition", wf.Version, stepIDs(wf))
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("a rejection must advance the sync: %+v", got)
	}
	assertOneRejection(t, router, "parse", "kept", c1, true)
}

// A bare trailing `---` carries no content and is still installed.
func TestIncrementalSync_EmptyTrailingDocumentWorkflowInstalled(t *testing.T) {
	repo, st, router, svc, _ := newIntegrityFixture(t, reviewedWorkflow)

	repo.write(wfPath, strings.Replace(reviewedWorkflow, `version: "1.0"`, `version: "1.1"`, 1)+"---\n")
	c2 := repo.commit("c2")
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatalf("IncrementalSync: %v", err)
	}
	row := st.row(wfPath)
	if row == nil || row.SourceCommit != c2 || row.Status != "Active" {
		t.Fatalf("row = %+v, want the c2 definition installed Active", row)
	}
	if wf := decodeDef(t, row.Definition); wf.Version != "1.1" {
		t.Fatalf("installed version = %q, want 1.1", wf.Version)
	}
	if rej := router.rejections(t); len(rej) != 0 {
		t.Fatalf("unexpected rejections: %+v", rej)
	}
}
