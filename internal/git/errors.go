package git

import "fmt"

// GitErrorKind classifies Git operation failures.
// Per Error Handling §5.
type GitErrorKind string

const (
	ErrKindTransient GitErrorKind = "transient" // Lock contention, network timeout — retryable
	ErrKindPermanent GitErrorKind = "permanent" // Conflict, missing ref — not retryable
	ErrKindNotFound  GitErrorKind = "not_found" // File or ref does not exist
)

// GitError represents a Git operation failure with classification.
type GitError struct {
	Kind    GitErrorKind
	Op      string // The operation that failed (e.g., "commit", "merge", "read_file")
	Message string
	Err     error // Underlying error
}

func (e *GitError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("git %s: %s: %v", e.Op, e.Message, e.Err)
	}
	return fmt.Sprintf("git %s: %s", e.Op, e.Message)
}

func (e *GitError) Unwrap() error {
	return e.Err
}

// IsRetryable returns true if the error is transient and the operation may be retried.
func (e *GitError) IsRetryable() bool {
	return e.Kind == ErrKindTransient
}

// classifyGitError analyzes git stderr output and classifies the error.
func classifyGitError(op, stderr string) *GitError {
	switch {
	case containsAny(stderr, "lock", "Unable to create", ".lock"):
		return &GitError{Kind: ErrKindTransient, Op: op, Message: "repository locked"}
	case containsAny(stderr, "CONFLICT", "conflict", "Merge conflict"):
		return &GitError{Kind: ErrKindPermanent, Op: op, Message: "merge conflict"}
	case containsAny(stderr, "does not exist", "not found", "bad revision", "unknown revision"):
		return &GitError{Kind: ErrKindNotFound, Op: op, Message: stderr}
	case containsAny(stderr, "fatal: not a git repository"):
		return &GitError{Kind: ErrKindPermanent, Op: op, Message: "not a git repository"}
	case containsAny(stderr, "Connection refused", "Could not resolve", "timeout"):
		return &GitError{Kind: ErrKindTransient, Op: op, Message: "network error"}
	default:
		return &GitError{Kind: ErrKindPermanent, Op: op, Message: stderr}
	}
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
