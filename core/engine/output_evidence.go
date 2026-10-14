package engine

// Step-output evidence (INIT-023 TASK-008; audit P3-19).
//
// The defect. A workflow step's `required_outputs` are self-attested
// labels: validateRequiredOutputs only checks that the submitter LISTED
// each declared name in artifacts_produced, and every shipped value
// (`deliverable`, `artifact_content`, `findings`, …) is a label, not a
// path (KNOWN-LIMITATIONS "Step outputs are self-attested labels"). A step
// that declared outputs could be submitted with nothing pushed, and the
// audit trail could not tell it apart from real work: step_completed was
// emitted with a nil payload, and a rework attempt's event reused the first
// attempt's id, so the event log's ON CONFLICT (event_id) DO NOTHING
// dropped it.
//
// What this records (evidence, not enforcement — Decision D4, owner
// question 1: flag only, review stays the control):
//
//   - step_completed carries the documented base fields plus the
//     execution, the declared and the attested outputs, and branch_change:
//     whether the run branch moved between the previous recorded head and
//     this submit's head. The event id is per execution, so rework attempts
//     are no longer deduplicated away; a crash-recovery re-emission of the
//     same execution keeps its id and stays idempotent.
//   - required_outputs_unchanged is true when the step declared outputs and
//     the branch is provably unchanged; the engine also logs a Warn.
//   - With merge binding on, the same branch_change is persisted for run
//     detail as the reserved commit_meta key spine.evidence.<execution_id>
//     (owner question 5), in the single commit_meta write the binding
//     already makes (headBinding.extra → mergeCommitMeta). It therefore
//     survives event-log retention, needs no schema change, and a workflow
//     cannot forge it (stripReserved drops every spine.* outcome key).
//
// Evidence follows TASK-005's head capture (Decision D12): the head is the
// headBinding captureHead returned, the previous head is the run's trusted
// binding as read before this submit's write (spine.bound_sha /
// spine.bound_by), falling back to the run branch's baseline. With binding
// off (and no expected_head_sha) nothing is captured, so every entry is
// unknown/head_sha_unrecorded, no git call is made and nothing is
// persisted. Evidence never blocks a submission: a failed or slow diff
// degrades to unknown/diff_failed:<kind>; the raw git error is logged,
// never put in the event.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// BranchChange is what the engine can say about the run branch between the
// previous recorded head and the head recorded for one step execution.
// Primary repository only (v1).
type BranchChange struct {
	RepositoryID string `json:"repository_id"`
	// Status is one of the BranchChange* constants.
	Status string `json:"status"`
	// HeadSHA is the head TASK-005's capture recorded for this execution.
	HeadSHA string `json:"head_sha,omitempty"`
	// PreviousSHA / PreviousSource: the run's previous trusted binding and
	// its spine.bound_by verbatim ("step:<step_id>:<execution_id>",
	// "convergence:…", "merge_recovery:…"), or the run branch's baseline
	// with source "baseline".
	PreviousSHA    string `json:"previous_sha,omitempty"`
	PreviousSource string `json:"previous_source,omitempty"`
	// ChangedPaths is 0 when unchanged, the diff's entry count when
	// changed, and absent otherwise.
	ChangedPaths *int `json:"changed_paths,omitempty"`
	// Reason explains unknown and not_applicable (EvidenceReason*), or is
	// "diff_failed:<kind>" with kind a git.GitErrorKind, "timeout",
	// "canceled" or "error".
	Reason string `json:"reason,omitempty"`
}

// BranchChange.Status values.
const (
	BranchChangeUnchanged     = "unchanged"
	BranchChangeChanged       = "changed"
	BranchChangeUnknown       = "unknown"
	BranchChangeNotApplicable = "not_applicable"
)

// BranchChange.Reason values (besides "diff_failed:<kind>").
const (
	EvidenceReasonHeadUnrecorded   = "head_sha_unrecorded"
	EvidenceReasonNoPreviousSHA    = "no_previous_sha"
	EvidenceReasonDivergenceBranch = "divergence_branch"
	EvidenceReasonNoRunBranch      = "no_run_branch"
	EvidenceReasonRecovered        = "recovered"
	EvidenceReasonDiffFailedPrefix = "diff_failed:"
)

// EvidencePreviousSourceBaseline is BranchChange.PreviousSource when the
// comparison point is the run branch's baseline (the tip it was cut from).
const EvidencePreviousSourceBaseline = "baseline"

// CommitMetaEvidencePrefix + <execution_id> is the reserved commit_meta key
// holding one execution's evidence for run detail (a compact JSON
// stepEvidenceRecord). Inside TASK-005's reserved "spine." namespace.
const CommitMetaEvidencePrefix = "spine.evidence."

