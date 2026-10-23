package main

// INIT-024 TASK-008 (audit P3-27, open half): in the shared resolver modes
// (WORKSPACE_RESOLVER=db and platform-binding) `spine serve` builds no
// process-level store, so the single-workspace scheduler of
// buildScheduler is never built either, and nothing fired step and run
// timeouts, failed orphaned runs or retried a committing run's merge. This
// file wires the bounded multi-workspace scheduler (INIT-023 TASK-012)
// over the workspace ServicePool instead, the way MultiProjectionSync is
// wired: built in buildServerConfig, started and stopped by the server
// lifecycle (serve_lifecycle.go). Single (file) mode is unchanged.
//
// Everything here is standalone-only: SMP embeds neither service/cmd/spine
// nor service/internal/workspace and never constructs a MultiScheduler.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/scheduler"
	"github.com/bszymi/spine/service/internal/workspace"
)

// Operator overrides of the shared-mode scheduler's bounds. Unset keeps
// MultiScheduler's defaults (scheduler.DefaultMaxConcurrentWorkspaces, and
// a per-workspace budget of one timeout interval).
const (
	schedulerMaxConcurrentEnv   = "SPINE_SCHEDULER_MAX_CONCURRENT_WORKSPACES"
	schedulerWorkspaceBudgetEnv = "SPINE_SCHEDULER_WORKSPACE_BUDGET"

	// maxSchedulerConcurrentWorkspaces is the largest accepted
	// SPINE_SCHEDULER_MAX_CONCURRENT_WORKSPACES. Each slot is a workspace
	// scan with its own database and git work, so a typo such as 40000 is
	// refused rather than taken as "unbounded".
	maxSchedulerConcurrentWorkspaces = 256
)

// multiSchedulerConfigFromEnv is the shared-mode scheduler configuration:
// the merge retry budget standalone enforces (defaultCommitMaxRetries, the
// single-mode value), the orphan threshold serveCmd parsed from
// SPINE_ORPHAN_THRESHOLD (0 keeps the 30-day default, as in single mode),
// and the two operator overrides. A set but invalid override refuses to
// start (fail closed, and no silent fallback to a value the operator did
// not choose): the value must be a positive integer up to
// maxSchedulerConcurrentWorkspaces, and a positive Go duration.
func multiSchedulerConfigFromEnv(orphanThreshold time.Duration) (scheduler.MultiSchedulerConfig, error) {
	cfg := scheduler.MultiSchedulerConfig{
		OrphanThreshold:  orphanThreshold,
		CommitMaxRetries: defaultCommitMaxRetries,
	}
	if raw, set := os.LookupEnv(schedulerMaxConcurrentEnv); set {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n <= 0 || n > maxSchedulerConcurrentWorkspaces {
			return scheduler.MultiSchedulerConfig{}, fmt.Errorf("%s=%q: want a whole number from 1 to %d",
				schedulerMaxConcurrentEnv, raw, maxSchedulerConcurrentWorkspaces)
		}
		cfg.MaxConcurrentWorkspaces = n
	}
	if raw, set := os.LookupEnv(schedulerWorkspaceBudgetEnv); set {
		d, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil || d <= 0 {
			return scheduler.MultiSchedulerConfig{}, fmt.Errorf("%s=%q: want a positive duration such as 30s",
				schedulerWorkspaceBudgetEnv, raw)
		}
		cfg.WorkspaceBudget = d
	}
	return cfg, nil
}

// buildMultiScheduler is the shared-mode scheduler of `spine serve`, or nil
// in single mode (no ServicePool), whose process-level store keeps the
// single-workspace scheduler of buildScheduler. The overrides are parsed
// in every mode, so a mistyped value never starts silently. buildServerConfig
// calls it beside buildScheduler and hands the result to the lifecycle.
func buildMultiScheduler(deps serveDeps, log *slog.Logger) (*scheduler.MultiScheduler, error) {
	cfg, err := multiSchedulerConfigFromEnv(deps.OrphanThreshold)
	if err != nil {
		return nil, err
	}
	if deps.WSServicePool == nil || deps.WSResolver == nil {
		return nil, nil
	}
	ms := scheduler.NewMultiScheduler(
		&schedulerWorkspacePool{pool: deps.WSServicePool},
		schedulerWorkspaceResolver{resolver: deps.WSResolver},
		cfg,
	)
	eff := ms.Config()
	log.Info("multi-workspace scheduler configured",
		"max_concurrent_workspaces", eff.MaxConcurrentWorkspaces,
		"workspace_budget", eff.WorkspaceBudget,
		"tick_wait", eff.TickWait,
		"commit_max_retries", eff.CommitMaxRetries,
		"orphan_threshold", eff.OrphanThreshold,
	)
	return ms, nil
}

