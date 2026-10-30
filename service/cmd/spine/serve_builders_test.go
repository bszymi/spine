package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/core/refadmit"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

// builderStepDefs is a refadmit.StepDefLookup stand-in for the
// orchestrator.
type builderStepDefs struct{}

func (builderStepDefs) LookupStepDef(context.Context, string, string) (*domain.StepDefinition, *domain.Run) {
	return nil, nil
}

// builderResolver resolves one workspace with no database.
type builderResolver struct{ cfg workspace.Config }

func (r builderResolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	if id != r.cfg.ID {
		return nil, workspace.ErrWorkspaceNotFound
	}
	c := r.cfg
	return &c, nil
}

func (r builderResolver) List(context.Context) ([]workspace.Config, error) {
	return []workspace.Config{r.cfg}, nil
}

func assertPermissive(t *testing.T, p branchprotect.Policy) {
	t.Helper()
	if p == nil {
		t.Fatal("policy is nil, want a permissive policy")
	}
	dec, _, err := p.Evaluate(context.Background(), branchprotect.Request{Branch: "refs/heads/main", Kind: branchprotect.OpDirectWrite})
	if err != nil || dec != branchprotect.DecisionAllow {
		t.Errorf("policy on main: %v %v, want a permissive allow", dec, err)
	}
}

// TestBuildGitPushResolver_RunReaderWiring is INIT-023 TASK-016 AC 14:
// the push resolver hands the ref-class admission the store it already
// uses, and a true nil (operator-only) when there is none.
func TestBuildGitPushResolver_RunReaderWiring(t *testing.T) {
	ctx := context.Background()
	defs := builderStepDefs{}

	t.Run("single mode with a store", func(t *testing.T) {
		st := &stubStore{}
		res, release, err := buildGitPushResolver(nil, st, nil, defs)(ctx, "ws")
		defer release()
		if err != nil {
			t.Fatal(err)
		}
		if res.Runs != refadmit.RunReader(st) {
			t.Errorf("Runs = %#v, want the fallback store", res.Runs)
		}
		if res.StepDefs != refadmit.StepDefLookup(defs) {
			t.Errorf("StepDefs = %#v, want the fallback lookup", res.StepDefs)
		}
	})

	t.Run("single mode without a store", func(t *testing.T) {
		res, release, err := buildGitPushResolver(nil, nil, nil, nil)(ctx, "ws")
		defer release()
		if err != nil {
			t.Fatal(err)
		}
		if res.Runs != nil {
			t.Errorf("Runs = %#v, want a nil interface", res.Runs)
		}
		if res.StepDefs != nil {
			t.Errorf("StepDefs = %#v, want nil", res.StepDefs)
		}
		assertPermissive(t, res.Policy)
	})

	t.Run("shared mode, workspace without a database", func(t *testing.T) {
		pool := workspace.NewServicePool(ctx, builderResolver{cfg: workspace.Config{
			ID: "ws-1", RepoPath: t.TempDir(), Status: workspace.StatusActive,
		}}, workspace.PoolConfig{IdleCheckInterval: -1})
		defer pool.Close()
		res, release, err := buildGitPushResolver(pool, &stubStore{}, nil, defs)(ctx, "ws-1")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if res.Runs != nil || res.StepDefs != nil {
			t.Errorf("Runs = %#v, StepDefs = %#v; want both nil (never the process-level fallback)", res.Runs, res.StepDefs)
		}
		assertPermissive(t, res.Policy)
	})

	t.Run("shared mode, workspace store and engine", func(t *testing.T) {
		wsStore := &stubStore{}
		pool := workspace.NewServicePool(ctx, builderResolver{cfg: workspace.Config{
			ID: "ws-2", RepoPath: t.TempDir(), Status: workspace.StatusActive,
		}}, workspace.PoolConfig{
			IdleCheckInterval: -1,
			Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
				ss.Store = wsStore
				ss.StepAssigner = defs
				return nil
			},
		})
		defer pool.Close()
		res, release, err := buildGitPushResolver(pool, &stubStore{}, nil, nil)(ctx, "ws-2")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if res.Runs != refadmit.RunReader(wsStore) {
			t.Errorf("Runs = %#v, want the workspace's own store", res.Runs)
		}
		if res.StepDefs != refadmit.StepDefLookup(defs) {
			t.Errorf("StepDefs = %#v, want the workspace's step assigner", res.StepDefs)
		}
	})
}

