package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/config"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/testutil"
	"github.com/go-chi/chi/v5"
)

// TestServerStartup_NoUnwiredServices boots the full server with a
// temp-repo Git client, an in-memory stub store, and dev mode, then
// probes every advertised endpoint. Any 503 response whose body
// contains "service not configured" is a wiring bug of the class
// fixed by PR #415, and fails the test.
//
// The endpoint list is derived at runtime from the chi router, so
// adding a new route does not require editing this test.
func TestServerStartup_NoUnwiredServices(t *testing.T) {
	srv, ts := bootSmokeServer(t, nil)
	defer ts.Close()

	probeAllRoutes(t, srv, ts, "smoke-token")
}

// TestServerStartup_DetectsMissingWorkflowService proves the smoke
// test catches the exact class of bug from PR #415: a service field
// left unwired in ServerConfig.
func TestServerStartup_DetectsMissingWorkflowService(t *testing.T) {
	srv, ts := bootSmokeServer(t, func(cfg *gateway.ServerConfig) {
		cfg.Workflows = nil
	})
	defer ts.Close()

	failures := collectWiringFailures(t, srv, ts, "smoke-token")
	if len(failures) == 0 {
		t.Fatal("expected at least one 503 'workflow service not configured' when Workflows is nil; got none — the smoke test is not catching wiring regressions")
	}
	// Sanity: at least one failure must be a workflow endpoint.
	foundWorkflow := false
	for _, f := range failures {
		if strings.Contains(f.path, "/workflows") {
			foundWorkflow = true
			break
		}
	}
	if !foundWorkflow {
		t.Fatalf("expected a /workflows endpoint failure; got %+v", failures)
	}
}

// bootSmokeServer assembles a gateway.Server via buildServerConfig
// using the same helpers the serve command uses, then wraps it in an
// httptest.Server. The optional mutate callback lets tests zero out
// a ServerConfig field to simulate a wiring regression.
func bootSmokeServer(t *testing.T, mutate func(*gateway.ServerConfig)) (*gateway.Server, *httptest.Server) {
	t.Helper()

	repoPath := testutil.NewTempRepo(t)
	gitClient := git.NewCLIClient(repoPath)
	q := queue.NewMemoryQueue(16)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go q.Start(ctx)

	deps := serveDeps{
		Store:      stubStore{},
		RepoPath:   repoPath,
		SpineCfg:   &config.SpineConfig{ArtifactsDir: "/"},
		GitClient:  gitClient,
		Queue:      q,
		Events:     event.NewQueueRouter(q),
		DevMode:    true,
		RuntimeEnv: "development",
	}

	rt, err := buildServerConfig(ctx, deps)
	if err != nil {
		t.Fatalf("buildServerConfig: %v", err)
	}
	if mutate != nil {
		mutate(&rt.Config)
	}

	srv := gateway.NewServer(":0", rt.Config)
	ts := httptest.NewServer(srv.Handler())
	return srv, ts
}

// probeAllRoutes walks every route the server exposes and issues a
// minimal authenticated request. Any 503 with "service not configured"
// is a wiring bug and fails the test. Other status codes — 4xx, 500,
// 404 — are fine; we're only gating against the PR #415 pattern.
func probeAllRoutes(t *testing.T, srv *gateway.Server, ts *httptest.Server, token string) {
	t.Helper()
	failures := collectWiringFailures(t, srv, ts, token)
	for _, f := range failures {
		t.Errorf("%s %s returned 503 service-not-configured: %s", f.method, f.path, f.body)
	}
}

type wiringFailure struct {
	method string
	path   string
	body   string
}

// collectWiringFailures probes each advertised route and returns the
// subset that 503 with a "service not configured" body.
func collectWiringFailures(t *testing.T, srv *gateway.Server, ts *httptest.Server, token string) []wiringFailure {
	t.Helper()

	routes, ok := srv.Handler().(chi.Routes)
	if !ok {
		t.Fatalf("server handler is not chi.Routes; got %T", srv.Handler())
	}

	client := ts.Client()
	var failures []wiringFailure

	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		url := ts.URL + normalizeRoute(route)
		// SSE stream blocks until the server closes the connection; skip
		// it to keep the test under 2 s. Its wiring is covered by the
		// /events list endpoint under the same service.
		if strings.HasSuffix(route, "/events/stream") {
			return nil
		}

		req, err := http.NewRequestWithContext(context.Background(), method, url, http.NoBody)
		if err != nil {
			t.Errorf("new request %s %s: %v", method, route, err)
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("probe %s %s: %v", method, route, err)
			return nil
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusServiceUnavailable && strings.Contains(string(body), "service not configured") {
			failures = append(failures, wiringFailure{method: method, path: route, body: string(body)})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}

	return failures
}

// normalizeRoute replaces chi route params and wildcards with dummy
// values so we can issue concrete URLs against the httptest server.
// Concrete values don't matter: we only care that routing dispatches
// to the real handler (not the 404 fallback).
var chiParamRE = regexp.MustCompile(`\{[^/]+\}`)

func normalizeRoute(route string) string {
	r := chiParamRE.ReplaceAllString(route, "placeholder")
	// Replace trailing "/*" wildcard with a concrete segment so chi
	// dispatches to the wildcard handler instead of redirecting.
	if strings.HasSuffix(r, "/*") {
		r = strings.TrimSuffix(r, "/*") + "/placeholder"
	}
	return r
}
