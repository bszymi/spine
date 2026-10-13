package projection

// INIT-023 TASK-003 AC 1: the design-phase PoCs for adapters/projection,
// committed with their assertions unchanged. On Spine 61994fe:
//  (1) IncrementalSync after an over-limit .md landed on the synced branch
//      read the whole blob (F4; scaled-down verify-r1-9 phase B: 57 MiB
//      TotalAlloc for an 8 MiB file), and
//  (2) a governed artifact under a non-ASCII directory was never projected,
//      because ListFiles handed discovery a C-quoted path that did not end
//      in ".md" (P3-9).
// The fixture store gained the one read the workflow-row sweep (INIT-023
// TASK-011, merged after the PoCs were written) makes.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
)

type t3Store struct {
	store.Store
	mu        sync.Mutex
	state     *store.SyncState
	artifacts map[string]*store.ArtifactProjection
}

func newT3Store() *t3Store { return &t3Store{artifacts: map[string]*store.ArtifactProjection{}} }

func (s *t3Store) GetSyncState(context.Context) (*store.SyncState, error) { return s.state, nil }
func (s *t3Store) UpdateSyncState(_ context.Context, st *store.SyncState) error {
	cp := *st
	s.state = &cp
	return nil
}
func (s *t3Store) DeleteAllProjections(context.Context) error {
	s.artifacts = map[string]*store.ArtifactProjection{}
	return nil
}
func (s *t3Store) UpsertArtifactProjection(_ context.Context, p *store.ArtifactProjection) error {
	s.artifacts[p.ArtifactPath] = p
	return nil
}
func (s *t3Store) UpsertArtifactLinks(context.Context, string, []store.ArtifactLink, string) error {
	return nil
}
func (s *t3Store) UpsertExecutionProjection(context.Context, *store.ExecutionProjection) error {
	return nil
}
func (s *t3Store) UpsertWorkflowProjection(context.Context, *store.WorkflowProjection) error {
	return nil
}
func (s *t3Store) DeleteWorkflowProjection(context.Context, string) error { return nil }
func (s *t3Store) UpsertBranchProtectionRules(context.Context, []store.BranchProtectionRuleProjection, string) error {
	return nil
}
func (s *t3Store) DeleteArtifactProjection(_ context.Context, p string) error {
	delete(s.artifacts, p)
	return nil
}
func (s *t3Store) DeleteArtifactLinks(context.Context, string) error { return nil }

// ListActiveWorkflowProjections is the TASK-011 sweep's listing; the
// fixture holds no workflow rows.
func (s *t3Store) ListActiveWorkflowProjections(context.Context) ([]store.WorkflowProjection, error) {
	return nil, nil
}

func t3pgit(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func t3pwrite(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const t3Seed = "---\nid: INIT-001\ntype: Initiative\ntitle: Seed\nstatus: Pending\ncreated: 2026-10-06\n---\n\n# seed\n"

func TestT003_IncrementalSyncDoesNotBufferOversizedFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t3pgit(t, dir, nil, "init", "-q", "-b", "main")
	t3pwrite(t, dir, "initiatives/INIT-001/initiative.md", t3Seed)
	t3pgit(t, dir, nil, "add", "-A")
	t3pgit(t, dir, nil, "commit", "-qm", "seed")

	st := newT3Store()
	svc := NewService(git.NewCLIClient(dir), st, nil, 0)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}

	// 8 MiB non-artifact markdown (verify-r1-9's docs/notes.md, scaled).
	body := append([]byte("# Notes\n\n"), bytes.Repeat([]byte("ordinary meeting notes\n"), (8<<20)/23+1)...)
	blob := t3pgit(t, dir, body, "hash-object", "-w", "--stdin")
	t3pgit(t, dir, nil, "read-tree", "main")
	t3pgit(t, dir, nil, "update-index", "--add", "--cacheinfo", "100644,"+blob+",docs/notes.md")
	tree := t3pgit(t, dir, nil, "write-tree")
	commit := t3pgit(t, dir, nil, "commit-tree", tree, "-p", "main", "-m", "notes")
	t3pgit(t, dir, nil, "update-ref", "refs/heads/main", commit)
	t3pgit(t, dir, nil, "reset", "-q", "--hard", "main")

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("IncrementalSync: TotalAlloc=%d KiB status=%s commit=%s", alloc>>10, st.state.Status, st.state.LastSyncedCommit[:8])
	if alloc > 2<<20 {
		t.Errorf("IncrementalSync allocated %d KiB for an 8 MiB non-artifact file; want < 2 MiB", alloc>>10)
	}
	if st.state.Status != "idle" || st.state.LastSyncedCommit != commit {
		t.Errorf("sync state = %s@%s, want idle@%s (skip and advance)", st.state.Status, st.state.LastSyncedCommit, commit)
	}
}

func TestT003_FullRebuildProjectsNonASCIIArtifactPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t3pgit(t, dir, nil, "init", "-q", "-b", "main")
	t3pwrite(t, dir, "initiatives/INIT-001/initiative.md", t3Seed)
	epic := "initiatives/INIT-001/epics/EPIC-001-r\xc3\xa9sum\xc3\xa9/epic.md"
	t3pwrite(t, dir, epic, "---\nid: EPIC-001\ntype: Epic\ntitle: Resume\nstatus: Pending\ncreated: 2026-10-06\ninitiative: /initiatives/INIT-001/initiative.md\nlinks:\n  - type: parent\n    target: /initiatives/INIT-001/initiative.md\n---\n\n# EPIC-001\n")
	t3pgit(t, dir, nil, "add", "-A")
	t3pgit(t, dir, nil, "commit", "-qm", "seed")

	st := newT3Store()
	svc := NewService(git.NewCLIClient(dir), st, nil, 0)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	var got []string
	for p := range st.artifacts {
		got = append(got, p)
	}
	t.Logf("projected: %q (status=%s)", got, st.state.Status)
	if _, ok := st.artifacts[epic]; !ok {
		t.Errorf("artifact %q was not projected (P3-9: quoted path dropped by discovery)", epic)
	}
}
