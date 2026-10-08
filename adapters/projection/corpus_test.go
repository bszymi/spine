package projection

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// INIT-023 TASK-011 AC 21: Spine's shipped workflow corpus goes through
// the projection unchanged — 8 rows, zero rejections, idle, and stored
// JSON byte-identical to the historical encoder.
func TestFullRebuild_ShippedWorkflowCorpus(t *testing.T) {
	logs := captureLogs(t)
	files, err := filepath.Glob(filepath.Join("..", "..", "workflows", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 8 {
		t.Fatalf("expected 8 shipped workflow files, found %d", len(files))
	}
	repo := newScratchRepo(t)
	bodies := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		p := "workflows/" + filepath.Base(f)
		bodies[p] = string(b)
		repo.write(p, string(b))
	}
	head := repo.commit("corpus")

	st := allRowsStore{newMapStore()}
	router := &recordingRouter{}
	svc := NewService(repo.git, st, router, 0)
	if err := svc.FullRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != head {
		t.Fatalf("sync state: %+v", got)
	}
	if len(st.workflows) != 8 {
		t.Fatalf("projected %d workflow rows, want 8", len(st.workflows))
	}
	if rej := router.rejections(t); len(rej) != 0 {
		t.Fatalf("shipped workflows must not be rejected: %+v", rej)
	}
	if n := len(logs.lines("workflow file rejected")); n != 0 {
		t.Fatalf("unexpected rejection logs:\n%s", logs.String())
	}
	for p, body := range bodies {
		legacy := legacyRow(t, p, body, head)
		got := st.row(p)
		if !bytes.Equal(got.Definition, legacy.Definition) {
			t.Errorf("%s: stored definition changed", p)
		}
		if got.WorkflowID != legacy.WorkflowID || got.Name != legacy.Name || got.Version != legacy.Version ||
			got.Status != legacy.Status || !bytes.Equal(got.AppliesTo, legacy.AppliesTo) {
			t.Errorf("%s: typed columns changed: got %+v want %+v", p, got, legacy)
		}
	}
	// A second instance's sweep leaves every row untouched.
	svc2 := NewService(repo.git, st, router, 0)
	before := map[string]int{}
	for p := range bodies {
		before[p] = st.upserts(p)
	}
	if err := svc2.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	for p := range bodies {
		if st.upserts(p) != before[p] {
			t.Errorf("%s: the sweep re-upserted an unchanged shipped row", p)
		}
	}
}
