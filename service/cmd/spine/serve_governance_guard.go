package main

// Governance-path guards in `spine serve` (ADR-016; INIT-023 TASK-006,
// owner question 4). The embedded library's zero value keeps the v1
// behaviour; the standalone server turns both guards on — the merge guard
// on every orchestrator it builds and the protection-removal guard on
// every projection service — and SPINE_GOVERNANCE_GUARD=off restores v1.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"

	"github.com/bszymi/spine/adapters/projection"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/service/internal/config"
)

// governanceGuardEnv switches both governance-path guards.
const governanceGuardEnv = "SPINE_GOVERNANCE_GUARD"

// parseGovernanceGuard reads the switch value: unset or empty is on; off,
// false, 0 and no (any case, surrounding spaces ignored) are off; on,
// true, 1 and yes are on; anything else is on, with a warning naming the
// value — an unrecognised value must never turn a security control off.
func parseGovernanceGuard(raw string) (on bool, warning string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return true, ""
	case "off", "false", "0", "no":
		return false, ""
	case "on", "true", "1", "yes":
		return true, ""
	default:
		return true, governanceGuardEnv + " has an unrecognised value; the governance-path guards stay ON (set off, false, 0 or no to disable them)"
	}
}

// governanceGuardFromEnv is the process-wide switch. Both orchestrator
// builders and both projection wirings read it, so they cannot disagree;
// serve logs it once at startup (logGovernanceGuard).
func governanceGuardFromEnv() bool {
	on, _ := parseGovernanceGuard(os.Getenv(governanceGuardEnv))
	return on
}

// logGovernanceGuard records the selection at startup. Off is a Warn
// naming what stays open; on without event delivery is a Warn too: honoured
// override pushes are then not persisted to runtime.event_log, so they
// cannot vouch for a protection removal (a relaxation needs a governed run
// with an operator approval).
func logGovernanceGuard(log *slog.Logger, eventDelivery bool) {
	raw := os.Getenv(governanceGuardEnv)
	on, warning := parseGovernanceGuard(raw)
	if warning != "" {
		log.Warn(warning, "env", governanceGuardEnv, "value", raw)
	}
	if !on {
		log.Warn(governanceGuardEnv+"=off: governance-path guards are OFF — a governed merge may change .spine/, .spine.yaml and workflow definitions without an operator approval, and the projection installs any branch-protection config that reaches main (audit F3 stays open)",
			"env", governanceGuardEnv)
		return
	}
	log.Info("governance-path guards: on (governance changes on governed merges need an operator approval; protection removals need an operator record)",
		"env", governanceGuardEnv)
	if !eventDelivery {
		log.Warn("governance-path guards: event delivery is off (SPINE_EVENT_DELIVERY), so honoured override pushes are not recorded in runtime.event_log and cannot authorise a branch-protection relaxation; relax protection through a governed run with an operator approval, or enable event delivery",
			"env", governanceGuardEnv)
	}
}

// standaloneGovernanceGuard is the engine guard `spine serve` installs:
// the default guarded paths plus the projection's workflow directories,
// the ADR-008 lifecycle exemption (owner question 3: on in spine serve)
// and the own-run evidence carve-out (owner question 9).
func standaloneGovernanceGuard(workflowDirs []string) engine.GovernanceGuard {
	return engine.GovernanceGuard{
		Enabled:                         governanceGuardFromEnv(),
		WorkflowDirs:                    append([]string(nil), workflowDirs...),
		ExemptWorkflowLifecycleTaskPath: true,
		ExemptOwnRunEvidence:            true,
	}
}

// enableGovernanceGuard configures orch's guard. Call it after
// EnableMergeBinding (the guard requires binding).
func enableGovernanceGuard(orch *engine.Orchestrator, workflowDirs []string) error {
	return orch.WithGovernanceGuard(standaloneGovernanceGuard(workflowDirs))
}

// workflowDirsFor is the projection's workflow-directory set for a spine
// config (nil: the repository root).
func workflowDirsFor(cfg *config.SpineConfig) []string {
	if cfg == nil {
		return projection.WorkflowDirs("")
	}
	return projection.WorkflowDirs(cfg.ArtifactsDir)
}

// enableProtectionRemovalGuard configures a projection service's guard.
// The provenance reader is the runtime store when it can list governance
// authorizations (*store.PostgresStore can); without one every removal is
// refused (fail closed). Events go to the service's own router.
func enableProtectionRemovalGuard(svc *projection.Service, st store.Store) {
	if svc == nil {
		return
	}
	g := projection.ProtectionRemovalGuard{Enabled: governanceGuardFromEnv()}
	if r, ok := st.(projection.GovernanceProvenanceReader); ok {
		g.Provenance = r
	}
	svc.WithProtectionRemovalGuard(g)
}

// governanceAwareCommitRetry wraps a commit-retry function for the
// scheduler's committing-run sweep: a merge held for an operator decision
// is waiting, not failing, so it is reported at Debug and the sweep sees
// success — otherwise every tick would log the hold at Error. The hold
// stays visible on the step's error_detail and the governance.merge_held
// event.
func governanceAwareCommitRetry(fn func(ctx context.Context, runID string) error) func(ctx context.Context, runID string) error {
	return func(ctx context.Context, runID string) error {
		err := fn(ctx, runID)
		if errors.Is(err, engine.ErrGovernanceApprovalRequired) {
			observe.Logger(ctx).Debug("commit retry: merge held for an operator governance decision",
				"run_id", runID)
			return nil
		}
		return err
	}
}
