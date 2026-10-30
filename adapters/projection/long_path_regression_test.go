package projection

// INIT-024 TASK-004 review round 1 (P3-1): a path longer than 4,096 bytes
// is no refusal of a listing or a diff. At d1f5e6e1 a kept path over that
// length refused the whole listing and every diff path was held to it, so
// a governed .md file at such a path failed FullRebuild, and such a path
// anywhere in a sync window failed every IncrementalSync — a persistent
// projection wedge on an embedder whose own merges let the path in. What a
// listing keeps stays bounded in total.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

// longPath is a repository path of exactly n bytes ending in name, under
// 255-byte directory names.
func longPath(n int, name string) string {
	var comps []string
	rest := n - len(name)
	for rest > 0 {
		k := min(255, rest-1)
		if left := rest - 1 - k; left > 0 && left < 2 {
			k--
		}
		comps = append(comps, strings.Repeat("d", k))
		rest -= k + 1
	}
	return strings.Join(append(comps, name), "/")
}

// stageLong adds body at path to the index without a working-tree file (a
// path this long cannot be written to every filesystem).
func stageLong(r *scratchRepo, path, body string) {
	r.t.Helper()
	r.write(".long-body", body)
	blob := r.run("hash-object", "-w", "--", ".long-body")
	if err := os.Remove(filepath.Join(r.dir, ".long-body")); err != nil {
		r.t.Fatal(err)
	}
	r.run("update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
}

// commitIndex commits the index as it stands on top of main.
func commitIndex(r *scratchRepo, msg string) string {
	r.t.Helper()
	tree := r.run("write-tree")
	c := r.run("commit-tree", tree, "-p", r.run("rev-parse", "refs/heads/main"), "-m", msg)
	r.run("update-ref", "refs/heads/main", c)
	return c
}

func TestFullRebuild_LongGovernedPathProjects(t *testing.T) {
	ctx := context.Background()
	r := newScratchRepo(t)
	r.write(wedgeTaskA, wedgeInitiative("INIT-001", "First"))
	r.commit("one artifact")
	deep := longPath(5000, "initiative.md")
	stageLong(r, deep, wedgeInitiative("INIT-002", "Deep"))
	head := commitIndex(r, "a governed file at a 5,000-byte path")

	st := newMapStore()
	if err := NewService(git.NewCLIClient(r.dir), st, nil, 0).FullRebuild(ctx); err != nil {
		t.Fatalf("FullRebuild with a 5,000-byte governed path: %v", err)
	}
	if st.state == nil || st.state.Status != "idle" || st.state.LastSyncedCommit != head {
		t.Fatalf("sync state = %+v, want idle at %s", st.state, head)
	}
	if st.artifacts[wedgeTaskA] == nil || st.artifacts[deep] == nil {
		t.Fatalf("projected %d artifacts, want both (the deep one included)", len(st.artifacts))
	}
}

func TestIncrementalSync_LongPathInTheWindowSyncs(t *testing.T) {
	ctx := context.Background()
	r := newScratchRepo(t)
	r.write(wedgeTaskA, wedgeInitiative("INIT-001", "First"))
	r.commit("one artifact")
	st := newMapStore()
	svc := NewService(git.NewCLIClient(r.dir), st, nil, 0)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatalf("baseline rebuild: %v", err)
	}
	// The window adds a non-governed file at a 5,000-byte path and changes
	// the artifact.
	stageLong(r, longPath(5000, "notes.txt"), "notes\n")
	r.write(wedgeTaskA, wedgeInitiative("INIT-001", "First, revised"))
	r.run("add", "--", wedgeTaskA)
	head := commitIndex(r, "a long path in the window")

	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatalf("IncrementalSync of a window with a 5,000-byte path: %v", err)
	}
	if st.state == nil || st.state.Status != "idle" || st.state.LastSyncedCommit != head {
		t.Fatalf("sync state = %+v, want idle at %s", st.state, head)
	}
	if a := st.artifacts[wedgeTaskA]; a == nil || a.Title != "First, revised" {
		t.Fatalf("the artifact change in the window was not applied: %+v", a)
	}
}
