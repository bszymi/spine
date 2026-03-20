package git

import (
	"testing"

	"github.com/bszymi/spine/internal/testutil"
)

// NewTestRepo creates a temporary Git repository for testing Git client operations.
// The repository is initialized with a main branch and an empty commit.
// Cleaned up automatically when the test ends.
func NewTestRepo(t *testing.T) (*CLIClient, string) {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	return NewCLIClient(repo), repo
}
