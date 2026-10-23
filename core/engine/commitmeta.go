package engine

import (
	"context"
	"sort"
	"strings"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/observe"
)

// Reserved runs.commit_meta keys (INIT-023 TASK-005). Merge binding keeps
// its state in the run's existing commit_meta column — no schema change —
// under the "spine." prefix, which workflow outcomes may not use: every
// outcome `commit:` map is stripped of the prefix before it is stored
// (stripReserved), with binding on or off. Embedders may read these keys
// for display; only the engine writes them.
const (
	// ReservedCommitMetaPrefix is the reserved namespace. A key is reserved
	// when its lowercased form starts with it.
	ReservedCommitMetaPrefix = "spine."

	// CommitMetaBoundSHA is the commit the governed merge publishes: the
	// run-branch head recorded at the last main-path step completion
	// before publish, or a child of it the engine made itself (the status
	// cascade, a recovery ledger commit). Full lowercase hex.
	CommitMetaBoundSHA = "spine.bound_sha"
	// CommitMetaBoundBy records who bound it, always naming the run:
	// "step:<step_id>:<execution_id>", "convergence:<run_id>:<divergence_id>"
	// or "merge_recovery:<run_id>:<operation>". The merge refuses a
	// binding whose bound_by does not name the run being merged (DC-9).
	CommitMetaBoundBy = "spine.bound_by"
	// CommitMetaHeadPrefix + <step_id> is the head at that step's latest
	// completion. Audit and tests only; never read for decisions.
	CommitMetaHeadPrefix = "spine.head."
	// CommitMetaRepoPrefix + <repo_id> + CommitMetaRepoBoundSuffix is a code
	// repository's bound head (P3-37).
	CommitMetaRepoPrefix      = "spine.repo."
	CommitMetaRepoBoundSuffix = ".bound_sha"
)

// RepoBoundSHAKey returns the commit_meta key holding repoID's bound head.
func RepoBoundSHAKey(repoID string) string {
	return CommitMetaRepoPrefix + repoID + CommitMetaRepoBoundSuffix
}

// isReservedCommitMetaKey reports whether k is in the reserved namespace,
// in any letter case ("Spine.Bound_By" is reserved too).
func isReservedCommitMetaKey(k string) bool {
	return strings.HasPrefix(strings.ToLower(k), ReservedCommitMetaPrefix)
}

// isBoundObjectID reports whether s is a full lowercase hex object id,
// the only form a binding is stored and compared in.
func isBoundObjectID(s string) bool { return git.IsCanonicalObjectName(s) }

// stripReserved returns a copy of m without any reserved key, and the
// sorted list of keys it dropped. It never modifies m, which may be the
// cached workflow definition's outcome map. A nil or empty m gives nil.
func stripReserved(m map[string]string) (map[string]string, []string) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	var dropped []string
	for k, v := range m {
		if isReservedCommitMetaKey(k) {
			dropped = append(dropped, k)
			continue
		}
		out[k] = v
	}
	sort.Strings(dropped)
	return out, dropped
}

// reservedOnly returns a copy of m's reserved keys.
func reservedOnly(m map[string]string) map[string]string {
	out := make(map[string]string)
	for k, v := range m {
		if isReservedCommitMetaKey(k) {
			out[k] = v
		}
	}
	return out
}

// headBinding is what one recorded capture writes: the bound head, who
// bound it, the per-step audit key and the code repositories' bound heads.
// A capture is the run's complete binding, so it replaces every bound key
// (primary and code repositories); the spine.head.* audit keys of other
// steps are kept.
type headBinding struct {
	head      string            // primary run-branch head (spine.bound_sha)
	by        string            // spine.bound_by
	stepID    string            // spine.head.<stepID>; empty for engine rebinds
	repoHeads map[string]string // code repo id → bound head
	// replaceRepoKeys drops every existing spine.repo.* key before
	// writing repoHeads. Set by step captures (a capture covers every
	// affected code repository) and by a convergence rebind that had no
	// trusted earlier binding (any repo key present was not written by
	// this run).
	replaceRepoKeys bool
	// extra holds further reserved keys written in the same commit_meta
	// write as the binding — today one spine.evidence.<execution_id> key
	// per step capture (INIT-023 TASK-008, step-output evidence). Every key
	// must be reserved (apply drops any other): they ride the binding's
	// single write instead of a second whole-map replace, and the keys of
	// earlier executions are kept like every reserved key.
	extra map[string]string
}

// apply returns reserved with b written over it.
func (b *headBinding) apply(reserved map[string]string) map[string]string {
	out := make(map[string]string, len(reserved)+3+len(b.repoHeads))
	for k, v := range reserved {
		if b.replaceRepoKeys && strings.HasPrefix(strings.ToLower(k), CommitMetaRepoPrefix) {
			continue
		}
		out[k] = v
	}
	if b.head != "" {
		out[CommitMetaBoundSHA] = b.head
		out[CommitMetaBoundBy] = b.by
	}
	if b.stepID != "" && b.head != "" {
		out[CommitMetaHeadPrefix+b.stepID] = b.head
	}
	for id, h := range b.repoHeads {
		out[RepoBoundSHAKey(id)] = h
	}
	for k, v := range b.extra {
		if isReservedCommitMetaKey(k) {
			out[k] = v
		}
	}
	return out
}

