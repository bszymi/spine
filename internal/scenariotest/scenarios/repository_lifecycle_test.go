//go:build scenario

package scenarios_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/cli"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/scenariotest/harness"
	"github.com/bszymi/spine/internal/store"
)

// repoTestEnv is the wired stack a repository scenario operates on. It
// is the integration-level cross-section the unit tests in TASK-002/003
// /004 deliberately stop short of: a real Postgres binding store
// (TASK-002) sits behind the catalog-aware service layer (TASK-003) and
// is exercised via the public API + CLI HTTP client (TASK-004).
type repoTestEnv struct {
	WorkspaceID  string
	Primary      repository.PrimarySpec
	Catalog      *repository.InMemoryCatalogStore
	Manager      *repository.Manager
	Registry     *repository.Registry
	Store        *store.PostgresStore
	Server       *httptest.Server
	OpClient     *cli.Client
	ReaderClient *cli.Client
}

// setupRepoEnv wires the repository management surface (catalog + real
// Postgres binding store + manager + registry) behind a real
// gateway.Server. The CLI client points at the test server so anything
// the CLI calls in production routes through the same handlers.
func setupRepoEnv(t *testing.T) *repoTestEnv {
	t.Helper()
	env := harness.NewTestEnvironment(t)

	workspaceID := "ws-scenario"
	primary := repository.PrimarySpec{
		Name:          "Scenario Spine",
		DefaultBranch: "main",
		LocalPath:     "/var/spine/workspaces/scenario/spine",
	}
	cat := repository.NewInMemoryCatalogStore(primary)
	mgr := repository.NewManager(workspaceID, primary, cat, env.DB.Store, nil)
	reg := repository.New(workspaceID, primary, func(ctx context.Context) (*repository.Catalog, error) {
		return cat.Load(ctx)
	}, env.DB.Store)

	authSvc := auth.NewService(env.DB.Store)
	ctx := context.Background()
	mustCreateActor(t, env.DB.Store, "op-scenario", domain.RoleOperator)
	mustCreateActor(t, env.DB.Store, "reader-scenario", domain.RoleReader)
	opTok := mustCreateToken(t, authSvc, ctx, "op-scenario")
	readerTok := mustCreateToken(t, authSvc, ctx, "reader-scenario")

	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:             env.DB.Store,
		Auth:              authSvc,
		RepositoryManager: mgr,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return &repoTestEnv{
		WorkspaceID:  workspaceID,
		Primary:      primary,
		Catalog:      cat,
		Manager:      mgr,
		Registry:     reg,
		Store:        env.DB.Store,
		Server:       ts,
		OpClient:     cli.NewClient(ts.URL, opTok),
		ReaderClient: cli.NewClient(ts.URL, readerTok),
	}
}

func mustCreateActor(t *testing.T, st *store.PostgresStore, id string, role domain.ActorRole) {
	t.Helper()
	if err := st.CreateActor(context.Background(), &domain.Actor{
		ActorID: id,
		Type:    domain.ActorTypeHuman,
		Name:    id,
		Role:    role,
		Status:  domain.ActorStatusActive,
	}); err != nil {
		t.Fatalf("create actor %s: %v", id, err)
	}
}

func mustCreateToken(t *testing.T, a *auth.Service, ctx context.Context, actorID string) string {
	t.Helper()
	tok, _, err := a.CreateToken(ctx, actorID, "scenario", nil)
	if err != nil {
		t.Fatalf("create token for %s: %v", actorID, err)
	}
	return tok
}

func decodeRepo(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode repository response: %v (body=%s)", err, data)
	}
	return m
}

func decodeRepoList(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("decode list response: %v (body=%s)", err, data)
	}
	return resp.Items
}

