package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/bszymi/spine/core/engine"
)

// stepEntryPolicyEnv selects the engine step-entry policy `spine serve`
// installs on every orchestrator it builds (INIT-023 TASK-007).
const stepEntryPolicyEnv = "SPINE_STEP_ENTRY_POLICY"

// stepEntryPolicyFromEnv reads SPINE_STEP_ENTRY_POLICY:
//
//   - unset, "" or "strict" → engine.StrictStepEntryPolicy(): run-status
//     guards, actor eligibility, execution-mode table, strict preconditions,
//     required_skills on assign, separation of duties, and termination of
//     live steps when a run ends. This is the standalone default (Owner
//     question 2).
//   - "legacy" → the zero value: the pre-INIT-023 behaviour, an escape hatch
//     for operators migrating custom workflows. serve logs a WARN at startup.
//   - anything else → error; serve refuses to start rather than guess.
//
// The value is matched case-insensitively after trimming whitespace.
// Validated once in serveCmd; the result is threaded to both orchestrator
// builders (process-level and per-workspace) so they cannot disagree.
func stepEntryPolicyFromEnv() (engine.StepEntryPolicy, error) {
	raw := os.Getenv(stepEntryPolicyEnv)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "strict":
		return engine.StrictStepEntryPolicy(), nil
	case "legacy":
		return engine.StepEntryPolicy{}, nil
	default:
		return engine.StepEntryPolicy{}, fmt.Errorf("%s=%q is not supported (expected strict or legacy)", stepEntryPolicyEnv, raw)
	}
}

// logStepEntryPolicy records the selected policy at startup. The legacy
// escape hatch is a WARN naming what stays open, so it stands out in logs.
func logStepEntryPolicy(log *slog.Logger, p engine.StepEntryPolicy) {
	if p.IsZero() {
		log.Warn(stepEntryPolicyEnv+"=legacy: engine step-entry checks are OFF — assign and claim do not check run status, actor eligibility, execution mode, preconditions or separation of duties, and a cancelled or failed run keeps live step executions (INIT-023 audit P3-3, P3-4, P3-15, P3-16 remain open). Use only while migrating custom workflows; the escape hatch will be removed.",
			"env", stepEntryPolicyEnv)
		return
	}
	log.Info("engine step-entry policy: strict", "env", stepEntryPolicyEnv)
}
