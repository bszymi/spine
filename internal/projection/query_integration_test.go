//go:build integration

package projection_test

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/testutil"
)

func setupQueryTest(t *testing.T) (*projection.QueryService, *projection.Service, *git.CLIClient, string, *store.PostgresStore) {
	t.Helper()

	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	db := store.NewTestStore(t)
	syncSvc := projection.NewService(client, db, nil, 1*time.Second)
	querySvc := projection.NewQueryService(db, client)

	// Add test artifacts with links
	testutil.WriteFile(t, repo, "governance/charter.md", `---
type: Governance
title: Charter
status: Foundational
links:
  - type: related_to
    target: /governance/guidelines.md
---

# Charter
`)
	testutil.WriteFile(t, repo, "governance/guidelines.md", `---
type: Governance
title: Guidelines
status: Living Document
links:
  - type: related_to
    target: /governance/charter.md
---

# Guidelines
`)
	testutil.WriteFile(t, repo, "architecture/domain-model.md", `---
type: Architecture
title: Domain Model
status: Living Document
version: "0.1"
links:
  - type: related_to
    target: /governance/charter.md
---

# Domain Model
`)
	testutil.WriteFile(t, repo, "workflows/task-execution.yaml", `id: task-execution
name: Task Execution
version: "1.0"
status: Active
applies_to:
  - Task
`)
	testutil.WriteFile(t, repo, "README.md", "# Readme\n")
	testutil.GitAdd(t, repo, ".", "add test content")

	// Full rebuild to populate projections
	if err := syncSvc.FullRebuild(context.Background()); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}

	return querySvc, syncSvc, client, repo, db
}

func TestQueryArtifactsByType(t *testing.T) {
	querySvc, _, _, _, db := setupQueryTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	result, err := querySvc.QueryArtifacts(ctx, store.ArtifactQuery{
		Type:  "Governance",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("QueryArtifacts: %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("expected 2 Governance artifacts, got %d", len(result.Items))
	}
}

func TestQueryArtifactsByStatus(t *testing.T) {
	querySvc, _, _, _, db := setupQueryTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	result, err := querySvc.QueryArtifacts(ctx, store.ArtifactQuery{
		Status: "Living Document",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("QueryArtifacts: %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("expected 2 Living Document artifacts, got %d", len(result.Items))
	}
}

func TestQueryArtifactsSearch(t *testing.T) {
	querySvc, _, _, _, db := setupQueryTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	result, err := querySvc.QueryArtifacts(ctx, store.ArtifactQuery{
		Search: "Domain",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("QueryArtifacts search: %v", err)
	}
	if len(result.Items) != 1 {
		t.Errorf("expected 1 artifact matching 'Domain', got %d", len(result.Items))
	}
}

func TestQueryArtifactsPagination(t *testing.T) {
	querySvc, _, _, _, db := setupQueryTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	// First page
	result1, err := querySvc.QueryArtifacts(ctx, store.ArtifactQuery{Limit: 2})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(result1.Items) != 2 {
		t.Fatalf("expected 2 items on page 1, got %d", len(result1.Items))
	}
	if !result1.HasMore {
		t.Error("expected HasMore on page 1")
	}

	// Second page
	result2, err := querySvc.QueryArtifacts(ctx, store.ArtifactQuery{
		Limit:  2,
		Cursor: result1.NextCursor,
	})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(result2.Items) != 1 {
		t.Errorf("expected 1 item on page 2, got %d", len(result2.Items))
	}
}

func TestQueryGraph(t *testing.T) {
	querySvc, _, _, _, db := setupQueryTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	graph, err := querySvc.QueryGraph(ctx, "governance/charter.md", 2, nil)
	if err != nil {
		t.Fatalf("QueryGraph: %v", err)
	}

	if graph.Root != "governance/charter.md" {
		t.Errorf("expected root governance/charter.md, got %s", graph.Root)
	}
	if len(graph.Nodes) < 2 {
		t.Errorf("expected at least 2 nodes (charter + linked), got %d", len(graph.Nodes))
	}
	if len(graph.Edges) < 1 {
		t.Errorf("expected at least 1 edge, got %d", len(graph.Edges))
	}
}

func TestQueryGraphDepthLimit(t *testing.T) {
	querySvc, _, _, _, db := setupQueryTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	// Depth 0 should only return the root
	graph, err := querySvc.QueryGraph(ctx, "governance/charter.md", 0, nil)
	if err != nil {
		t.Fatalf("QueryGraph depth 0: %v", err)
	}
	// Default depth is 2 when 0 is passed
	if len(graph.Nodes) < 1 {
		t.Error("expected at least 1 node")
	}
}

func TestQueryGraphLinkTypeFilter(t *testing.T) {
	querySvc, _, _, _, db := setupQueryTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	graph, err := querySvc.QueryGraph(ctx, "governance/charter.md", 2, []string{"parent"})
	if err != nil {
		t.Fatalf("QueryGraph with filter: %v", err)
	}
	// No parent links exist, so only root node
	if len(graph.Edges) != 0 {
		t.Errorf("expected 0 edges with parent filter, got %d", len(graph.Edges))
	}
}

func TestQueryHistory(t *testing.T) {
	querySvc, _, _, _, db := setupQueryTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	entries, err := querySvc.QueryHistory(ctx, "governance/charter.md", 10)
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	if len(entries) < 1 {
		t.Error("expected at least 1 history entry")
	}
}

func TestCheckFreshness(t *testing.T) {
	querySvc, _, _, _, db := setupQueryTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	check, err := querySvc.CheckFreshness(ctx)
	if err != nil {
		t.Fatalf("CheckFreshness: %v", err)
	}
	if check.IsStale {
		t.Error("should not be stale immediately after rebuild")
	}
	if check.SyncStatus != "idle" {
		t.Errorf("expected idle, got %s", check.SyncStatus)
	}
}

func TestCheckFreshnessStale(t *testing.T) {
	querySvc, _, _, repo, db := setupQueryTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	// Add a new commit to make projections stale
	testutil.WriteFile(t, repo, "governance/new.md", `---
type: Governance
title: New Doc
status: Living Document
---

# New
`)
	testutil.GitAdd(t, repo, "governance/new.md", "add new doc")

	check, err := querySvc.CheckFreshness(ctx)
	if err != nil {
		t.Fatalf("CheckFreshness: %v", err)
	}
	if !check.IsStale {
		t.Error("should be stale after new commit")
	}
}
