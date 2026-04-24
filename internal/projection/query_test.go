package projection

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/store"
)

// queryFakeStore embeds store.Store so any method the tests don't
// explicitly override panics loudly — preventing accidental coupling
// to the real store.
type queryFakeStore struct {
	store.Store
	artifactsResult *store.ArtifactQueryResult
	artifactsErr    error
	projections     map[string]*store.ArtifactProjection
	projectionErr   error
	links           map[string][]store.ArtifactLink
	linksErr        error
	runs            []domain.Run
	runsErr         error
	syncState       *store.SyncState
	syncStateErr    error
}

func (s *queryFakeStore) QueryArtifacts(_ context.Context, _ store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	return s.artifactsResult, s.artifactsErr
}

func (s *queryFakeStore) GetArtifactProjection(_ context.Context, path string) (*store.ArtifactProjection, error) {
	if s.projectionErr != nil {
		return nil, s.projectionErr
	}
	if proj, ok := s.projections[path]; ok {
		return proj, nil
	}
	return nil, domain.NewError(domain.ErrNotFound, "not found: "+path)
}

func (s *queryFakeStore) QueryArtifactLinks(_ context.Context, path string) ([]store.ArtifactLink, error) {
	if s.linksErr != nil {
		return nil, s.linksErr
	}
	return s.links[path], nil
}

func (s *queryFakeStore) ListRunsByTask(_ context.Context, _ string) ([]domain.Run, error) {
	return s.runs, s.runsErr
}

func (s *queryFakeStore) GetSyncState(_ context.Context) (*store.SyncState, error) {
	return s.syncState, s.syncStateErr
}

// queryFakeGit embeds git.GitClient so unused methods panic.
type queryFakeGit struct {
	git.GitClient
	head        string
	headErr     error
	logResult   []git.CommitInfo
	logErr      error
	logOptsSeen git.LogOpts
}

func (g *queryFakeGit) Head(_ context.Context) (string, error) {
	return g.head, g.headErr
}

func (g *queryFakeGit) Log(_ context.Context, opts git.LogOpts) ([]git.CommitInfo, error) {
	g.logOptsSeen = opts
	return g.logResult, g.logErr
}

func TestQueryService_QueryArtifacts_DelegatesAndPropagatesError(t *testing.T) {
	want := &store.ArtifactQueryResult{Items: []store.ArtifactProjection{{ArtifactPath: "a"}}}
	st := &queryFakeStore{artifactsResult: want}
	q := NewQueryService(st, nil)
	got, err := q.QueryArtifacts(context.Background(), store.ArtifactQuery{})
	if err != nil {
		t.Fatalf("QueryArtifacts: %v", err)
	}
	if got != want {
		t.Errorf("expected pass-through result, got different pointer")
	}

	sentinel := errors.New("boom")
	st.artifactsErr = sentinel
	if _, err := q.QueryArtifacts(context.Background(), store.ArtifactQuery{}); !errors.Is(err, sentinel) {
		t.Errorf("expected error pass-through, got %v", err)
	}
}

