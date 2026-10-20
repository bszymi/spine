package harness

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/projection"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/core/validation"
	"github.com/bszymi/spine/core/workflow"
)

// TestRuntime wires Spine services for in-process scenario execution.
// Services are constructed identically to production (cmd/spine/main.go),
// ensuring test fidelity. The runtime is configured for synchronous,
// deterministic behaviour — no background polling or async event delivery.
type TestRuntime struct {
	Store        *store.PostgresStore
	Artifacts    *artifact.Service
	Workflows    *workflow.Service
	Projections  *projection.Service
	Validator    *validation.Engine
	Events       event.EventRouter
	Queue        *queue.MemoryQueue
	Orchestrator *engine.Orchestrator
	// Clock is the controllable time source for scenarios that want
	// to drive scheduler-tick side effects deterministically. It is
	// seeded to wall-clock at construction so timestamps written by
	// production code remain comparable. Scenarios that don't touch
	// time can ignore it; per TASK-028 only callers that explicitly
	// pass Clock.Now into a scheduler observe the advance.
	Clock *Clock
}

// RuntimeOption configures optional components of the TestRuntime.
type RuntimeOption func(*runtimeConfig)

type runtimeConfig struct {
	withEvents          bool
	withValidation      bool
	withOrchestrator    bool
	withoutMergeBinding bool
	withStatusLifecycle bool
	// governanceGuard turns on both governance-path guards (INIT-023
	// TASK-006); governanceArtifactsDir is the artifacts_dir whose
	// workflows/ directory the engine guard also covers.
	governanceGuard        bool
	governanceArtifactsDir string
}

// WithEvents enables the event system (MemoryQueue + QueueRouter).
// When enabled, artifact and projection services receive a real event
// router instead of nil. The queue runs in a background goroutine
// and is stopped automatically when the test ends.
func WithEvents() RuntimeOption {
	return func(c *runtimeConfig) {
		c.withEvents = true
	}
}

// WithValidation enables the cross-artifact validation engine.
func WithValidation() RuntimeOption {
	return func(c *runtimeConfig) {
		c.withValidation = true
	}
}

// WithOrchestrator enables the workflow engine orchestrator.
// Implies WithEvents (the orchestrator needs event emission).
func WithOrchestrator() RuntimeOption {
	return func(c *runtimeConfig) {
		c.withOrchestrator = true
		c.withEvents = true
	}
}

// WithoutMergeBinding builds the orchestrator WITHOUT SHA-bound governed
// merges (INIT-023 TASK-005). The harness enables binding by default,
// mirroring `spine serve`; a scenario that must commit to the run branch
// during a gate step (which binding refuses, owner question 4) opts out
// here and states its reason in a comment at the call site.
func WithoutMergeBinding() RuntimeOption {
	return func(c *runtimeConfig) {
		c.withoutMergeBinding = true
	}
}

// WithDeclaredStatusLifecycle turns on the declared artifact-status
// lifecycle (INIT-023 TASK-017) the way `spine serve` does: on the
// orchestrator's governed merge, on the workflow service (commit.status at
// create/update/validate) and, as blocking SC-002/SC-003, on the
// validator. It implies WithOrchestrator and WithValidation, and needs
// merge binding (the harness default): combining it with
// WithoutMergeBinding fails the test, because every merge would refuse
// with the configuration error.
func WithDeclaredStatusLifecycle() RuntimeOption {
	return func(c *runtimeConfig) {
		c.withStatusLifecycle = true
		c.withValidation = true
		c.withOrchestrator = true
		c.withEvents = true
	}
}

// WithGovernanceGuard turns on both governance-path guards (ADR-016;
// INIT-023 TASK-006), as `spine serve` does: the orchestrator holds a
// governed merge that changes governance paths until an operator decides,
// and the projection refuses an unprovenanced branch-protection removal.
// artifactsDir is the environment's artifacts_dir ("" for the root), so
// <artifacts_dir>/workflows/ is guarded too. Off by default, so existing
// scenarios are unchanged. Requires merge binding (the default).
func WithGovernanceGuard(artifactsDir string) RuntimeOption {
	return func(c *runtimeConfig) {
		c.governanceGuard = true
		c.governanceArtifactsDir = artifactsDir
	}
}

