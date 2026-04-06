package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

type fakeBlockingStore struct {
	links       map[string][]store.ArtifactLink // sourcePath -> links
	targetLinks map[string][]store.ArtifactLink // targetPath -> links
	projections map[string]*store.ArtifactProjection
}

func newFakeBlockingStore() *fakeBlockingStore {
	return &fakeBlockingStore{
		links:       make(map[string][]store.ArtifactLink),
		targetLinks: make(map[string][]store.ArtifactLink),
		projections: make(map[string]*store.ArtifactProjection),
	}
}

func (f *fakeBlockingStore) QueryArtifactLinks(_ context.Context, sourcePath string) ([]store.ArtifactLink, error) {
	return f.links[sourcePath], nil
}

func (f *fakeBlockingStore) QueryArtifactLinksByTarget(_ context.Context, targetPath string) ([]store.ArtifactLink, error) {
	return f.targetLinks[targetPath], nil
}

func (f *fakeBlockingStore) GetArtifactProjection(_ context.Context, path string) (*store.ArtifactProjection, error) {
	proj, ok := f.projections[path]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "not found")
	}
	return proj, nil
}

func makeOrchWithBlocking(bs BlockingStore) *Orchestrator {
	orch := &Orchestrator{
		blocking: bs,
	}
	return orch
}

func TestIsBlocked_NoLinks(t *testing.T) {
	bs := newFakeBlockingStore()
	orch := makeOrchWithBlocking(bs)

	result, err := orch.IsBlocked(context.Background(), "tasks/task-1.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Blocked {
		t.Error("expected not blocked when no links")
	}
}

func TestIsBlocked_BlockerNotComplete(t *testing.T) {
	bs := newFakeBlockingStore()
	bs.links["tasks/task-2.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/task-2.md", TargetPath: "tasks/task-1.md", LinkType: "blocked_by"},
	}
	bs.projections["tasks/task-1.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/task-1.md",
		Status:       string(domain.StatusPending),
	}
	orch := makeOrchWithBlocking(bs)

	result, err := orch.IsBlocked(context.Background(), "tasks/task-2.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Blocked {
		t.Error("expected blocked")
	}
	if len(result.BlockedBy) != 1 || result.BlockedBy[0] != "tasks/task-1.md" {
		t.Errorf("expected blocked by tasks/task-1.md, got %v", result.BlockedBy)
	}
}

func TestIsBlocked_BlockerComplete(t *testing.T) {
	bs := newFakeBlockingStore()
	bs.links["tasks/task-2.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/task-2.md", TargetPath: "tasks/task-1.md", LinkType: "blocked_by"},
	}
	bs.projections["tasks/task-1.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/task-1.md",
		Status:       string(domain.StatusCompleted),
	}
	orch := makeOrchWithBlocking(bs)

	result, err := orch.IsBlocked(context.Background(), "tasks/task-2.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Blocked {
		t.Error("expected not blocked when blocker is completed")
	}
	if len(result.Resolved) != 1 {
		t.Errorf("expected 1 resolved blocker, got %d", len(result.Resolved))
	}
}

func TestIsBlocked_MultipleBlockers_PartiallyResolved(t *testing.T) {
	bs := newFakeBlockingStore()
	bs.links["tasks/task-3.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/task-3.md", TargetPath: "tasks/task-1.md", LinkType: "blocked_by"},
		{SourcePath: "tasks/task-3.md", TargetPath: "tasks/task-2.md", LinkType: "blocked_by"},
	}
	bs.projections["tasks/task-1.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/task-1.md",
		Status:       string(domain.StatusCompleted),
	}
	bs.projections["tasks/task-2.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/task-2.md",
		Status:       string(domain.StatusInProgress),
	}
	orch := makeOrchWithBlocking(bs)

	result, err := orch.IsBlocked(context.Background(), "tasks/task-3.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Blocked {
		t.Error("expected blocked when one blocker is still in progress")
	}
	if len(result.BlockedBy) != 1 {
		t.Errorf("expected 1 blocker, got %d", len(result.BlockedBy))
	}
	if len(result.Resolved) != 1 {
		t.Errorf("expected 1 resolved, got %d", len(result.Resolved))
	}
}

