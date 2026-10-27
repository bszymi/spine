package gateway

import (
	"context"
	"testing"

	"github.com/bszymi/spine/service/internal/githttp"
)

// t006GitHandler builds the git handler with each workspace's runner
// addresses bound to it (INIT-024 TASK-006). It is the one piece of the
// TASK-006 tests that names the new configuration: the scaffold-base run
// that shows the regression tests failing replaces this file (go test
// -overlay) with one that hands the same addresses to the base's
// process-wide TrustedCIDRs list — the only trust an operator could
// configure there.
func t006GitHandler(t *testing.T, trusted map[string][]string, receivePack bool) *githttp.Handler {
	t.Helper()
	h, err := githttp.NewHandler(githttp.Config{
		// handleGit attaches the resolved repository path per request;
		// this resolver is never consulted on the request path.
		ResolveRepoPath:         func(context.Context, string) (string, error) { return "", nil },
		TrustedCIDRsByWorkspace: trusted,
		ReceivePackEnabled:      receivePack,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}
