package workflow

import (
	"fmt"

	"github.com/bszymi/spine/core/domain"
)

// DeferredStatusPrecondition identifies a status precondition on the run's
// own artifact that can never hold mid-run (INIT-023 TASK-007, Design §8.4).
//
// The shape is a consumed API: SMP TASK-011's load-time legacy loader drops
// exactly the preconditions this report names from workspaces that keep the
// old workflow files, so every field is needed to identify one precondition
// without ambiguity. Do not change it without coordinating with SMP.
type DeferredStatusPrecondition struct {
	StepID string   // step carrying the precondition
	Index  int      // position in StepDefinition.Preconditions
	Type   string   // "artifact_status" or "field_value"
	Status string   // the required status value
	SetBy  []string // "<step_id>.<outcome_id>" whose commit.status sets it
}

// String renders the finding for test and lint output.
func (d DeferredStatusPrecondition) String() string {
	return fmt.Sprintf("%s: preconditions[%d] %s %q (set only by %v, which is applied at merge)",
		d.StepID, d.Index, d.Type, d.Status, d.SetBy)
}

// DeferredStatusPreconditions reports every `artifact_status` precondition,
// and every `field_value` precondition on `field: status`, that reads the
// run's own artifact (no `config.path`) and requires a status that:
//
//   - is set by some outcome's `commit.status` in the same workflow, and
//   - is not required by the entry step's own status preconditions.
//
// Why such a precondition can never pass: an outcome's commit metadata is
// stored on the run (SetCommitMeta) and written to the artifact only inside
// MergeRunBranch, at publish time. Until then the run's own artifact keeps
// its run-start status on main and on the run branch, so a later step that
// requires a status an earlier outcome "commits" waits for something that
// happens only after the run ends. Under an enforcing step-entry policy that
// step can never be bound, and every run of the workflow strands.
//
// `field_present` on `status` is not reported: it requires presence, not a
// value. The report is ordered by step order in the workflow, then by Index;
// SetBy is in step, then outcome, order.
//
// Limitation (accepted, Design §8.4 note): the precise statement is "a
// non-entry step's own-artifact status precondition can only hold if it
// equals the run-start status". This heuristic catches every case known in
// Spine's and SMP's corpora, but would miss a precondition requiring a
// status that is neither committed by any outcome nor the start status.
// Such a precondition is already unsatisfiable from run start and fails the
// same way at activation, so the rule is not widened.
//
// This is an exported helper and a test, deliberately not a Validate rule:
// adding it to validation would change validation output on embedders'
// workflow write paths before they opt in to the step-entry policy.
func DeferredStatusPreconditions(wf *domain.WorkflowDefinition) []DeferredStatusPrecondition {
	if wf == nil {
		return nil
	}

	setBy := map[string][]string{}
	for i := range wf.Steps {
		s := &wf.Steps[i]
		for j := range s.Outcomes {
			if st := s.Outcomes[j].Commit["status"]; st != "" {
				setBy[st] = append(setBy[st], s.ID+"."+s.Outcomes[j].ID)
			}
		}
	}

	entryStatuses := map[string]bool{}
	for i := range wf.Steps {
		if wf.Steps[i].ID != wf.EntryStep {
			continue
		}
		for _, p := range wf.Steps[i].Preconditions {
			if st, ok := ownArtifactStatus(p); ok {
				entryStatuses[st] = true
			}
		}
	}

	var out []DeferredStatusPrecondition
	for i := range wf.Steps {
		s := &wf.Steps[i]
		for idx, p := range s.Preconditions {
			st, ok := ownArtifactStatus(p)
			if !ok || entryStatuses[st] || len(setBy[st]) == 0 {
				continue
			}
			out = append(out, DeferredStatusPrecondition{
				StepID: s.ID,
				Index:  idx,
				Type:   p.Type,
				Status: st,
				SetBy:  append([]string(nil), setBy[st]...),
			})
		}
	}
	return out
}

// ownArtifactStatus returns the status a precondition requires of the run's
// own artifact, or ok=false when the precondition is not such a status
// requirement (another type, another field, an explicit path, or no value).
func ownArtifactStatus(p domain.Precondition) (string, bool) {
	if p.Config["path"] != "" {
		return "", false
	}
	var st string
	switch p.Type {
	case "artifact_status":
		st = p.Config["status"]
	case "field_value":
		if p.Config["field"] != "status" {
			return "", false
		}
		st = p.Config["value"]
	default:
		return "", false
	}
	return st, st != ""
}