func TestIsBlocked_CircularDependency(t *testing.T) {
	bs := newFakeBlockingStore()
	bs.links["tasks/task-1.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/task-1.md", TargetPath: "tasks/task-2.md", LinkType: "blocked_by"},
	}
	bs.links["tasks/task-2.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/task-2.md", TargetPath: "tasks/task-1.md", LinkType: "blocked_by"},
	}
	bs.projections["tasks/task-1.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/task-1.md",
		Status:       string(domain.StatusPending),
	}
	bs.projections["tasks/task-2.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/task-2.md",
		Status:       string(domain.StatusPending),
	}
	orch := makeOrchWithBlocking(bs)

	// Direct call won't hit cycle since IsBlocked doesn't recurse into blockers
	// The cycle detection is for future transitive blocking support
	result, err := orch.IsBlocked(context.Background(), "tasks/task-1.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Blocked {
		t.Error("expected blocked")
	}
}

func TestIsBlocked_MissingProjection(t *testing.T) {
	bs := newFakeBlockingStore()
	bs.links["tasks/task-2.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/task-2.md", TargetPath: "tasks/missing.md", LinkType: "blocked_by"},
	}
	// No projection for missing.md
	orch := makeOrchWithBlocking(bs)

	result, err := orch.IsBlocked(context.Background(), "tasks/task-2.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Blocked {
		t.Error("expected blocked when blocker projection is missing (safe default)")
	}
}

func TestIsBlocked_IgnoresNonBlockedByLinks(t *testing.T) {
	bs := newFakeBlockingStore()
	bs.links["tasks/task-1.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/task-1.md", TargetPath: "epics/epic-1.md", LinkType: "parent"},
		{SourcePath: "tasks/task-1.md", TargetPath: "tasks/task-2.md", LinkType: "related_to"},
	}
	orch := makeOrchWithBlocking(bs)

	result, err := orch.IsBlocked(context.Background(), "tasks/task-1.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Blocked {
		t.Error("expected not blocked — parent and related_to links should be ignored")
	}
}

func TestIsBlocked_NilBlockingStore(t *testing.T) {
	orch := &Orchestrator{} // no blocking store

	result, err := orch.IsBlocked(context.Background(), "tasks/task-1.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Blocked {
		t.Error("expected not blocked when blocking store is nil")
	}
}

func TestCheckAndEmitBlockingTransition_DependentUnblocked(t *testing.T) {
	bs := newFakeBlockingStore()

	// Task A is blocked by Task B.
	bs.targetLinks["tasks/task-b.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/task-a.md", TargetPath: "tasks/task-b.md", LinkType: "blocked_by"},
	}
	// Task A has only one blocker (task-b), and task-b is now completed.
	bs.links["tasks/task-a.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/task-a.md", TargetPath: "tasks/task-b.md", LinkType: "blocked_by"},
	}
	bs.projections["tasks/task-b.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/task-b.md",
		Status:       string(domain.StatusCompleted),
	}

	events := &memEventCollector{}
	orch := &Orchestrator{blocking: bs, events: events}

	orch.CheckAndEmitBlockingTransition(context.Background(), "tasks/task-b.md")

	// Should have emitted task_unblocked for task-a.
	if len(events.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events.events))
	}
	if events.events[0].Type != domain.EventTaskUnblocked {
		t.Errorf("expected task_unblocked, got %s", events.events[0].Type)
	}
}

func TestCheckAndEmitBlockingTransition_DependentStillBlocked(t *testing.T) {
	bs := newFakeBlockingStore()

	// Task A is blocked by both Task B and Task C.
	bs.targetLinks["tasks/task-b.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/task-a.md", TargetPath: "tasks/task-b.md", LinkType: "blocked_by"},
	}
	bs.links["tasks/task-a.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/task-a.md", TargetPath: "tasks/task-b.md", LinkType: "blocked_by"},
		{SourcePath: "tasks/task-a.md", TargetPath: "tasks/task-c.md", LinkType: "blocked_by"},
	}
	bs.projections["tasks/task-b.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/task-b.md", Status: string(domain.StatusCompleted),
	}
	bs.projections["tasks/task-c.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/task-c.md", Status: string(domain.StatusPending),
	}

	events := &memEventCollector{}
	orch := &Orchestrator{blocking: bs, events: events}

	orch.CheckAndEmitBlockingTransition(context.Background(), "tasks/task-b.md")

	// Task A is still blocked by task-c — no event should be emitted.
	if len(events.events) != 0 {
		t.Errorf("expected 0 events (still blocked), got %d", len(events.events))
	}
}

func TestCheckAndEmitBlockingTransition_NilStore(t *testing.T) {
	orch := &Orchestrator{} // no blocking store — should not panic
	orch.CheckAndEmitBlockingTransition(context.Background(), "tasks/task-1.md")
}

// memEventCollector captures emitted events for testing.
type memEventCollector struct {
	events []domain.Event
}

func (m *memEventCollector) Emit(_ context.Context, e domain.Event) error {
	m.events = append(m.events, e)
	return nil
}
