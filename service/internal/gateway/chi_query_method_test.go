package gateway_test

// INIT-024 TASK-013: chi v5.5.0 adds the HTTP QUERY method to the methods
// a Handle/HandleFunc route matches (chi v5.2.5 answered 405 for any QUERY
// request before a handler ran). The API's wildcard routes are HandleFunc
// routes, so a QUERY request now reaches their handlers, which dispatch on
// the method and must refuse it. Method-specific routes still answer 405.

import (
	"net/http"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/testutil"
)

func TestQueryMethod_ReachesNoWildcardAction(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, "governance/small.md", f5GatewayDoc)
	f5GatewayGit(t, repo, "add", "-A")
	f5GatewayGit(t, repo, "commit", "-q", "-m", "seed")
	ts, token := f5GatewayServer(t, repo)

	// The same paths answer as before for their own methods.
	if status, er := f5Do(t, ts, token, http.MethodGet, "/api/v1/artifacts/governance/small.md", ""); status != http.StatusOK {
		t.Fatalf("GET artifact: %d %+v, want 200", status, er)
	}

	const query = "QUERY"
	for _, path := range []string{
		"/api/v1/artifacts/governance/small.md",
		"/api/v1/artifacts/governance/small.md/links",
		"/api/v1/artifacts/governance/small.md/validate",
		"/api/v1/workflows/task-default",
		"/api/v1/workflows/task-default/validate",
		"/api/v1/tasks/governance/small.md/abandon",
	} {
		status, er := f5Do(t, ts, token, query, path, "{}")
		if status != http.StatusNotFound || len(er.Errors) == 0 || er.Errors[0].Code != string(domain.ErrNotFound) {
			t.Errorf("QUERY %s: %d %+v, want the wildcard handler's 404", path, status, er)
		}
	}
	// A route registered for one method still refuses another, QUERY
	// included.
	if status, _ := f5Do(t, ts, token, query, "/api/v1/artifacts", ""); status != http.StatusMethodNotAllowed {
		t.Errorf("QUERY /api/v1/artifacts: %d, want 405", status)
	}
}