func TestQueryService_GetArtifact_DelegatesAndPropagatesError(t *testing.T) {
	st := &queryFakeStore{projections: map[string]*store.ArtifactProjection{
		"initiatives/INIT-001/initiative.md": {ArtifactPath: "initiatives/INIT-001/initiative.md"},
	}}
	q := NewQueryService(st, nil)
	got, err := q.GetArtifact(context.Background(), "initiatives/INIT-001/initiative.md")
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil projection")
	}

	_, err = q.GetArtifact(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestQueryService_QueryGraph_TraversesAndRespectsDepth(t *testing.T) {
	// root → child via follow_up_to → grandchild via parent
	st := &queryFakeStore{
		projections: map[string]*store.ArtifactProjection{
			"root":       {ArtifactPath: "root", ArtifactType: "Task", Title: "Root", Status: "Proposed"},
			"child":      {ArtifactPath: "child", ArtifactType: "Task", Title: "Child", Status: "Proposed"},
			"grandchild": {ArtifactPath: "grandchild", ArtifactType: "Epic", Title: "Grand", Status: "Proposed"},
		},
		links: map[string][]store.ArtifactLink{
			"root":  {{SourcePath: "root", TargetPath: "/child", LinkType: "follow_up_to"}},
			"child": {{SourcePath: "child", TargetPath: "/grandchild", LinkType: "parent"}},
		},
	}
	q := NewQueryService(st, nil)

	// depth=1 visits root + child, ignores grandchild
	res, err := q.QueryGraph(context.Background(), "root", 1, nil)
	if err != nil {
		t.Fatalf("QueryGraph: %v", err)
	}
	if len(res.Nodes) != 2 {
		t.Errorf("depth=1: expected 2 nodes (root+child), got %d", len(res.Nodes))
	}
	// TargetPath should be normalized (no leading /)
	if len(res.Edges) == 0 || res.Edges[0].Target != "child" {
		t.Errorf("expected normalized edge target 'child', got %+v", res.Edges)
	}

	// depth=3 (clamped to 5) walks the full chain
	res, err = q.QueryGraph(context.Background(), "root", 3, nil)
	if err != nil {
		t.Fatalf("QueryGraph depth=3: %v", err)
	}
	if len(res.Nodes) != 3 {
		t.Errorf("depth=3: expected 3 nodes, got %d", len(res.Nodes))
	}
}

func TestQueryService_QueryGraph_DepthClampingAndLinkTypeFilter(t *testing.T) {
	st := &queryFakeStore{
		projections: map[string]*store.ArtifactProjection{
			"root":    {ArtifactPath: "root", ArtifactType: "Task"},
			"kept":    {ArtifactPath: "kept", ArtifactType: "Task"},
			"dropped": {ArtifactPath: "dropped", ArtifactType: "Task"},
		},
		links: map[string][]store.ArtifactLink{
			"root": {
				{SourcePath: "root", TargetPath: "kept", LinkType: "parent"},
				{SourcePath: "root", TargetPath: "dropped", LinkType: "related_to"},
			},
		},
	}
	q := NewQueryService(st, nil)

	// Negative depth → clamped to default (2)
	res, err := q.QueryGraph(context.Background(), "root", -1, []string{"parent"})
	if err != nil {
		t.Fatalf("QueryGraph: %v", err)
	}
	// Only "parent" link kept; "related_to" filtered out.
	gotTargets := map[string]bool{}
	for _, e := range res.Edges {
		gotTargets[e.Target] = true
	}
	if !gotTargets["kept"] || gotTargets["dropped"] {
		t.Errorf("link_type filter did not apply: edges=%+v", res.Edges)
	}

	// depth>5 is clamped to 5 (we rely on traversal completing without
	// hitting the raw input as the loop guard).
	if _, err := q.QueryGraph(context.Background(), "root", 100, nil); err != nil {
		t.Errorf("QueryGraph with oversized depth: %v", err)
	}
}

func TestQueryService_QueryGraph_TolerantOfBrokenLinks(t *testing.T) {
	st := &queryFakeStore{
		projections: map[string]*store.ArtifactProjection{
			"root": {ArtifactPath: "root", ArtifactType: "Task"},
		},
		links: map[string][]store.ArtifactLink{
			"root": {{SourcePath: "root", TargetPath: "missing", LinkType: "parent"}},
		},
	}
	q := NewQueryService(st, nil)

	res, err := q.QueryGraph(context.Background(), "root", 2, nil)
	if err != nil {
		t.Fatalf("QueryGraph should tolerate broken link: %v", err)
	}
	if len(res.Nodes) != 1 {
		t.Errorf("expected only root node, got %d", len(res.Nodes))
	}
	// Edge is still recorded even though target is missing.
	if len(res.Edges) != 1 {
		t.Errorf("expected 1 edge, got %d", len(res.Edges))
	}
}

func TestQueryService_QueryGraph_PropagatesStoreError(t *testing.T) {
	sentinel := errors.New("db down")
	st := &queryFakeStore{
		projections:   map[string]*store.ArtifactProjection{},
		projectionErr: sentinel,
	}
	q := NewQueryService(st, nil)
	_, err := q.QueryGraph(context.Background(), "root", 2, nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error to propagate, got %v", err)
	}
}

func TestQueryService_QueryHistory_DefaultsAndMaps(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	gitFake := &queryFakeGit{
		logResult: []git.CommitInfo{{
			SHA:       "abc123",
			Timestamp: ts,
			Author:    git.Author{Name: "Alice"},
			Message:   "initial",
			Trailers: map[string]string{
				"Trace-ID":  "t-1",
				"Operation": "artifact.create",
			},
		}},
	}
	q := NewQueryService(&queryFakeStore{}, gitFake)

	// limit=0 defaults to 20 — verified via the captured opts.
	entries, err := q.QueryHistory(context.Background(), "path.md", 0)
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	if gitFake.logOptsSeen.Limit != 20 {
		t.Errorf("expected default limit 20, got %d", gitFake.logOptsSeen.Limit)
	}
	if gitFake.logOptsSeen.Path != "path.md" {
		t.Errorf("expected path pass-through, got %q", gitFake.logOptsSeen.Path)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.CommitSHA != "abc123" || e.Author != "Alice" || e.TraceID != "t-1" || e.Operation != "artifact.create" {
		t.Errorf("unexpected entry mapping: %+v", e)
	}
	if e.Timestamp != "2025-06-01T12:00:00Z" {
		t.Errorf("expected UTC RFC-3339-like timestamp, got %q", e.Timestamp)
	}

	gitFake.logErr = errors.New("git blew up")
	if _, err := q.QueryHistory(context.Background(), "x", 5); err == nil || !strings.Contains(err.Error(), "query history for x") {
		t.Errorf("expected wrapped git error, got %v", err)
	}
}

func TestQueryService_CheckFreshness(t *testing.T) {
	gitFake := &queryFakeGit{head: "deadbeef"}

	// never_synced when state is nil
	st := &queryFakeStore{}
	q := NewQueryService(st, gitFake)
	chk, err := q.CheckFreshness(context.Background())
	if err != nil {
		t.Fatalf("CheckFreshness: %v", err)
	}
	if chk.SyncStatus != "never_synced" || !chk.IsStale {
		t.Errorf("never_synced: got %+v", chk)
	}

	// in-sync: HEAD matches, status idle
	st.syncState = &store.SyncState{LastSyncedCommit: "deadbeef", Status: "idle"}
	chk, err = q.CheckFreshness(context.Background())
	if err != nil {
		t.Fatalf("CheckFreshness in-sync: %v", err)
	}
	if chk.IsStale {
		t.Errorf("expected fresh, got %+v", chk)
	}

	// stale via HEAD drift
	st.syncState = &store.SyncState{LastSyncedCommit: "oldsha", Status: "idle"}
	chk, _ = q.CheckFreshness(context.Background())
	if !chk.IsStale {
		t.Errorf("expected stale (HEAD drift), got %+v", chk)
	}

	// stale via non-idle status
	st.syncState = &store.SyncState{LastSyncedCommit: "deadbeef", Status: "syncing"}
	chk, _ = q.CheckFreshness(context.Background())
	if !chk.IsStale {
		t.Errorf("expected stale (status != idle), got %+v", chk)
	}

	// HEAD failure surfaces
	gitFake.headErr = errors.New("no git")
	if _, err := q.CheckFreshness(context.Background()); err == nil {
		t.Error("expected HEAD error to surface")
	}
	gitFake.headErr = nil

	// sync-state failure surfaces
	st.syncStateErr = errors.New("store down")
	if _, err := q.CheckFreshness(context.Background()); err == nil {
		t.Error("expected sync-state error to surface")
	}
}

func TestQueryService_QueryRuns_Delegates(t *testing.T) {
	want := []domain.Run{{RunID: "r1"}, {RunID: "r2"}}
	st := &queryFakeStore{runs: want}
	q := NewQueryService(st, nil)
	got, err := q.QueryRuns(context.Background(), "task")
	if err != nil {
		t.Fatalf("QueryRuns: %v", err)
	}
	if len(got) != len(want) {
		t.Errorf("expected %d runs, got %d", len(want), len(got))
	}

	st.runsErr = errors.New("boom")
	if _, err := q.QueryRuns(context.Background(), "task"); err == nil {
		t.Error("expected error pass-through")
	}
}

func TestContainsString(t *testing.T) {
	if !containsString([]string{"a", "b", "c"}, "b") {
		t.Error("expected true for present element")
	}
	if containsString([]string{"a", "b"}, "z") {
		t.Error("expected false for missing element")
	}
	if containsString(nil, "a") {
		t.Error("expected false for nil slice")
	}
	if containsString([]string{}, "") {
		t.Error("expected false for empty slice and empty target")
	}
}
