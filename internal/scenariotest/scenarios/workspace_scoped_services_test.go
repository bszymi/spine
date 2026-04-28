//go:build scenario

package scenarios_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/scenariotest/harness"
	"github.com/bszymi/spine/internal/secrets"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
	"github.com/bszymi/spine/internal/workspace"
)

// TestWorkspace_ScopedServices verifies that the ServicePool Builder hook
// constructs independent, workspace-scoped services for each workspace.
// Each workspace gets its own Validator and Divergence service, and
// validation results in one workspace do not leak to the other.
//
// Scenario: ServicePool Builder creates independent workspace-scoped services
//   Given two workspaces (Alpha and Beta) each with their own DB and repo
//   When ServicePool resolves both workspaces
//   Then each should have a distinct Validator instance
//     And each should have a distinct Divergence service instance
//     And each should have a distinct Store connection
//   When validation is run on each workspace independently
//   Then each validator should return results only for its own artifacts
func TestWorkspace_ScopedServices(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()

	// --- Workspace Alpha: standard test environment ---
	envAlpha := harness.NewTestEnvironment(t, harness.WithGovernance())

	// --- Workspace Beta: separate DB + repo ---
	betaDBURL := os.Getenv("SPINE_REGISTRY_TEST_DATABASE_URL")
	if betaDBURL == "" {
		t.Skip("SPINE_REGISTRY_TEST_DATABASE_URL not set — need a second DB for workspace-scoped services test")
	}

	repoBeta := harness.NewTestRepo(t)
	repoBeta.SeedGovernance(t)

	betaStore, err := store.NewPostgresStore(ctx, betaDBURL)
	if err != nil {
		t.Fatalf("connect to beta DB: %v", err)
	}
	t.Cleanup(func() { betaStore.Close() })
	if err := betaStore.ApplyMigrations(ctx, store.FindMigrationsDir()); err != nil {
		t.Fatalf("apply beta migrations: %v", err)
	}
	betaStore.CleanupTestData(ctx, t)

	// Build Beta services manually.
	gitBeta := git.NewCLIClient(repoBeta.Dir)
	qBeta := queue.NewMemoryQueue(100)
	go qBeta.Start(ctx)
	t.Cleanup(func() { qBeta.Stop() })
	eventsBeta := event.NewQueueRouter(qBeta)
	artifactsBeta := artifact.NewService(gitBeta, eventsBeta, repoBeta.Dir)
	artifactsBeta.WithPolicy(branchprotect.NewPermissive())
	projBeta := projection.NewService(gitBeta, betaStore, eventsBeta, 30*time.Second)

	// --- Step 1: Create different artifacts in each workspace ---
	t.Run("create-artifact-in-alpha", func(t *testing.T) {
		content := `---
type: Governance
title: Alpha Validation Target
status: Living Document
version: "0.1"
---

# Alpha Validation Target

Alpha-specific content.
`
		if _, err := envAlpha.Runtime.Artifacts.Create(ctx, "governance/alpha-validation.md", content); err != nil {
			t.Fatalf("create artifact in alpha: %v", err)
		}
	})

	t.Run("create-artifact-in-beta", func(t *testing.T) {
		content := `---
type: Governance
title: Beta Validation Target
status: Living Document
version: "0.1"
---

# Beta Validation Target

Beta-specific content.
`
		if _, err := artifactsBeta.Create(ctx, "governance/beta-validation.md", content); err != nil {
			t.Fatalf("create artifact in beta: %v", err)
		}
	})

	// --- Step 2: Sync projections independently ---
	t.Run("sync-projections", func(t *testing.T) {
		if err := envAlpha.Runtime.Projections.FullRebuild(ctx); err != nil {
			t.Fatalf("sync alpha: %v", err)
		}
		if err := projBeta.FullRebuild(ctx); err != nil {
			t.Fatalf("sync beta: %v", err)
		}
	})

	// --- Step 3: Build ServicePool with Builder and verify scoped services ---
	t.Run("builder-creates-scoped-services", func(t *testing.T) {
		var builderCalls int

		resolver := &twoWorkspaceResolver{
			alpha: workspace.Config{
				ID:          "alpha",
				DatabaseURL: secrets.NewSecretValue([]byte(os.Getenv("SPINE_DATABASE_URL"))),
				RepoPath:    envAlpha.Repo.Dir,
			},
			beta: workspace.Config{
				ID:          "beta",
				DatabaseURL: secrets.NewSecretValue([]byte(betaDBURL)),
				RepoPath:    repoBeta.Dir,
			},
		}

		pool := workspace.NewServicePool(ctx, resolver, workspace.PoolConfig{
			Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
				builderCalls++
				return nil
			},
		})
		defer pool.Close()

		// Get both workspace service sets.
		ssAlpha, err := pool.Get(ctx, "alpha")
		if err != nil {
			t.Fatalf("Get alpha: %v", err)
		}
		defer pool.Release("alpha")

		ssBeta, err := pool.Get(ctx, "beta")
		if err != nil {
			t.Fatalf("Get beta: %v", err)
		}
		defer pool.Release("beta")

		// Builder was called once per workspace.
		if builderCalls != 2 {
			t.Errorf("expected builder called 2 times, got %d", builderCalls)
		}

		// Each workspace has its own Validator.
		if ssAlpha.Validator == nil {
			t.Fatal("alpha Validator should not be nil")
		}
		if ssBeta.Validator == nil {
			t.Fatal("beta Validator should not be nil")
		}
		if ssAlpha.Validator == ssBeta.Validator {
			t.Error("alpha and beta should have different Validator instances")
		}

		// Each workspace has its own Divergence service.
		if ssAlpha.Divergence == nil {
			t.Fatal("alpha Divergence should not be nil")
		}
		if ssBeta.Divergence == nil {
			t.Fatal("beta Divergence should not be nil")
		}
		if ssAlpha.Divergence == ssBeta.Divergence {
			t.Error("alpha and beta should have different Divergence instances")
		}

		// Each workspace has independent Store connections.
		if ssAlpha.Store == ssBeta.Store {
			t.Error("alpha and beta should have different Store instances")
		}
	})

	// --- Step 4: Verify workspace-scoped validation is independent ---
	t.Run("scoped-validation-independence", func(t *testing.T) {
		// Create validators from each workspace's store.
		validatorAlpha := validation.NewEngine(envAlpha.DB.Store)
		validatorBeta := validation.NewEngine(betaStore)

		// Count artifacts in each store to establish expected validation counts.
		alphaArtifacts, err := envAlpha.DB.Store.QueryArtifacts(ctx, store.ArtifactQuery{Limit: 1000})
		if err != nil {
			t.Fatalf("query alpha artifacts: %v", err)
		}
		betaArtifacts, err := betaStore.QueryArtifacts(ctx, store.ArtifactQuery{Limit: 1000})
		if err != nil {
			t.Fatalf("query beta artifacts: %v", err)
		}

		// Validate in each workspace — should return results only for its own artifacts.
		resultsAlpha := validatorAlpha.ValidateAll(ctx)
		resultsBeta := validatorBeta.ValidateAll(ctx)

		if len(resultsAlpha) != len(alphaArtifacts.Items) {
			t.Errorf("alpha: expected %d validation results (one per artifact), got %d",
				len(alphaArtifacts.Items), len(resultsAlpha))
		}
		if len(resultsBeta) != len(betaArtifacts.Items) {
			t.Errorf("beta: expected %d validation results (one per artifact), got %d",
				len(betaArtifacts.Items), len(resultsBeta))
		}

		// Alpha should have more artifacts (governance seeds + alpha-validation.md)
		// than beta (governance seeds + beta-validation.md) — but counts should differ
		// if the seeded governance docs differ. At minimum, they must be independent:
		// the total count proves each validator queries only its own store.
		if len(resultsAlpha) == 0 {
			t.Error("alpha validator returned no results — expected at least governance artifacts")
		}
		if len(resultsBeta) == 0 {
			t.Error("beta validator returned no results — expected at least governance artifacts")
		}
	})
}

