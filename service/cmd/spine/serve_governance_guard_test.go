package main

// INIT-023 TASK-006: `spine serve` turns both governance-path guards on
// (owner question 4) — SPINE_GOVERNANCE_GUARD parsing (AC 23), both
// orchestrator builders and both projection wirings (AC 30's wiring
// half), and a held merge under the commit-retry sweep logging no Error
// line (AC 35).

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/projection"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/adapters/scheduler"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

func TestParseGovernanceGuard(t *testing.T) {
	for _, c := range []struct {
		raw  string
		on   bool
		warn bool
	}{
		{"", true, false},
		{"off", false, false},
		{" OFF ", false, false},
		{"false", false, false},
		{"0", false, false},
		{"no", false, false},
		{"on", true, false},
		{"true", true, false},
		{"1", true, false},
		{"yes", true, false},
		{"disabled", true, true},
		{"of", true, true},
	} {
		on, warn := parseGovernanceGuard(c.raw)
		if on != c.on || (warn != "") != c.warn {
			t.Errorf("parseGovernanceGuard(%q) = %v, %q; want on=%v warn=%v", c.raw, on, warn, c.on, c.warn)
		}
	}
	t.Setenv(governanceGuardEnv, "junk")
	var buf bytes.Buffer
	logGovernanceGuard(slog.New(slog.NewTextHandler(&buf, nil)), true)
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "stay ON") || !governanceGuardFromEnv() {
		t.Fatalf("junk value: on=%v log=%s", governanceGuardFromEnv(), buf.String())
	}
}

// TestServeWiring_EnablesGovernanceGuards: both orchestrator builders
// enable the engine guard with the projection's workflow directories
// (artifacts_dir "spine" → workflows/ and spine/workflows/), the lifecycle
// exemption and the evidence carve-out; the workspace builder also turns
// on its projection's removal guard; SPINE_GOVERNANCE_GUARD=off leaves all
// of them off.
func TestServeWiring_EnablesGovernanceGuards(t *testing.T) {
	for _, c := range []struct {
		env string
		on  bool
	}{{"", true}, {"off", false}} {
		t.Run("env="+c.env, func(t *testing.T) {
			t.Setenv(governanceGuardEnv, c.env)
			t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
			ctx := context.Background()
			repoPath := testutil.NewTempRepo(t)
			gitClient := git.NewCLIClient(repoPath)
			q := queue.NewMemoryQueue(4)
			events := event.NewQueueRouter(q)
			wantDirs := "workflows/,spine/workflows/"

			st := newTimeoutStore()
			_, wfProvider := buildWorkflowResolver(st, gitClient)
			orch := buildOrchestrator(st, wfProvider, gitClient, events, q, nil, nil, engine.StrictStepEntryPolicy(),
				workflowDirsFor(&config.SpineConfig{ArtifactsDir: "spine"}), observe.Logger(ctx))
			if orch == nil {
				t.Fatal("buildOrchestrator returned nil")
			}
			checkGuard(t, "buildOrchestrator", orch, c.on, wantDirs)

			wsStore := newTimeoutStore()
			repoSpec := repository.PrimarySpec{LocalPath: repoPath}
			reg := repository.New("", repoSpec, func(context.Context) (*repository.Catalog, error) {
				return repository.ParseCatalog(nil, repoSpec)
			}, wsStore)
			pool, err := gitpool.New(gitClient, reg, gitpool.NewCLIClientFactory())
			if err != nil {
				t.Fatal(err)
			}
			projSync := projection.NewService(gitClient, wsStore, events, time.Minute)
			projSync.WithArtifactsDir("spine")
			ss := &workspace.ServiceSet{Store: wsStore, GitClient: gitClient, GitPool: pool, Queue: q, Events: events, ProjSync: projSync}
			if err := workspaceOrchestratorBuilder(ctx, ss, engine.StepEntryPolicy{}); err != nil {
				t.Fatalf("workspaceOrchestratorBuilder: %v", err)
			}
			wsOrch, ok := ss.StepAssigner.(*engine.Orchestrator)
			if !ok {
				t.Fatalf("ss.StepAssigner = %T", ss.StepAssigner)
			}
			checkGuard(t, "workspaceOrchestratorBuilder", wsOrch, c.on, wantDirs)
			if projSync.ProtectionRemovalGuardEnabled() != c.on {
				t.Fatalf("workspace projection removal guard enabled = %v, want %v", projSync.ProtectionRemovalGuardEnabled(), c.on)
			}

			top := projection.NewService(gitClient, wsStore, events, time.Minute)
			enableProtectionRemovalGuard(top, wsStore)
			if top.ProtectionRemovalGuardEnabled() != c.on {
				t.Fatalf("process projection removal guard enabled = %v, want %v", top.ProtectionRemovalGuardEnabled(), c.on)
			}
		})
	}
}

func checkGuard(t *testing.T, where string, orch *engine.Orchestrator, on bool, dirs string) {
	t.Helper()
	cfg, enabled := orch.GovernanceGuardConfig()
	if enabled != on {
		t.Fatalf("%s: governance guard enabled = %v, want %v", where, enabled, on)
	}
	if !on {
		return
	}
	if got := strings.Join(cfg.WorkflowDirs, ","); got != dirs {
		t.Fatalf("%s: WorkflowDirs = %q, want %q", where, got, dirs)
	}
	if !cfg.ExemptWorkflowLifecycleTaskPath || !cfg.ExemptOwnRunEvidence {
		t.Fatalf("%s: exemptions = %+v, want the lifecycle exemption and the evidence carve-out", where, cfg)
	}
}

// committingStore lists one committing run for the commit-retry sweep.
type committingStore struct{ stubStore }

func (committingStore) ListRunsByStatus(_ context.Context, status domain.RunStatus) ([]domain.Run, error) {
	if status == domain.RunStatusCommitting {
		return []domain.Run{{RunID: "run-held", Status: domain.RunStatusCommitting}}, nil
	}
	return nil, nil
}

// TestCommitRetry_HeldMergeLogsNoError is AC 35: under the scheduler's
// commit-retry sweep a held merge (the engine's hold error) logs no Error
// line, while any other failure still does.
func TestCommitRetry_HeldMergeLogsNoError(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	hold := domain.NewErrorWithCause(domain.ErrPrecondition, "governed merge changes 1 governance path(s)", engine.ErrGovernanceApprovalRequired)
	sched := scheduler.New(committingStore{}, nil,
		scheduler.WithCommitRetry(governanceAwareCommitRetry(func(context.Context, string) error { return hold }), 3, 0))
	sched.RunRetryCycle(context.Background())
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("a held merge logged an Error line:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "merge held for an operator governance decision") {
		t.Fatalf("hold not logged at Debug:\n%s", buf.String())
	}

	buf.Reset()
	other := scheduler.New(committingStore{}, nil,
		scheduler.WithCommitRetry(governanceAwareCommitRetry(func(context.Context, string) error { return errors.New("git down") }), 3, 0))
	other.RunRetryCycle(context.Background())
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("a real merge failure no longer logs at Error:\n%s", buf.String())
	}
}
