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
	runtimeOpts    []RuntimeOption
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

	repo := NewTestRepo(t)

	if cfg.seedGovernance {
		repo.SeedGovernance(t)
	}
	if cfg.seedWorkflows {
		repo.SeedWorkflows(t)
	}

	db := NewTestDB(t)
	rt := NewTestRuntime(t, repo, db, cfg.runtimeOpts...)

	t.Cleanup(func() {
		db.Cleanup(context.Background(), t)
	})

	return &TestEnvironment{
		Repo:    repo,
		DB:      db,
		Runtime: rt,
	}
}