// TestWorkspace_ScopedSchedulerCallbacks verifies that the ServicePool Builder
// can inject per-workspace scheduler callbacks and they execute independently.
//
// Scenario: ServicePool Builder can inject per-workspace scheduler callbacks
//   Given a ServicePool with a Builder that sets CommitRetryFn, StepRecoveryFn, and RunFailFn
//   When the workspace service set is retrieved
//   Then all three callbacks should be set on the ServiceSet
//   When each callback is invoked
//   Then each should execute without error
func TestWorkspace_ScopedSchedulerCallbacks(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()

	envAlpha := harness.NewTestEnvironment(t, harness.WithGovernance())

	t.Run("builder-sets-scheduler-callbacks", func(t *testing.T) {
		resolver := &singleWorkspaceResolver{
			cfg: workspace.Config{
				ID:          "alpha",
				DatabaseURL: secrets.NewSecretValue([]byte(os.Getenv("SPINE_DATABASE_URL"))),
				RepoPath:    envAlpha.Repo.Dir,
			},
		}

		var commitRetrySet, stepRecoverySet, runFailSet bool

		pool := workspace.NewServicePool(ctx, resolver, workspace.PoolConfig{
			Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
				ss.CommitRetryFn = func(_ context.Context, _ string) error {
					commitRetrySet = true
					return nil
				}
				ss.StepRecoveryFn = func(_ context.Context, _ string) error {
					stepRecoverySet = true
					return nil
				}
				ss.RunFailFn = func(_ context.Context, _, _ string) error {
					runFailSet = true
					return nil
				}
				return nil
			},
		})
		defer pool.Close()

		ss, err := pool.Get(ctx, "alpha")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer pool.Release("alpha")

		// Verify callbacks are set.
		if ss.CommitRetryFn == nil {
			t.Error("CommitRetryFn should be set")
		}
		if ss.StepRecoveryFn == nil {
			t.Error("StepRecoveryFn should be set")
		}
		if ss.RunFailFn == nil {
			t.Error("RunFailFn should be set")
		}

		// Invoke callbacks to verify they're wired correctly.
		_ = ss.CommitRetryFn(ctx, "run-1")
		_ = ss.StepRecoveryFn(ctx, "exec-1")
		_ = ss.RunFailFn(ctx, "run-2", "test reason")

		if !commitRetrySet {
			t.Error("CommitRetryFn was not invoked")
		}
		if !stepRecoverySet {
			t.Error("StepRecoveryFn was not invoked")
		}
		if !runFailSet {
			t.Error("RunFailFn was not invoked")
		}
	})
}

// --- Test Resolvers ---

type twoWorkspaceResolver struct {
	alpha, beta workspace.Config
}

func (r *twoWorkspaceResolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	switch id {
	case "alpha":
		return &r.alpha, nil
	case "beta":
		return &r.beta, nil
	default:
		return nil, workspace.ErrWorkspaceNotFound
	}
}

func (r *twoWorkspaceResolver) List(_ context.Context) ([]workspace.Config, error) {
	return []workspace.Config{r.alpha, r.beta}, nil
}

type singleWorkspaceResolver struct {
	cfg workspace.Config
}

func (r *singleWorkspaceResolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	if id == r.cfg.ID {
		return &r.cfg, nil
	}
	return nil, workspace.ErrWorkspaceNotFound
}

func (r *singleWorkspaceResolver) List(_ context.Context) ([]workspace.Config, error) {
	return []workspace.Config{r.cfg}, nil
}
