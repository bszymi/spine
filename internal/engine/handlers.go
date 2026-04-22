package engine

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
)

// EngineMergeActorID is the stable actor identity recorded on step executions
// that the Spine engine advances directly (i.e. type: internal steps routed
// through the merge handler). Auditing and reporting surfaces can match on
// this prefix to distinguish engine-owned advances from runner-submitted ones.
const EngineMergeActorID = "actor-engine-merge"

// InternalHandler is the signature of an engine-owned handler for a workflow
// step whose type is `internal` and execution.mode is `spine_only`. The
// handler is responsible for doing the step's work (e.g. performing the
// authoritative merge) and advancing the step execution to a terminal
// outcome.
type InternalHandler func(ctx context.Context, o *Orchestrator, run *domain.Run, exec *domain.StepExecution, stepDef *domain.StepDefinition) error

// internalHandlers registers the engine-owned handlers by the name that
// appears in a workflow step's `execution.handler` field. The keys here
// must match internal/workflow/handlers.go's KnownInternalHandlers so the
// parser can validate references at workflow-load time without importing
// this package.
var internalHandlers = map[string]InternalHandler{
	"merge": mergeHandler,
}

// LookupInternalHandler resolves a handler registered under name, or
// returns (nil, false) if none is registered. Callers treat a missing
// handler as a fatal configuration error — workflow-load validation
// should have rejected the workflow earlier.
func LookupInternalHandler(name string) (InternalHandler, bool) {
	h, ok := internalHandlers[name]
	return h, ok
}

// mergeHandler is the engine-owned handler for publish-style steps whose
// work is "perform the authoritative merge and advance the step to its
// terminal outcome". The handler delegates to MergeRunBranch, which owns
// the branch-protection check, the commit-status cascade, the merge
// itself, and the push. Advancing the publish step's StepExecution to a
// terminal outcome (published / merge_failed) is folded into the merge
// flow in MergeRunBranch — see advancePublishStep.
func mergeHandler(ctx context.Context, o *Orchestrator, run *domain.Run, exec *domain.StepExecution, stepDef *domain.StepDefinition) error {
	return o.MergeRunBranch(ctx, run.RunID)
}
