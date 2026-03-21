package projection

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/store"
)

// QueryService provides read operations against the Projection Store.
// Per Access Surface §3.4: queries read from projections, which are
// eventually consistent with Git.
type QueryService struct {
	store store.Store
	git   git.GitClient
}

// NewQueryService creates a new query service.
func NewQueryService(s store.Store, gitClient git.GitClient) *QueryService {
	return &QueryService{store: s, git: gitClient}
}

// QueryArtifacts searches projected artifacts by type, status, and cursor pagination.
func (q *QueryService) QueryArtifacts(ctx context.Context, query store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	return q.store.QueryArtifacts(ctx, query)
}

// GetArtifact retrieves a single projected artifact by path.
func (q *QueryService) GetArtifact(ctx context.Context, path string) (*store.ArtifactProjection, error) {
	return q.store.GetArtifactProjection(ctx, path)
}

// GraphNode represents a node in the artifact relationship graph.
type GraphNode struct {
	Path         string `json:"path"`
	ArtifactType string `json:"artifact_type"`
	Title        string `json:"title"`
	Status       string `json:"status"`
}

// GraphEdge represents a relationship between two artifacts.
type GraphEdge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	LinkType string `json:"link_type"`
}

// GraphResult contains the result of a graph traversal query.
type GraphResult struct {
	Root  string      `json:"root"`
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// QueryGraph traverses the artifact relationship graph starting from a root path.
// Depth controls how many levels of links to follow (max 5 per API spec).
func (q *QueryService) QueryGraph(ctx context.Context, rootPath string, depth int, linkTypes []string) (*GraphResult, error) {
	if depth <= 0 {
		depth = 2
	}
	if depth > 5 {
		depth = 5
	}

	result := &GraphResult{Root: rootPath}
	visited := make(map[string]bool)

	if err := q.traverseGraph(ctx, rootPath, depth, linkTypes, visited, result); err != nil {
		return nil, err
	}

	return result, nil
}

func (q *QueryService) traverseGraph(ctx context.Context, path string, depth int, linkTypes []string, visited map[string]bool, result *GraphResult) error {
	if depth < 0 || visited[path] {
		return nil
	}
	visited[path] = true

	// Get the artifact node
	proj, err := q.store.GetArtifactProjection(ctx, path)
	if err != nil {
		return nil // skip missing artifacts
	}

	result.Nodes = append(result.Nodes, GraphNode{
		Path:         proj.ArtifactPath,
		ArtifactType: proj.ArtifactType,
		Title:        proj.Title,
		Status:       proj.Status,
	})

	// Get outgoing links
	links, err := q.store.QueryArtifactLinks(ctx, path)
	if err != nil {
		return nil // skip if links query fails
	}

	for _, link := range links {
		// Filter by link types if specified
		if len(linkTypes) > 0 && !containsString(linkTypes, link.LinkType) {
			continue
		}

		result.Edges = append(result.Edges, GraphEdge{
			Source:   link.SourcePath,
			Target:   link.TargetPath,
			LinkType: link.LinkType,
		})

		// Normalize target path: canonical paths start with / but projection paths don't
		targetPath := link.TargetPath
		if targetPath != "" && targetPath[0] == '/' {
			targetPath = targetPath[1:]
		}

		// Recurse into the target
		if err := q.traverseGraph(ctx, targetPath, depth-1, linkTypes, visited, result); err != nil {
			continue
		}
	}

	return nil
}

// HistoryEntry represents a commit in an artifact's change history.
type HistoryEntry struct {
	CommitSHA string `json:"commit_sha"`
	Timestamp string `json:"timestamp"`
	Author    string `json:"author"`
	Message   string `json:"message"`
	TraceID   string `json:"trace_id,omitempty"`
	Operation string `json:"operation,omitempty"`
}

// QueryHistory returns the Git commit history for an artifact.
// This reads directly from Git, not from projections.
func (q *QueryService) QueryHistory(ctx context.Context, path string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = 20
	}

	commits, err := q.git.Log(ctx, git.LogOpts{
		Path:  path,
		Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("query history for %s: %w", path, err)
	}

	var entries []HistoryEntry
	for _, c := range commits {
		entries = append(entries, HistoryEntry{
			CommitSHA: c.SHA,
			Timestamp: c.Timestamp.Format("2006-01-02T15:04:05Z"),
			Author:    c.Author.Name,
			Message:   c.Message,
			TraceID:   c.Trailers["Trace-ID"],
			Operation: c.Trailers["Operation"],
		})
	}

	return entries, nil
}

// FreshnessCheck reports how up-to-date the projection store is.
type FreshnessCheck struct {
	LastSyncedCommit string `json:"last_synced_commit"`
	CurrentHead      string `json:"current_head"`
	IsStale          bool   `json:"is_stale"`
	SyncStatus       string `json:"sync_status"`
}

// CheckFreshness compares the projection sync state against current Git HEAD.
func (q *QueryService) CheckFreshness(ctx context.Context) (*FreshnessCheck, error) {
	head, err := q.git.Head(ctx)
	if err != nil {
		return nil, fmt.Errorf("get HEAD: %w", err)
	}

	state, err := q.store.GetSyncState(ctx)
	if err != nil {
		return nil, fmt.Errorf("get sync state: %w", err)
	}

	check := &FreshnessCheck{
		CurrentHead: head,
	}

	if state == nil {
		check.IsStale = true
		check.SyncStatus = "never_synced"
		return check, nil
	}

	check.LastSyncedCommit = state.LastSyncedCommit
	check.SyncStatus = state.Status
	check.IsStale = state.LastSyncedCommit != head || state.Status != "idle"

	return check, nil
}

// QueryRuns delegates to the store's run queries.
func (q *QueryService) QueryRuns(ctx context.Context, taskPath string) ([]domain.Run, error) {
	return q.store.ListRunsByTask(ctx, taskPath)
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
