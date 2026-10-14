package projection

// INIT-023 wave 3 integration (codex review of the merged branch): rows a
// projection built before TASK-003 can hold at a path TASK-003 now
// refuses. Before TASK-003 git listed paths for discovery in its quoted
// form, which leaves every byte >= 0x80 raw under core.quotePath=false,
// so a bidi control, U+2028/U+2029 or a C1 control in a file name could
// be projected. TASK-003 then refused such paths unread and assumed no
// row could exist: a deletion, rename or modification skipped the row,
// and the workflow sweep re-read the file and kept the row Active. The
// row now goes with its file, and a workflow row is quarantined (or
// removed) instead of staying bindable.

import (
	"context"
	"fmt"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

func TestProjection_StoredUnsafeRowsAreRetired(t *testing.T) {
	ctx := context.Background()
	const (
		delArtifact  = "governance/d\u202eel.md"
		modArtifact  = "governance/m\u2028od.md"
		renArtifact  = "governance/r\u202een.md"
		delWorkflow  = "workflows/d\u202eel.yaml"
		modWorkflow  = "workflows/m\u202eod.yaml"
		keepWorkflow = "workflows/k\u2066eep.yaml" // not in the diff: the sweep judges it
	)

	repo := newScratchRepo(t)
	c1 := commitRaw(t, repo, "", map[string]string{
		"governance/g.md": refusedGovDoc,
		delArtifact:       refusedGovDoc,
		modArtifact:       refusedGovDoc,
		renArtifact:       refusedGovDoc + "\nrenamed away\n",
		delWorkflow:       reviewedWorkflow,
		modWorkflow:       reviewedWorkflow,
		keepWorkflow:      reviewedWorkflow,
	})
	st := &utf8Store{mapStore: newMapStore(), t: t}
	ev := &recordingRouter{}
	svc := NewService(git.NewCLIClient(repo.dir), st, ev, 0)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}

	// The rows a pre-TASK-003 projection under core.quotePath=false left
	// behind (TASK-003's FullRebuild projects none of them).
	for _, p := range []string{delArtifact, modArtifact, renArtifact} {
		st.artifacts[p] = &store.ArtifactProjection{ArtifactPath: p, ArtifactID: "G", ArtifactType: "Governance",
			Title: "G", Status: "Living Document", SourceCommit: c1}
		st.links[p] = []store.ArtifactLink{{SourcePath: p, TargetPath: "/governance/g.md", LinkType: "related_to"}}
	}
	for _, p := range []string{delWorkflow, modWorkflow, keepWorkflow} {
		row := acceptedRow(t, p, reviewedWorkflow, c1)
		st.workflows[p] = &row
	}

	c2 := commitRaw(t, repo, c1, map[string]string{
		modArtifact:       refusedGovDoc + "\nedited\n",
		modWorkflow:       reviewedWorkflow + "# edited\n",
		"governance/r.md": refusedGovDoc + "\nrenamed away\n",
	})
	head := commitRawDelete(t, repo, c2, delArtifact, delWorkflow, renArtifact)

	ev.reset()
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != head {
		t.Fatalf("sync state = %+v, want idle@%s", got, head)
	}
	for _, p := range []string{delArtifact, modArtifact, renArtifact} {
		if st.artifacts[p] != nil || len(st.links[p]) != 0 {
			t.Errorf("stale artifact row or links at %q survived the sync", p)
		}
	}
	if st.artifacts["governance/g.md"] == nil {
		t.Error("an unrelated artifact was removed")
	}
	if st.workflows[delWorkflow] != nil {
		t.Errorf("stale workflow row at %q survived its file's deletion", delWorkflow)
	}
	if w := st.workflows[modWorkflow]; w == nil || w.Status != string(domain.WorkflowStatusQuarantined) || w.SourceCommit != c1 {
		t.Errorf("modified unsafe workflow row = %s, want it kept at %s and quarantined", rowState(w), c1)
	}
	assertUnsafeRejection(t, ev, modWorkflow, actionKept)

	// A new instance's first sync runs the workflow sweep: the row at an
	// unsafe path that no diff touched is quarantined too, not re-read and
	// kept Active.
	if w := st.workflows[keepWorkflow]; w == nil || w.Status != string(domain.WorkflowStatusActive) {
		t.Fatalf("precondition: untouched row = %s, want Active", rowState(w))
	}
	ev.reset()
	svc2 := NewService(git.NewCLIClient(repo.dir), st, ev, 0)
	if err := svc2.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	if w := st.workflows[keepWorkflow]; w == nil || w.Status != string(domain.WorkflowStatusQuarantined) {
		t.Errorf("sweep: row at an unsafe path = %s, want it quarantined", rowState(w))
	}
	assertUnsafeRejection(t, ev, keepWorkflow, actionKept)
}

func rowState(w *store.WorkflowProjection) string {
	if w == nil {
		return "<no row>"
	}
	return fmt.Sprintf("{status %s, source_commit %s}", w.Status, w.SourceCommit)
}

func assertUnsafeRejection(t *testing.T, ev *recordingRouter, path, action string) {
	t.Helper()
	for _, r := range ev.rejections(t) {
		if r["workflow_path"] == path {
			if r["check"] != checkUnsafePath || r["action"] != action {
				t.Errorf("rejection for %q = %v, want check %s, action %s", path, r, checkUnsafePath, action)
			}
			return
		}
	}
	t.Errorf("no projection_workflow_rejected event for %q (events: %s)", path, fmt.Sprint(ev.rejections(t)))
}
