package harness

import (
	"testing"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/store"
)

// TestRuntime wires Spine services for in-process scenario execution.
// For the initial spike, only artifact and projection services are wired.
// The orchestrator, validation engine, and other services will be added
// as the scenario testing system evolves.
type TestRuntime struct {
	Store       *store.PostgresStore
	Artifacts   *artifact.Service
	Projections *projection.Service
}

// NewTestRuntime creates a TestRuntime with artifact and projection services
// wired to the given repo and database. Event routers are nil — both services
// handle this gracefully.
func NewTestRuntime(t *testing.T, repo *TestRepo, db *TestDB) *TestRuntime {
	t.Helper()

	return &TestRuntime{
		Store:       db.Store,
		Artifacts:   artifact.NewService(repo.Git, nil, repo.Dir),
		Projections: projection.NewService(repo.Git, db.Store, nil, 1*time.Second),
	}
}
