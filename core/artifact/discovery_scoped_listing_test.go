package artifact_test

// INIT-024 TASK-004 review round 1 (P4-6): discovery and ID allocation
// name the directory they scan, so git walks only that subtree — a tree
// that expands to millions of paths elsewhere costs them nothing. The
// result is the one an unscoped listing gives.

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
)

// scopeRecordingGit records the directory of every scoped listing.
type scopeRecordingGit struct {
	*git.CLIClient
	mu   sync.Mutex
	dirs []string
}

func (g *scopeRecordingGit) ListFilesUnder(ctx context.Context, ref, dir string, keep func(string) bool) ([]string, error) {
	g.mu.Lock()
	g.dirs = append(g.dirs, dir)
	g.mu.Unlock()
	return g.CLIClient.ListFilesUnder(ctx, ref, dir, keep)
}

func TestDiscovery_ScopesTheListingToItsDirectory(t *testing.T) {
	ctx := context.Background()
	files := map[string]string{
		"spine/initiatives/INIT-001/initiative.md":    "---\nid: INIT-001\ntype: Initiative\ntitle: In scope\nstatus: Pending\ncreated: 2026-10-28\n---\n",
		"spine/workflows/task.yaml":                   "id: wf\n",
		"workflows/root.yaml":                         "id: root\n",
		"initiatives/INIT-009/initiative.md":          "---\nid: INIT-009\ntype: Initiative\ntitle: Out of scope\nstatus: Pending\ncreated: 2026-10-28\n---\n",
		"initiatives/INIT-009/epics/EPIC-004/epic.md": "x\n",
		"initiatives/INIT-009/epics/EPIC-007/epic.md": "x\n",
	}
	dir, commit := rawTreeRepo(t, files)
	g := &scopeRecordingGit{CLIClient: git.NewCLIClient(dir)}

	res, err := artifact.DiscoverAll(ctx, g, commit, "spine")
	if err != nil || len(res.Artifacts) != 1 || res.Artifacts[0].ID != "INIT-001" || !reflect.DeepEqual(res.Workflows, []string{"spine/workflows/task.yaml"}) {
		t.Fatalf("DiscoverAll(spine) = %+v, %v", res, err)
	}
	wf, err := artifact.DiscoverWorkflows(ctx, g, commit, "spine")
	if err != nil || !reflect.DeepEqual(wf, []string{"spine/workflows/task.yaml"}) {
		t.Fatalf("DiscoverWorkflows(spine) = %q, %v", wf, err)
	}
	root, err := artifact.DiscoverWorkflows(ctx, g, commit)
	if err != nil || !reflect.DeepEqual(root, []string{"workflows/root.yaml"}) {
		t.Fatalf("DiscoverWorkflows() = %q, %v", root, err)
	}
	id, err := artifact.NextID(ctx, g, "initiatives/INIT-009/epics", domain.ArtifactTypeEpic, commit)
	if err != nil || id != "EPIC-008" {
		t.Fatalf("NextID = %q, %v; want EPIC-008", id, err)
	}
	got := append([]string(nil), g.dirs...)
	sort.Strings(got)
	want := []string{"initiatives/INIT-009/epics/", "spine", "spine/workflows", "workflows"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scoped listings = %q, want %q", got, want)
	}
	// The repository root is not a directory scope: the whole tree is listed.
	g.dirs = nil
	if _, err := artifact.DiscoverAll(ctx, g, commit); err != nil || len(g.dirs) != 0 {
		t.Fatalf("DiscoverAll() scoped its listing: %q, %v", g.dirs, err)
	}
}
