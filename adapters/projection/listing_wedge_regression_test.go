package projection

// INIT-024 TASK-004 AC 1 (review finding N-6): a tree whose path listing
// is larger than the git client's output limit, but which holds only a
// few governed files, projects normally. Ported from the review's
// scaled-down reproduction: the client's limit is the 4 KiB floor and the
// tree lists several times that. At the base, ListFiles buffered the whole
// listing under the limit, so FullRebuild failed in discovery, and an
// IncrementalSync whose own diff was small failed in the workflow-row
// sweep and the branch-protection lookup, both of which list the tree.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

const (
	wedgeCap    = 4 << 10
	wedgeTaskA  = "initiatives/INIT-001/initiative.md"
	wedgeTaskB  = "initiatives/INIT-002/initiative.md"
	wedgeWFPath = "workflows/task-default.yaml"
)

func wedgeInitiative(id, title string) string {
	return "---\nid: " + id + "\ntype: Initiative\ntitle: " + title + "\nstatus: Pending\ncreated: 2026-10-27\n---\n\n# " + title + "\n"
}

// wedgeBulk adds n empty files with long names: one blob, a tiny pack,
// a listing of about 45 bytes per file.
func wedgeBulk(r *scratchRepo, dir string, n int) {
	for i := 0; i < n; i++ {
		r.write(fmt.Sprintf("%s/d%02d/file-with-a-long-name-%04d.txt", dir, i%10, i), "")
	}
}

func wedgeListingBytes(t *testing.T, r *scratchRepo, rev string) int {
	t.Helper()
	return len(r.run("ls-tree", "-r", "--name-only", "-z", rev))
}

func TestFullRebuild_LargeListingFewArtifactsProjects(t *testing.T) {
	ctx := context.Background()

	t.Run("FullRebuild", func(t *testing.T) {
		r := newScratchRepo(t)
		wedgeBulk(r, "bulk", 300)
		r.write(wedgeWFPath, reviewedWorkflow)
		r.write(wedgeTaskA, wedgeInitiative("INIT-001", "First"))
		r.write(wedgeTaskB, wedgeInitiative("INIT-002", "Second"))
		head := r.commit("bulk and two artifacts")
		if n := wedgeListingBytes(t, r, head); n < 2*wedgeCap {
			t.Fatalf("fixture: the listing is %d bytes, want well over the %d-byte cap", n, wedgeCap)
		}

		st := newMapStore()
		svc := NewService(git.NewCLIClient(r.dir, git.WithMaxOutputBytes(wedgeCap)), st, nil, 0)
		if err := svc.FullRebuild(ctx); err != nil {
			t.Fatalf("FullRebuild over a listing larger than the cap: %v", err)
		}
		wedgeAssertProjected(t, st, head)
	})

	t.Run("IncrementalSync", func(t *testing.T) {
		r := newScratchRepo(t)
		wedgeBulk(r, "bulk", 300)
		r.write(wedgeWFPath, reviewedWorkflow)
		base := r.commit("bulk and a workflow")
		if n := wedgeListingBytes(t, r, base); n < 2*wedgeCap {
			t.Fatalf("fixture: the listing is %d bytes, want well over the %d-byte cap", n, wedgeCap)
		}
		// The baseline is synced by a client with the default limit (setup
		// only): the store holds the workflow row the sweep reconciles.
		st := newMapStore()
		if err := NewService(git.NewCLIClient(r.dir), st, nil, 0).FullRebuild(ctx); err != nil {
			t.Fatalf("baseline rebuild: %v", err)
		}
		if st.workflows[wedgeWFPath] == nil {
			t.Fatal("fixture: the baseline holds no workflow row, so the sweep would list nothing")
		}

		// A small change: the sync's own diff is two files.
		r.write(wedgeTaskA, wedgeInitiative("INIT-001", "First"))
		r.write(wedgeTaskB, wedgeInitiative("INIT-002", "Second"))
		head := r.commit("two artifacts")

		// A fresh instance with the 4 KiB cap: its first sync sweeps the
		// stored workflow rows (a listing at head) and projects the
		// branch-protection config (a listing with a pattern).
		svc := NewService(git.NewCLIClient(r.dir, git.WithMaxOutputBytes(wedgeCap)), st, nil, 0)
		if err := svc.IncrementalSync(ctx); err != nil {
			t.Fatalf("IncrementalSync over a listing larger than the cap: %v", err)
		}
		wedgeAssertProjected(t, st, head)
	})

	// The bound that remains: a sync WINDOW whose changed paths alone are
	// over the cap still fails closed — Diff keeps every path it returns —
	// and leaves the state where it was; an operator rebuild, which now
	// lists only the governed paths, recovers it, and the next small window
	// syncs. This pins the recovery the residual relies on.
	t.Run("a diff window over the cap fails closed; a rebuild recovers", func(t *testing.T) {
		r := newScratchRepo(t)
		r.write(wedgeTaskA, wedgeInitiative("INIT-001", "First"))
		base := r.commit("one artifact")
		st := newMapStore()
		svc := NewService(git.NewCLIClient(r.dir, git.WithMaxOutputBytes(wedgeCap)), st, nil, 0)
		if err := svc.FullRebuild(ctx); err != nil {
			t.Fatalf("baseline rebuild: %v", err)
		}
		wedgeBulk(r, "bulk", 300)
		r.write(wedgeTaskB, wedgeInitiative("INIT-002", "Second"))
		head := r.commit("bulk in the window")

		err := svc.IncrementalSync(ctx)
		if !errors.Is(err, git.ErrOutputTooLarge) {
			t.Fatalf("IncrementalSync of a window whose diff is over the cap: %v, want ErrOutputTooLarge", err)
		}
		if st.state == nil || st.state.Status != "error" || st.state.LastSyncedCommit != base {
			t.Fatalf("state after the refused window = %+v, want error at the previous commit", st.state)
		}
		if err := svc.FullRebuild(ctx); err != nil {
			t.Fatalf("operator rebuild: %v", err)
		}
		if st.state.Status != "idle" || st.state.LastSyncedCommit != head {
			t.Fatalf("state after the rebuild = %+v, want idle at %s", st.state, head)
		}
		r.write(wedgeTaskB, wedgeInitiative("INIT-002", "Second, revised"))
		next := r.commit("a small change")
		if err := svc.IncrementalSync(ctx); err != nil {
			t.Fatalf("IncrementalSync after the rebuild: %v", err)
		}
		if st.state.Status != "idle" || st.state.LastSyncedCommit != next {
			t.Fatalf("state after the next window = %+v, want idle at %s", st.state, next)
		}
	})
}

func wedgeAssertProjected(t *testing.T, st *mapStore, head string) {
	t.Helper()
	if st.state == nil || st.state.Status != "idle" || st.state.LastSyncedCommit != head {
		t.Fatalf("sync state = %+v, want idle at %s", st.state, head)
	}
	for _, p := range []string{wedgeTaskA, wedgeTaskB} {
		if st.artifacts[p] == nil {
			t.Errorf("artifact %s not projected (projected: %d)", p, len(st.artifacts))
		}
	}
	if st.workflows[wedgeWFPath] == nil {
		t.Errorf("workflow %s not projected", wedgeWFPath)
	}
}
