package main

// Governance-path guards in `spine serve` (ADR-016; INIT-023 TASK-006,
// owner question 4). The embedded library's zero value keeps the v1
// behaviour; the standalone server turns both guards on — the merge guard
// on every orchestrator it builds and the protection-removal guard on
// every projection service — and SPINE_GOVERNANCE_GUARD=off restores v1.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"

	"github.com/bszymi/spine/adapters/projection"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
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
// naming what stays open.
func logGovernanceGuard(log *slog.Logger) {
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

// eventLogWriter is the store surface the push path's provenance write
// needs (store.DeliveryStore has it).
type eventLogWriter interface {
	WriteEventLog(ctx context.Context, entry *store.EventLogEntry) error
}

// overrideProvenanceEmitter writes every honoured
// branch_protection.override event to runtime.event_log synchronously, then
// forwards it. The projection's protection-removal guard accepts an
// override push as provenance only through that row, and the standalone
// event router delivers asynchronously (and writes event_log only when
// event delivery is on): without this, a sync that ran between the push
// landing and the delivery subscriber's write refused the authorised
// relaxation and advanced past it for good (codex review round 1). The
// git pre-receive gate emits the event before handing the push to git, so
// the row exists before the ref moves. The delivery subscriber's later
// write of the same event id is a no-op (ON CONFLICT DO NOTHING). A failed
// write is logged and the push proceeds: the relaxation is then refused
// until an operator re-affirms it (fail-safe).
type overrideProvenanceEmitter struct {
	inner event.Emitter
	log   eventLogWriter
}

func (e *overrideProvenanceEmitter) Emit(ctx context.Context, evt domain.Event) error {
	if evt.Type == domain.EventBranchProtectionOverride {
		payload, err := json.Marshal(evt)
		if err == nil {
			err = e.log.WriteEventLog(ctx, &store.EventLogEntry{
				EventID: evt.EventID, EventType: string(evt.Type), Payload: payload, CreatedAt: evt.Timestamp,
			})
		}
		if err != nil {
			observe.Logger(ctx).Warn("governance-path guard: override provenance not recorded; a branch-protection relaxation this push carries will be refused until an operator re-affirms it",
				"event_id", evt.EventID, "error", err)
		}
	}
	if e.inner == nil {
		return nil
	}
	return e.inner.Emit(ctx, evt)
}

// withOverrideProvenance wraps the git push path's emitter while the
// governance-path guards are on and the workspace has a store that can
// write the event log; otherwise it returns inner unchanged.
func withOverrideProvenance(inner event.Emitter, st store.Store) event.Emitter {
	if !governanceGuardFromEnv() || st == nil {
		return inner
	}
	w, ok := st.(eventLogWriter)
	if !ok {
		return inner
	}
	return &overrideProvenanceEmitter{inner: inner, log: w}
}