// mergeCommitMeta builds the commit_meta a main-path step completion
// stores, and whether to store it. It never modifies current or
// outcomeCommit and always returns a new map.
//
//   - Binding off (bindingOn false): stripReserved(outcomeCommit), written
//     only when the outcome has a commit — today's single write, minus
//     reserved keys. Reserved keys already stored are dropped with the
//     rest of the old map (replace semantics), so turning binding off
//     retires them at the next commit-carrying outcome — except the
//     planning starter record (CommitMetaGovernanceStartedBy), which is
//     not binding state: separation of duties reads it for the whole run
//     (runAuthor), and dropping it at a draft outcome's commit would leave
//     the run reviewable by nobody (INIT-024 TASK-001). It exists only
//     while separation of duties or the governance guard wrote it at start
//     (recordPlanningStarter), so without either this is unchanged.
//   - Binding on: the non-reserved part is stripReserved(outcomeCommit)
//     when the outcome has a commit (replace semantics, as today: earlier
//     non-reserved keys are dropped) or current's non-reserved keys when it
//     has none; the reserved part is current's reserved keys with hb
//     written over them. It is written when the outcome has a commit or hb
//     is set.
func mergeCommitMeta(current, outcomeCommit map[string]string, hasCommit, bindingOn bool, hb *headBinding) (map[string]string, bool) {
	stripped, _ := stripReserved(outcomeCommit)
	if !bindingOn {
		if !hasCommit {
			return nil, false
		}
		if stripped == nil {
			stripped = map[string]string{}
		}
		if starter := current[CommitMetaGovernanceStartedBy]; starter != "" {
			stripped[CommitMetaGovernanceStartedBy] = starter
		}
		return stripped, true
	}
	if !hasCommit && hb == nil {
		return nil, false
	}
	out := make(map[string]string)
	if hasCommit {
		for k, v := range stripped {
			out[k] = v
		}
	} else {
		for k, v := range current {
			if !isReservedCommitMetaKey(k) {
				out[k] = v
			}
		}
	}
	reserved := reservedOnly(current)
	if hb != nil {
		reserved = hb.apply(reserved)
	}
	for k, v := range reserved {
		out[k] = v
	}
	return out, true
}

// withEngineBinding returns a copy of current (every key kept) with b
// written over its reserved part. Used by the engine's own rebinds
// (convergence, recovery ledger), which change no workflow metadata.
func withEngineBinding(current map[string]string, b *headBinding) map[string]string {
	out := make(map[string]string, len(current)+3)
	for k, v := range current {
		if !isReservedCommitMetaKey(k) {
			out[k] = v
		}
	}
	for k, v := range b.apply(reservedOnly(current)) {
		out[k] = v
	}
	return out
}

// logStrippedReserved warns once per completion that a workflow outcome
// declared reserved commit keys, which are never stored.
func logStrippedReserved(ctx context.Context, workflowID, stepID, outcomeID string, outcomeCommit map[string]string) {
	if _, dropped := stripReserved(outcomeCommit); len(dropped) > 0 {
		observe.Logger(ctx).Warn("workflow outcome declares reserved commit keys; they are not stored",
			"workflow_id", workflowID, "step_id", stepID, "outcome_id", outcomeID,
			"keys", dropped)
	}
}

// boundByStep, boundByConvergence and boundByRecovery build the
// spine.bound_by values. Each names the run, so a value cannot be planted
// in advance by a workflow file (run ids derive from a random trace id).
func boundByStep(stepID, executionID string) string {
	return "step:" + stepID + ":" + executionID
}

func boundByConvergence(runID, divergenceID string) string {
	return "convergence:" + runID + ":" + divergenceID
}

func boundByRecovery(runID, operation string) string {
	return "merge_recovery:" + runID + ":" + operation
}

// boundByNamesRun reports whether a spine.bound_by value is well formed
// and names runID (DC-9). A step binding names the run through its
// execution id, which always starts with "<run_id>-" (StartRun,
// advanceToNextStep, tryConvergence and RetryStep all build it that way).
// A step id may itself contain ":" (workflow validation allows it), so
// the execution id is found by its "<run_id>-" prefix, not by the first
// colon (codex review, round 1): "step:build:lint:<run>-build:lint-1"
// names the run. Run ids contain no ":" ("run-" plus trace-id hex).
func boundByNamesRun(by, runID string) bool {
	if runID == "" || by == "" {
		return false
	}
	kind, rest, ok := strings.Cut(by, ":")
	if !ok {
		return false
	}
	switch kind {
	case "step":
		i := strings.Index(rest, ":"+runID+"-")
		if i <= 0 {
			return false // no execution id of this run, or no step id
		}
		execID := rest[i+1:]
		return len(execID) > len(runID)+1
	case "convergence", "merge_recovery":
		id, tail, ok := strings.Cut(rest, ":")
		return ok && id == runID && tail != ""
	default:
		return false
	}
}

// trustedBinding returns the run's bound head and bound_by when the
// binding is well formed and was written by this run, and ok=false
// otherwise. present reports whether any spine.bound_sha is stored at
// all (an untrusted value is present but not ok).
func trustedBinding(meta map[string]string, runID string) (sha, by string, ok, present bool) {
	sha = meta[CommitMetaBoundSHA]
	by = meta[CommitMetaBoundBy]
	present = sha != "" || by != ""
	ok = isBoundObjectID(sha) && boundByNamesRun(by, runID)
	return sha, by, ok, present
}