// schedulerWorkspacePool adapts the workspace ServicePool to
// scheduler.WorkspacePool. Every Get that returns without an error holds
// one pool reference, which MultiScheduler releases when that workspace's
// share of the tick ends (also on a panic); a Get that errors holds none.
type schedulerWorkspacePool struct {
	pool *workspace.ServicePool
}

// Get returns the scheduler's view of one workspace's services: its own
// store and event router, and the engine callbacks its orchestrator was
// built with (workspaceOrchestratorBuilder), so every timeout, recovery
// and merge retry runs on that workspace's store, git clients and
// credentials, never another's.
//
// The pool keys its entries by the resolver's canonical id (Config.ID). A
// set whose id is not the one asked for is refused and its reference
// released under the id it was taken under: scheduling one workspace's
// runs with another's services would be a cross-tenant write, and
// releasing under the asked-for id would leak the reference (and with it
// the set, never idle-evicted).
func (p *schedulerWorkspacePool) Get(ctx context.Context, workspaceID string) (scheduler.ServiceSet, error) {
	ss, err := p.pool.Get(ctx, workspaceID)
	if err != nil {
		return scheduler.ServiceSet{}, err
	}
	if ss.Config.ID != workspaceID {
		p.pool.Release(ss.Config.ID)
		return scheduler.ServiceSet{}, fmt.Errorf("workspace pool returned the services of workspace %q for %q; not scheduled",
			ss.Config.ID, workspaceID)
	}
	return schedulerServiceSet(ss), nil
}

// Release drops the reference Get took.
func (p *schedulerWorkspacePool) Release(workspaceID string) {
	p.pool.Release(workspaceID)
}

// schedulerServiceSet maps a pooled ServiceSet to the fields
// scheduler.ServiceSet reads. A set without a store is skipped by the
// scheduler (MultiScheduler.runWorkspace); a nil event router stays a nil
// interface, never a typed nil the scheduler would call through.
func schedulerServiceSet(ss *workspace.ServiceSet) scheduler.ServiceSet {
	out := scheduler.ServiceSet{
		Store:             ss.Store,
		CommitRetryFn:     ss.CommitRetryFn,
		CommitExhaustedFn: ss.CommitExhaustedFn,
		StepRecoveryFn:    ss.StepRecoveryFn,
		RunFailFn:         ss.RunFailFn,
		RunTerminatedFn:   ss.RunTerminatedFn,
	}
	if ss.Events != nil {
		out.Events = ss.Events
	}
	return out
}

// schedulerWorkspaceResolver adapts workspace.Resolver to
// scheduler.WorkspaceResolver.
//
// List is the resolver's own active list: the db provider selects
// status = 'active' (a deactivation through the API also drops its list
// cache, so the next tick no longer sees the workspace), and the
// platform-binding provider lists the bindings it has resolved (a 404
// from the platform drops one). As a second line, anything not marked
// active, or whose id fails workspace.ValidateID, is never handed to the
// pool.
type schedulerWorkspaceResolver struct {
	resolver workspace.Resolver
}

func (r schedulerWorkspaceResolver) List(ctx context.Context) ([]scheduler.WorkspaceRef, error) {
	cfgs, err := r.resolver.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.WorkspaceRef, 0, len(cfgs))
	for i := range cfgs {
		if cfgs[i].Status != workspace.StatusActive || workspace.ValidateID(cfgs[i].ID) != nil {
			continue
		}
		out = append(out, schedulerWorkspaceRef(cfgs[i].ID))
	}
	return out, nil
}

// schedulerWorkspaceRef is a listed workspace id.
type schedulerWorkspaceRef string

func (w schedulerWorkspaceRef) WorkspaceID() string { return string(w) }

// waitMultiScheduler stops the shared-mode scheduler and waits, until ctx
// is done, for the scans it still runs, so serveCmd does not close the
// workspace pool (and every store) under a merge that is still writing.
// A scan that outlives ctx is logged and left to the process exit.
func waitMultiScheduler(ctx context.Context, ms *scheduler.MultiScheduler, log *slog.Logger) {
	if ms == nil {
		return
	}
	ms.Stop()
	if err := ms.Wait(ctx); err != nil {
		log.Warn("multi-workspace scheduler scans still running at shutdown; not waited for", "error", err)
	}
}
