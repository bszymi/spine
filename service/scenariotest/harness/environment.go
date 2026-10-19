package harness

import (
	"context"
	"testing"
)

// TestEnvironment is a complete, ready-to-use Spine test environment
// composed of a Git repository, database, and runtime services.
// Create one with NewTestEnvironment.
type TestEnvironment struct {
	Repo    *TestRepo
	DB      *TestDB
	Runtime *TestRuntime
}

// EnvOption configures the test environment.
type EnvOption func(*envConfig)

type envConfig struct {
	seedGovernance bool
	seedWorkflows  bool
	artifactsDir   string // empty means default "/"
	runtimeOpts    []RuntimeOption
	noHomeBranch   bool
}

// WithoutHomeBranch builds the repository's client without
// git.WithHomeBranch("main") (INIT-023 TASK-012), so its working tree is
// never restored and "HEAD" reads follow the checked-out branch. For a
// scenario that must observe a non-restored tree; every use must say why
// in a comment. Also accepted by NewTestRepo.
func WithoutHomeBranch() EnvOption {
	return func(c *envConfig) {
		c.noHomeBranch = true
	}
}

// WithGovernance seeds the repository with governance documents.
func WithGovernance() EnvOption {
	return func(c *envConfig) {
		c.seedGovernance = true
	}
}

// WithWorkflows seeds the repository with workflow definitions.
// Implies WithGovernance (governance must be seeded first to create
// the directory structure).
func WithWorkflows() EnvOption {
	return func(c *envConfig) {
		c.seedGovernance = true
		c.seedWorkflows = true
	}
}

// WithRuntimeEvents enables the event system in the runtime.
func WithRuntimeEvents() EnvOption {
	return func(c *envConfig) {
		c.runtimeOpts = append(c.runtimeOpts, WithEvents())
	}
}

// WithRuntimeValidation enables the validation engine in the runtime.
func WithRuntimeValidation() EnvOption {
	return func(c *envConfig) {
		c.runtimeOpts = append(c.runtimeOpts, WithValidation())
	}
}

// WithRuntimeOrchestrator enables the workflow engine orchestrator in the runtime.
func WithRuntimeOrchestrator() EnvOption {
	return func(c *envConfig) {
		c.runtimeOpts = append(c.runtimeOpts, WithOrchestrator())
	}
}

// WithRuntimeWithoutMergeBinding builds the runtime's orchestrator without
// SHA-bound governed merges (see WithoutMergeBinding). Every use must say
// why in a comment.
func WithRuntimeWithoutMergeBinding() EnvOption {
	return func(c *envConfig) {
		c.runtimeOpts = append(c.runtimeOpts, WithoutMergeBinding())
	}
}

// WithStatusLifecycle builds the runtime with the declared artifact-status
// lifecycle on (INIT-023 TASK-017; see WithDeclaredStatusLifecycle): the
// orchestrator, the workflow service and the validator, together with the
// merge binding the lifecycle requires (on by default). Off by default, so
// existing scenarios are unchanged.
func WithStatusLifecycle() EnvOption {
	return func(c *envConfig) {
		c.runtimeOpts = append(c.runtimeOpts, WithDeclaredStatusLifecycle())
	}
}

// WithArtifactsDir configures the artifacts directory for the test environment.
// Seeds and artifact service use this directory instead of the repo root.
func WithArtifactsDir(dir string) EnvOption {
	return func(c *envConfig) {
		c.artifactsDir = dir
	}
}

// Seeded returns options that seed the repository with governance documents
// and workflow definitions. Use WithRuntimeEvents() and WithRuntimeValidation()
// additionally if the scenario needs event delivery or cross-artifact validation.
func Seeded() []EnvOption {
	return []EnvOption{
		WithGovernance(),
		WithWorkflows(),
	}
}

// NewTestEnvironment creates a complete test environment with coordinated
// setup and teardown. All components are wired together and cleaned up
// automatically when the test ends.
//
// Usage:
//
//	env := harness.NewTestEnvironment(t, harness.Seeded()...)
//	// env.Repo, env.DB, env.Runtime are ready to use
func NewTestEnvironment(t *testing.T, opts ...EnvOption) *TestEnvironment {
	t.Helper()

	cfg := &envConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var repoOpts []EnvOption
	if cfg.noHomeBranch {
		repoOpts = append(repoOpts, WithoutHomeBranch())
	}
	repo := NewTestRepo(t, repoOpts...)

	if cfg.seedGovernance {
		repo.SeedGovernance(t)
	}
	if cfg.seedWorkflows {
		repo.SeedWorkflows(t)
	}

	db := NewTestDB(t)
	rt := NewTestRuntime(t, repo, db, cfg.runtimeOpts...)

	// Configure artifacts directory if specified.
	if cfg.artifactsDir != "" {
		rt.Artifacts.WithArtifactsDir(cfg.artifactsDir)
		// The governed merge's status lifecycle resolves run.TaskPath
		// under the same directory (INIT-023 TASK-017); without a
		// lifecycle the setting is inert.
		if rt.Orchestrator != nil {
			rt.Orchestrator.WithArtifactsDir(cfg.artifactsDir)
		}
	}

	t.Cleanup(func() {
		db.Cleanup(context.Background(), t)
	})

	return &TestEnvironment{
		Repo:    repo,
		DB:      db,
		Runtime: rt,
	}
}