// StepOutputEvidence is one completed primary-branch execution's evidence,
// as StepOutputEvidence (run detail) returns it.
type StepOutputEvidence struct {
	ExecutionID              string       `json:"execution_id"`
	StepID                   string       `json:"step_id"`
	Attempt                  int          `json:"attempt"`
	ActorID                  string       `json:"actor_id,omitempty"`
	OutcomeID                string       `json:"outcome_id"`
	RequiredOutputs          []string     `json:"required_outputs,omitempty"`
	BranchChange             BranchChange `json:"branch_change"`
	RequiredOutputsUnchanged bool         `json:"required_outputs_unchanged"`
}

// stepEvidenceRecord is the persisted value of a spine.evidence.* key.
type stepEvidenceRecord struct {
	BranchChange             BranchChange `json:"branch_change"`
	RequiredOutputsUnchanged bool         `json:"required_outputs_unchanged"`
}

// evidenceDiffTimeout bounds the one diff a submit may run. The diff runs
// on the submit path (in SMP, inside the per-execution lock whose request
// budget is 10 s), so it is short: a timeout costs only the count
// (unknown/diff_failed:timeout); the unchanged signal needs no git call.
// A variable only so tests can shorten it.
var evidenceDiffTimeout = 3 * time.Second

// Bounds on the client-supplied artifacts_produced list as the event
// carries it; artifacts_produced_count is always exact.
const (
	maxEventArtifacts      = 32
	maxEventArtifactLength = 512
)

func intPtr(n int) *int { return &n }

// branchChangeAtSubmit derives the evidence for a submit whose capture was
// hb (nil when nothing was captured). run is SubmitStepResult's own read,
// taken before this submit's commit_meta write, so its binding is the
// previous one. It makes at most one git call (the diff), and none when
// the head is unrecorded or equal to the previous head.
func (o *Orchestrator) branchChangeAtSubmit(ctx context.Context, run *domain.Run, exec *domain.StepExecution, hb *headBinding) BranchChange {
	bc := BranchChange{RepositoryID: domain.PrimaryRepositoryID}
	switch {
	case exec.BranchID != "":
		bc.Status, bc.Reason = BranchChangeNotApplicable, EvidenceReasonDivergenceBranch
		return bc
	case run.BranchName == "":
		bc.Status, bc.Reason = BranchChangeNotApplicable, EvidenceReasonNoRunBranch
		return bc
	case hb == nil || hb.head == "":
		bc.Status, bc.Reason = BranchChangeUnknown, EvidenceReasonHeadUnrecorded
		return bc
	}
	bc.HeadSHA = hb.head

	if prev, by, ok, _ := trustedBinding(run.CommitMeta, run.RunID); ok && by == boundByStep(exec.StepID, exec.ExecutionID) {
		// The run's binding is this execution's own capture: an earlier
		// attempt of this submit recorded it (and this evidence) but its
		// terminal write failed, so the execution is still open (codex
		// review, round 3). Comparing with it would call every change
		// "unchanged"; keep the comparison point that attempt recorded.
		rec, found := storedEvidence(run, exec.ExecutionID)
		if !found || rec.BranchChange.PreviousSHA == "" {
			bc.Status, bc.Reason = BranchChangeUnknown, EvidenceReasonNoPreviousSHA
			return bc
		}
		bc.PreviousSHA, bc.PreviousSource = rec.BranchChange.PreviousSHA, rec.BranchChange.PreviousSource
	} else if ok {
		bc.PreviousSHA, bc.PreviousSource = prev, by
	} else if base := run.RepositoryBaselines[domain.PrimaryRepositoryID]; isBoundObjectID(base) {
		// A binding this run did not record is never a comparison point
		// (as for the merge, DC-9); the baseline is.
		bc.PreviousSHA, bc.PreviousSource = base, EvidencePreviousSourceBaseline
	} else {
		bc.Status, bc.Reason = BranchChangeUnknown, EvidenceReasonNoPreviousSHA
		return bc
	}

	if bc.HeadSHA == bc.PreviousSHA {
		bc.Status, bc.ChangedPaths = BranchChangeUnchanged, intPtr(0)
		return bc
	}

	diffCtx, cancel := context.WithTimeout(ctx, evidenceDiffTimeout)
	defer cancel()
	diffs, err := o.git.Diff(diffCtx, bc.PreviousSHA, bc.HeadSHA)
	if err != nil {
		kind := diffFailureKind(diffCtx, err)
		observe.Logger(ctx).Warn("step output evidence: diff failed; branch change unknown",
			"run_id", run.RunID, "execution_id", exec.ExecutionID,
			"previous_sha", bc.PreviousSHA, "head_sha", bc.HeadSHA, "kind", kind, "error", err)
		bc.Status, bc.Reason = BranchChangeUnknown, EvidenceReasonDiffFailedPrefix+kind
		return bc
	}
	bc.Status, bc.ChangedPaths = BranchChangeChanged, intPtr(len(diffs))
	return bc
}