// TestRepositoryScenario_TwoRepoLifecycle covers the headline path:
// register two code repos via the CLI's HTTP client, list and inspect
// them via the API, and confirm the registry resolves each to an
// active joined view. This is the cross-cut TASK-004 unit tests stop
// short of — they fake the binding store so they never observe the
// real INSERT path TASK-002 added.
func TestRepositoryScenario_TwoRepoLifecycle(t *testing.T) {
	env := setupRepoEnv(t)
	ctx := context.Background()

	registerBody := func(id string) map[string]string {
		return map[string]string{
			"id":             id,
			"name":           id,
			"default_branch": "main",
			"role":           "service",
			"clone_url":      "https://example.com/" + id + ".git",
			"local_path":     "/var/spine/workspaces/scenario/repos/" + id,
		}
	}

	for _, id := range []string{"payments", "orders"} {
		if _, err := env.OpClient.Post(ctx, "/api/v1/repositories", registerBody(id)); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	listBody, err := env.ReaderClient.Get(ctx, "/api/v1/repositories", nil)
	if err != nil {
		t.Fatalf("list repositories: %v", err)
	}
	items := decodeRepoList(t, listBody)
	gotIDs := make(map[string]string)
	for _, it := range items {
		gotIDs[it["id"].(string)] = it["status"].(string)
	}
	if gotIDs["spine"] != "active" {
		t.Errorf("primary missing or not active: %v", gotIDs)
	}
	if gotIDs["payments"] != "active" || gotIDs["orders"] != "active" {
		t.Errorf("expected both code repos active, got %v", gotIDs)
	}

	for _, id := range []string{"payments", "orders"} {
		got, err := env.Registry.Lookup(ctx, id)
		if err != nil {
			t.Fatalf("registry lookup %s: %v", id, err)
		}
		if !got.IsActive() {
			t.Errorf("expected %s active in registry, got status=%q", id, got.Status)
		}
		if got.CloneURL != "https://example.com/"+id+".git" {
			t.Errorf("registry view missing binding details for %s: %+v", id, got)
		}
	}

	active, err := env.Registry.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 3 {
		t.Errorf("expected primary+2 in active list, got %d (%+v)", len(active), active)
	}
}

// TestRepositoryScenario_CatalogOnlyEntryUnbound exercises the
// asymmetric write path: governance commits a new catalog entry
// before the binding row exists. The registry must report that
// distinct "registered but unbound" state — both via the typed
// sentinel and the read APIs that surface it.
func TestRepositoryScenario_CatalogOnlyEntryUnbound(t *testing.T) {
	env := setupRepoEnv(t)
	ctx := context.Background()

	if err := env.Catalog.AddEntry(ctx, repository.CatalogEntry{
		ID:            "analytics",
		Kind:          repository.KindCode,
		Name:          "Analytics",
		DefaultBranch: "main",
		Role:          "service",
	}); err != nil {
		t.Fatalf("seed catalog entry: %v", err)
	}

	_, err := env.Registry.Lookup(ctx, "analytics")
	if !errors.Is(err, repository.ErrRepositoryUnbound) {
		t.Fatalf("expected ErrRepositoryUnbound, got %v", err)
	}

	getBody, err := env.OpClient.Get(ctx, "/api/v1/repositories/analytics", nil)
	if err != nil {
		t.Fatalf("get analytics: %v", err)
	}
	got := decodeRepo(t, getBody)
	if got["id"] != "analytics" || got["kind"] != "code" {
		t.Errorf("identity fields missing: %+v", got)
	}
	if status, _ := got["status"].(string); status != "" {
		t.Errorf("expected empty status for unbound entry, got %q", status)
	}
	if clone, _ := got["clone_url"].(string); clone != "" {
		t.Errorf("expected empty clone_url for unbound entry, got %q", clone)
	}

	active, err := env.Registry.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	for _, r := range active {
		if r.ID == "analytics" {
			t.Errorf("unbound entry must not appear in ListActive: %+v", r)
		}
	}
}

// TestRepositoryScenario_DeactivateHidesFromActive confirms the
// soft-deactivate contract end to end: the binding flips inactive,
// the registry refuses execution-time lookups with the inactive
// sentinel, ListActive omits it, but the admin read paths preserve
// history.
func TestRepositoryScenario_DeactivateHidesFromActive(t *testing.T) {
	env := setupRepoEnv(t)
	ctx := context.Background()

	if _, err := env.OpClient.Post(ctx, "/api/v1/repositories", map[string]string{
		"id":             "billing",
		"name":           "Billing",
		"default_branch": "main",
		"clone_url":      "https://example.com/billing.git",
		"local_path":     "/var/spine/workspaces/scenario/repos/billing",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := env.OpClient.Post(ctx, "/api/v1/repositories/billing/deactivate", nil); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	_, err := env.Registry.Lookup(ctx, "billing")
	if !errors.Is(err, repository.ErrRepositoryInactive) {
		t.Fatalf("expected ErrRepositoryInactive, got %v", err)
	}

	active, err := env.Registry.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	for _, r := range active {
		if r.ID == "billing" {
			t.Errorf("deactivated repo must not appear in ListActive: %+v", r)
		}
	}

	getBody, err := env.OpClient.Get(ctx, "/api/v1/repositories/billing", nil)
	if err != nil {
		t.Fatalf("admin get: %v", err)
	}
	got := decodeRepo(t, getBody)
	if got["status"] != "inactive" {
		t.Errorf("expected admin view to show status=inactive, got %v", got["status"])
	}
	if got["clone_url"] == "" {
		t.Errorf("expected clone_url retained for history, got empty")
	}
}

// TestRepositoryScenario_SingleRepoBackwardCompat asserts the
// single-repo workspace contract from EPIC-001 TASK-006: a workspace
// with no catalog file (and no binding rows) still resolves the
// primary "spine" repository, and lookups for any other id return
// not-found rather than crashing.
//
// This scenario gates the migration story: existing v0.x deployments
// boot without producing a /.spine/repositories.yaml and must keep
// working. The test isolates that promise from the multi-repo write
// flows so a regression here cannot hide behind a passing two-repo
// scenario.
func TestRepositoryScenario_SingleRepoBackwardCompat(t *testing.T) {
	env := setupRepoEnv(t)
	ctx := context.Background()

	got, err := env.Registry.Lookup(ctx, repository.PrimaryRepositoryID)
	if err != nil {
		t.Fatalf("primary lookup: %v", err)
	}
	if got.Kind != repository.KindSpine || !got.IsActive() {
		t.Errorf("expected active primary, got %+v", got)
	}
	if got.LocalPath != env.Primary.LocalPath {
		t.Errorf("expected primary LocalPath %q, got %q", env.Primary.LocalPath, got.LocalPath)
	}

	_, err = env.Registry.Lookup(ctx, "missing")
	if !errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Errorf("expected ErrRepositoryNotFound for unknown id, got %v", err)
	}

	all, err := env.Registry.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 || all[0].ID != repository.PrimaryRepositoryID {
		t.Errorf("expected primary-only registry list, got %+v", all)
	}

	active, err := env.Registry.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 || active[0].ID != repository.PrimaryRepositoryID {
		t.Errorf("expected primary-only active list, got %+v", active)
	}

	listBody, err := env.OpClient.Get(ctx, "/api/v1/repositories", nil)
	if err != nil {
		t.Fatalf("list via API: %v", err)
	}
	items := decodeRepoList(t, listBody)
	if len(items) != 1 || items[0]["id"] != repository.PrimaryRepositoryID {
		t.Errorf("expected primary-only API list, got %+v", items)
	}
}

// TestRepositoryScenario_OutOfBandConvergence checks that a catalog
// commit and a binding insert from independent processes (governance
// merge vs platform binding refresh) converge into the same merged
// view through every read surface. This catches consistency drift
// between Manager.Get, Registry.Lookup, and the API list — drift the
// per-store unit tests can't see because each one fakes the other.
func TestRepositoryScenario_OutOfBandConvergence(t *testing.T) {
	env := setupRepoEnv(t)
	ctx := context.Background()

	if err := env.Catalog.AddEntry(ctx, repository.CatalogEntry{
		ID:            "search",
		Kind:          repository.KindCode,
		Name:          "Search",
		DefaultBranch: "trunk",
		Role:          "service",
		Description:   "out-of-band catalog write",
	}); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}

	if err := env.Store.CreateRepositoryBinding(ctx, &store.RepositoryBinding{
		RepositoryID: "search",
		WorkspaceID:  env.WorkspaceID,
		CloneURL:     "ssh://git@example.com/search.git",
		LocalPath:    "/var/spine/workspaces/scenario/repos/search",
		Status:       store.RepositoryBindingStatusActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	resolved, err := env.Registry.Lookup(ctx, "search")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if resolved.Name != "Search" || resolved.DefaultBranch != "trunk" {
		t.Errorf("identity not merged from catalog: %+v", resolved)
	}
	if resolved.CloneURL != "ssh://git@example.com/search.git" {
		t.Errorf("operational details not merged from binding: %+v", resolved)
	}

	mgrView, err := env.Manager.Get(ctx, "search")
	if err != nil {
		t.Fatalf("manager get: %v", err)
	}
	if mgrView.CloneURL != resolved.CloneURL || mgrView.Description != "out-of-band catalog write" {
		t.Errorf("manager view diverges from registry view: mgr=%+v reg=%+v", mgrView, resolved)
	}

	apiBody, err := env.OpClient.Get(ctx, "/api/v1/repositories/search", nil)
	if err != nil {
		t.Fatalf("api get: %v", err)
	}
	got := decodeRepo(t, apiBody)
	if got["default_branch"] != "trunk" || got["status"] != "active" {
		t.Errorf("api view drift: %+v", got)
	}
	if !strings.HasPrefix(got["clone_url"].(string), "ssh://git@example.com/") {
		t.Errorf("api clone_url unexpected: %v", got["clone_url"])
	}
}
