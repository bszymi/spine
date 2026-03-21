package git

import (
	"context"
	"time"
)

// GitClient defines the interface for Git repository operations.
// Per Implementation Guide §3.1.
// The v0.x implementation shells out to the git CLI.
type GitClient interface {
	Clone(ctx context.Context, url, path string) error
	Commit(ctx context.Context, opts CommitOpts) (CommitResult, error)
	Merge(ctx context.Context, opts MergeOpts) (MergeResult, error)
	CreateBranch(ctx context.Context, name, base string) error
	DeleteBranch(ctx context.Context, name string) error
	Diff(ctx context.Context, from, to string) ([]FileDiff, error)
	Log(ctx context.Context, opts LogOpts) ([]CommitInfo, error)
	ReadFile(ctx context.Context, ref, path string) ([]byte, error)
	ListFiles(ctx context.Context, ref, pattern string) ([]string, error)
	Head(ctx context.Context) (string, error)
}

// CommitOpts defines options for creating a Git commit.
type CommitOpts struct {
	Message    string            // Commit summary line
	Body       string            // Optional commit body
	Trailers   map[string]string // Structured trailers (Trace-ID, Actor-ID, Run-ID, Operation)
	Author     Author            // Commit author identity
	AllowEmpty bool              // Allow creating empty commits
}

// Author represents a Git commit author.
type Author struct {
	Name  string
	Email string
}

// CommitResult contains the result of a successful commit.
type CommitResult struct {
	SHA string // Full commit SHA
}

// MergeOpts defines options for merging branches.
type MergeOpts struct {
	Source   string // Branch to merge from
	Target   string // Branch to merge into (empty = current branch)
	Strategy string // "fast-forward" or "merge-commit"
	Message  string // Merge commit message (for merge-commit strategy)
	Trailers map[string]string
}

// MergeResult contains the result of a successful merge.
type MergeResult struct {
	SHA         string // Resulting commit SHA
	FastForward bool   // True if merge was fast-forward
}

// LogOpts defines options for querying Git log.
type LogOpts struct {
	Path  string // Filter by file path (optional)
	Limit int    // Max number of commits (0 = unlimited)
	Since string // Commit SHA to start from (exclusive)
}

// CommitInfo represents a Git commit from the log.
type CommitInfo struct {
	SHA       string
	Author    Author
	Message   string
	Trailers  map[string]string
	Timestamp time.Time
}

// FileDiff represents a file change between two Git refs.
type FileDiff struct {
	Path   string
	Status string // "added", "modified", "deleted", "renamed"
}