// storedEvidence decodes the run's persisted evidence for executionID.
func storedEvidence(run *domain.Run, executionID string) (stepEvidenceRecord, bool) {
	raw, ok := run.CommitMeta[CommitMetaEvidencePrefix+executionID]
	if !ok {
		return stepEvidenceRecord{}, false
	}
	var rec stepEvidenceRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return stepEvidenceRecord{}, false
	}
	return rec, true
}

// diffFailureKind classifies a failed evidence diff without its text.
func diffFailureKind(diffCtx context.Context, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(diffCtx.Err(), context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var gerr *git.GitError
	if errors.As(err, &gerr) && gerr.Kind != "" {
		return string(gerr.Kind)
	}
	return "error"
}

// requiredOutputsUnchanged is the P3-19 flag: the step declared outputs and
// the branch is provably unchanged. Logged at Warn when true.
func requiredOutputsUnchanged(ctx context.Context, stepDef *domain.StepDefinition, exec *domain.StepExecution, bc BranchChange) bool {
	flag := len(stepDef.RequiredOutputs) > 0 && bc.Status == BranchChangeUnchanged
	if flag {
		observe.Logger(ctx).Warn("required outputs declared but run branch unchanged",
			"run_id", exec.RunID, "step_id", exec.StepID, "execution_id", exec.ExecutionID,
			"actor_id", exec.ActorID, "required_outputs", stepDef.RequiredOutputs, "head_sha", bc.HeadSHA,
			"previous_source", bc.PreviousSource)
	}
	return flag
}

// evidenceMetaEntry returns the reserved commit_meta key and value
// persisting one execution's evidence.
func evidenceMetaEntry(executionID string, bc BranchChange, unchanged bool) (string, string, error) {
	raw, err := json.Marshal(stepEvidenceRecord{BranchChange: bc, RequiredOutputsUnchanged: unchanged})
	if err != nil {
		return "", "", err
	}
	return CommitMetaEvidencePrefix + executionID, string(raw), nil
}

// stepCompletedEventID is step_completed's event id: per execution
// (Decision D8), so every rework attempt is recorded, and stable for one
// execution, so a crash-recovery re-emission is deduplicated against the
// original (the event log keeps the first copy).
func stepCompletedEventID(run *domain.Run, exec *domain.StepExecution) string {
	return fmt.Sprintf("evt-%s-%s-completed", run.TraceID[:12], exec.ExecutionID)
}

// stepCompletedPayload is the step_completed payload
// (architecture/event-schemas.md §4.2). The first block is the documented
// base; the rest is INIT-023 TASK-008's evidence. The engine-owned publish
// step carries the base fields only.
type stepCompletedPayload struct {
	StepID     string  `json:"step_id"`
	StepName   string  `json:"step_name"`
	Outcome    string  `json:"outcome"`
	NextStep   string  `json:"next_step"`
	Attempt    int     `json:"attempt"`
	DurationMS *int64  `json:"duration_ms,omitempty"`
	HasCommit  bool    `json:"has_commit"`
	BranchID   *string `json:"branch_id"`

	ExecutionID              string        `json:"execution_id"`
	ActorID                  string        `json:"actor_id,omitempty"`
	RequiredOutputs          []string      `json:"required_outputs,omitempty"`
	ArtifactsProduced        []string      `json:"artifacts_produced,omitempty"`
	ArtifactsProducedCount   *int          `json:"artifacts_produced_count,omitempty"`
	BranchChange             *BranchChange `json:"branch_change,omitempty"`
	RequiredOutputsUnchanged *bool         `json:"required_outputs_unchanged,omitempty"`
}

// baseStepCompletedPayload fills the documented base fields.
func baseStepCompletedPayload(stepDef *domain.StepDefinition, outcome *domain.OutcomeDefinition, exec *domain.StepExecution, completedAt time.Time) stepCompletedPayload {
	next := outcome.NextStep
	if next == "" {
		next = "end"
	}
	p := stepCompletedPayload{
		StepID:      exec.StepID,
		StepName:    stepDef.Name,
		Outcome:     outcome.ID,
		NextStep:    next,
		Attempt:     exec.Attempt,
		HasCommit:   len(outcome.Commit) > 0,
		ExecutionID: exec.ExecutionID,
		ActorID:     exec.ActorID,
	}
	if exec.StartedAt != nil {
		d := completedAt.Sub(*exec.StartedAt).Milliseconds()
		p.DurationMS = &d
	}
	if exec.BranchID != "" {
		b := exec.BranchID
		p.BranchID = &b
	}
	return p
}

// withEvidence adds the evidence fields to a submitted (or recovered)
// step's payload. artifacts is the client-attested list; it is capped
// (count exact) because it comes from the client.
func (p stepCompletedPayload) withEvidence(stepDef *domain.StepDefinition, artifacts []string, bc BranchChange, unchanged bool) stepCompletedPayload {
	p.RequiredOutputs = stepDef.RequiredOutputs
	p.ArtifactsProduced = capArtifacts(artifacts)
	p.ArtifactsProducedCount = intPtr(len(artifacts))
	p.BranchChange = &bc
	p.RequiredOutputsUnchanged = &unchanged
	return p
}

// capArtifacts returns at most maxEventArtifacts entries, each cut to at
// most maxEventArtifactLength bytes on a UTF-8 boundary.
func capArtifacts(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	n := len(in)
	if n > maxEventArtifacts {
		n = maxEventArtifacts
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = truncateUTF8(in[i], maxEventArtifactLength)
	}
	return out
}

func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// emitStepCompleted emits step_completed with exec's actor on the envelope.
func (o *Orchestrator) emitStepCompleted(ctx context.Context, run *domain.Run, exec *domain.StepExecution, p stepCompletedPayload) {
	raw, err := json.Marshal(p)
	if err != nil {
		observe.Logger(ctx).Warn("step_completed payload could not be encoded", "execution_id", exec.ExecutionID, "error", err)
		raw = nil
	}
	o.emitEventAs(ctx, domain.EventStepCompleted, run.RunID, run.TraceID, exec.ActorID,
		stepCompletedEventID(run, exec), raw)
}

// StepOutputEvidence returns the persisted evidence of every completed
// primary-branch execution of the run, oldest first, for run detail. It
// reads the run and its executions and loads the run's pinned workflow
// once (for required_outputs), and makes no other git call: the evidence
// is the spine.evidence.<execution_id> keys recorded at submit, so it
// survives event-log retention. An execution without a key (binding was
// off, or it ran before INIT-023 TASK-008) is omitted; a key that does not
// decode is skipped with a Warn. Nil when there is no evidence at all.
func (o *Orchestrator) StepOutputEvidence(ctx context.Context, runID string) ([]StepOutputEvidence, error) {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	hasKey := false
	for k := range run.CommitMeta {
		if strings.HasPrefix(k, CommitMetaEvidencePrefix) {
			hasKey = true
			break
		}
	}
	if !hasKey {
		return nil, nil
	}
	execs, err := o.store.ListStepExecutionsByRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list step executions: %w", err)
	}
	// Stores order by created_at; fakes may not. Execution id breaks ties.
	sort.SliceStable(execs, func(i, j int) bool {
		if !execs[i].CreatedAt.Equal(execs[j].CreatedAt) {
			return execs[i].CreatedAt.Before(execs[j].CreatedAt)
		}
		return execs[i].ExecutionID < execs[j].ExecutionID
	})
	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return nil, fmt.Errorf("load workflow: %w", err)
	}

	var out []StepOutputEvidence
	for i := range execs {
		e := &execs[i]
		if e.Status != domain.StepStatusCompleted || e.BranchID != "" {
			continue
		}
		raw, ok := run.CommitMeta[CommitMetaEvidencePrefix+e.ExecutionID]
		if !ok {
			continue
		}
		var rec stepEvidenceRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			observe.Logger(ctx).Warn("step output evidence: stored record does not decode; skipped",
				"run_id", runID, "execution_id", e.ExecutionID, "error", err)
			continue
		}
		var required []string
		if sd := findStepDef(wfDef, e.StepID); sd != nil {
			required = sd.RequiredOutputs
		}
		out = append(out, StepOutputEvidence{
			ExecutionID:              e.ExecutionID,
			StepID:                   e.StepID,
			Attempt:                  e.Attempt,
			ActorID:                  e.ActorID,
			OutcomeID:                e.OutcomeID,
			RequiredOutputs:          required,
			BranchChange:             rec.BranchChange,
			RequiredOutputsUnchanged: rec.RequiredOutputsUnchanged,
		})
	}
	return out, nil
}