func TestStepDefLookupOrNil(t *testing.T) {
	var typedNil *engine.Orchestrator
	for name, v := range map[string]any{"nil": nil, "typed nil orchestrator": typedNil, "not a lookup": 42} {
		if got := stepDefLookupOrNil(v); got != nil {
			t.Errorf("%s: got %#v, want a nil interface", name, got)
		}
	}
	if got := stepDefLookupOrNil(builderStepDefs{}); got == nil {
		t.Error("a lookup: got nil")
	}
	if got := runReaderOrNil(nil); got != nil {
		t.Errorf("runReaderOrNil(nil) = %#v, want a nil interface", got)
	}
}

// TestBuildGitHTTPHandler_WarnsWhenRunBranchesAreOperatorOnly is AC 14's
// startup warning: with receive-pack on and no run store, run-branch
// pushes are operator-only, and the operator is told so. In shared mode
// the pair is resolved per workspace on each push, so a nil fallback
// pair is expected and is not reported (review round 1: it warned on
// every shared-mode start).
func TestBuildGitHTTPHandler_WarnsWhenRunBranchesAreOperatorOnly(t *testing.T) {
	resolver := builderResolver{cfg: workspace.Config{ID: "ws", RepoPath: t.TempDir(), Status: workspace.StatusActive}}
	build := func(runs refadmit.RunReader, defs refadmit.StepDefLookup, perWorkspace, receivePack bool) string {
		t.Helper()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		if h := buildGitHTTPHandler(resolver, nil, receivePack, branchprotect.NewPermissive(), runs, defs, perWorkspace, gitHTTPLimits{}, log); h == nil {
			t.Skip("git-http-backend unavailable")
		}
		return buf.String()
	}

	out := build(nil, builderStepDefs{}, false, true)
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "run-branch pushes are operator-only: no run store wired") {
		t.Errorf("nil reader with receive-pack on: want the operator-only warning, got:\n%s", out)
	}
	out = build(&stubStore{}, nil, false, true)
	if !strings.Contains(out, "run-branch pushes are operator-only: no step-definition lookup wired") {
		t.Errorf("nil step-definition lookup: want the operator-only warning, got:\n%s", out)
	}
	if out := build(&stubStore{}, builderStepDefs{}, false, true); strings.Contains(out, "operator-only") {
		t.Errorf("fully wired: unexpected warning:\n%s", out)
	}
	if out := build(nil, nil, false, false); strings.Contains(out, "operator-only") {
		t.Errorf("receive-pack off: unexpected warning:\n%s", out)
	}
	out = build(nil, nil, true, true)
	if strings.Contains(out, "operator-only: no") || !strings.Contains(out, "resolves each workspace's run store and engine per push") {
		t.Errorf("shared mode: want the per-workspace Info line and no fallback warning, got:\n%s", out)
	}
}

// TestBuildServerConfig_GitPushResolverWiresAdmission drives the real
// `spine serve` wiring (buildServerConfig, single mode, as the smoke
// test does) and asks the installed push resolver for its admission
// pair: the process store as the run reader and the orchestrator as the
// review-freeze lookup. Review round 1: passing nil instead of the
// lookup to buildGitPushResolver left every package test green while
// making every non-operator run, plan and divergence push refused
// (resolver_unavailable), with no startup warning. Mutation check:
// that nil fails this test.
func TestBuildServerConfig_GitPushResolverWiresAdmission(t *testing.T) {
	repoPath := testutil.NewTempRepo(t)
	q := queue.NewMemoryQueue(16)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go q.Start(ctx)
	st := stubStore{}
	rt, err := buildServerConfig(ctx, serveDeps{
		Store:      st,
		RepoPath:   repoPath,
		SpineCfg:   &config.SpineConfig{ArtifactsDir: "/"},
		GitClient:  git.NewCLIClient(repoPath),
		Queue:      q,
		Events:     event.NewQueueRouter(q),
		DevMode:    true,
		RuntimeEnv: "development",
	})
	if err != nil {
		t.Fatalf("buildServerConfig: %v", err)
	}
	if rt.Config.GitPushResolver == nil {
		t.Fatal("no git push resolver installed")
	}
	if rt.Config.StepAssigner == nil {
		t.Fatal("fixture: no orchestrator built (StepAssigner is nil)")
	}
	res, release, err := rt.Config.GitPushResolver(ctx, "")
	defer release()
	if err != nil {
		t.Fatal(err)
	}
	if res.Runs != refadmit.RunReader(st) {
		t.Errorf("Runs = %#v, want the process store", res.Runs)
	}
	if res.StepDefs == nil {
		t.Fatal("StepDefs is nil: every non-operator run-branch push would be refused")
	}
	if any(res.StepDefs) != any(rt.Config.StepAssigner) {
		t.Errorf("StepDefs = %T %p, want the orchestrator the gateway uses as its step assigner", res.StepDefs, res.StepDefs)
	}
}