// NewTestRuntime creates a TestRuntime wired to the given repo and database.
// By default, event routers are nil and validation is disabled.
// Use WithEvents(), WithValidation(), and WithOrchestrator() to enable
// optional components.
func NewTestRuntime(t *testing.T, repo *TestRepo, db *TestDB, opts ...RuntimeOption) *TestRuntime {
	t.Helper()

	cfg := &runtimeConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	rt := &TestRuntime{
		Store: db.Store,
		Clock: NewClock(time.Now()),
	}

	var eventRouter event.EventRouter
	if cfg.withEvents {
		q := queue.NewMemoryQueue(100)
		ctx, cancel := context.WithCancel(context.Background())
		go q.Start(ctx)
		t.Cleanup(func() {
			q.Stop()
			cancel()
		})
		rt.Queue = q
		rt.Events = event.NewQueueRouter(q)
		eventRouter = rt.Events
	}

	rt.Artifacts = artifact.NewService(repo.Git, eventRouter, repo.Dir)
	// Most scenarios do not exercise branch protection and their repos
	// are not seeded with the 018 projection rows — use a permissive
	// policy by default so the Service is policy-wired (ADR-009 §3 is not
	// bypassed) without blocking writes to main. Scenarios that do want
	// to exercise the guard install a projection-backed policy explicitly.
	rt.Artifacts.WithPolicy(branchprotect.NewPermissive())
	rt.Workflows = workflow.NewService(repo.Git, repo.Dir)
	if cfg.withStatusLifecycle {
		if cfg.withoutMergeBinding {
			t.Fatal("harness: the status lifecycle requires merge binding; drop WithoutMergeBinding")
		}
		rt.Workflows.WithStatusLifecycle(domain.DefaultStatusLifecycle())
	}
	rt.Projections = projection.NewService(repo.Git, db.Store, eventRouter, 1*time.Second)
	// Production standalone wiring pins the projection to the
	// governed-merge target (INIT-023 TASK-011); harness repositories
	// are created on main, so scenarios run with the same option.
	if err := rt.Projections.WithAuthoritativeBranch(engine.AuthoritativeBranch); err != nil {
		t.Fatalf("projection authoritative branch: %v", err)
	}
	if cfg.governanceGuard {
		rt.Projections.WithProtectionRemovalGuard(projection.ProtectionRemovalGuard{Enabled: true, Provenance: db.Store})
	}

	if cfg.withValidation {
		var vopts []validation.Option
		if cfg.withStatusLifecycle {
			vopts = append(vopts, validation.WithBlockingStatusConflicts(validation.DefaultBlockingStatusConflicts...))
		}
		rt.Validator = validation.NewEngine(db.Store, vopts...)
	}

	if cfg.withOrchestrator {
		wfProvider := workflow.NewProjectionProviderFromListFn(func(ctx context.Context) ([]workflow.WorkflowProjection, error) {
			projs, err := db.Store.ListActiveWorkflowProjections(ctx)
			if err != nil {
				return nil, err
			}
			result := make([]workflow.WorkflowProjection, len(projs))
			for i := range projs {
				result[i] = workflow.WorkflowProjection{
					WorkflowPath: projs[i].WorkflowPath,
					WorkflowID:   projs[i].WorkflowID,
					Name:         projs[i].Name,
					Version:      projs[i].Version,
					Status:       projs[i].Status,
					AppliesTo:    projs[i].AppliesTo,
					Definition:   projs[i].Definition,
					SourceCommit: projs[i].SourceCommit,
				}
			}
			return result, nil
		})
		resolver := engine.NewBindingResolver(wfProvider, repo.Git)
		wfLoader := engine.NewGitWorkflowLoader(repo.Git)

		orch, err := engine.New(
			resolver,
			db.Store,
			&noopActorAssigner{},
			rt.Artifacts,
			eventRouter,
			repo.Git,
			wfLoader,
		)
		if err != nil {
			t.Fatalf("create orchestrator: %v", err)
		}
		if rt.Validator != nil {
			orch.WithValidator(rt.Validator)
		}
		orch.WithArtifactWriter(rt.Artifacts)
		orch.WithWorkflowWriter(rt.Workflows)
		orch.WithBlockingStore(db.Store)
		orch.WithAssignmentStore(db.Store)
		// Scenarios do not exercise branch-protection denial paths on
		// MergeRunBranch — OpGovernedMerge is allowed unconditionally —
		// so a permissive policy matches the scenario harness's
		// intent: "route through the guard, but do not block".
		orch.WithBranchProtectPolicy(branchprotect.NewPermissive())
		// SHA-bound governed merges are on by default, as in `spine
		// serve` (INIT-023 TASK-005); WithoutMergeBinding opts out.
		if !cfg.withoutMergeBinding {
			if err := orch.EnableMergeBinding(engine.MergeBindingConfig{}); err != nil {
				t.Fatalf("enable merge binding: %v", err)
			}
		}
		if cfg.withStatusLifecycle {
			orch.WithStatusLifecycle(domain.DefaultStatusLifecycle())
		}
		if cfg.governanceGuard {
			if err := orch.WithGovernanceGuard(engine.GovernanceGuard{
				Enabled:                         true,
				WorkflowDirs:                    projection.WorkflowDirs(cfg.governanceArtifactsDir),
				ExemptWorkflowLifecycleTaskPath: true,
				ExemptOwnRunEvidence:            true,
			}); err != nil {
				t.Fatalf("enable governance guard: %v", err)
			}
		}
		rt.Orchestrator = orch
	}

	return rt
}

// noopActorAssigner is a no-op implementation of engine.ActorAssigner
// for scenario testing. It accepts assignments without delivering them,
// allowing scenarios to manually submit step results.
type noopActorAssigner struct{}

func (n *noopActorAssigner) DeliverAssignment(_ context.Context, _ actor.AssignmentRequest) error {
	return nil
}

func (n *noopActorAssigner) ProcessResult(_ context.Context, _ actor.AssignmentRequest, _ actor.AssignmentResult) error {
	return nil
}
