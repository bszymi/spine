package workflow

import (
	"fmt"
	"strings"

	"github.com/bszymi/spine/core/domain"
)

// CommitStatusRuleID is the validation rule id of a commit.status a
// workflow's applies_to types do not declare (ValidateCommitStatuses).
const CommitStatusRuleID = "commit_status"

// ValidateCommitStatuses implements architecture/workflow-validation.md
// §5.5 rule 1 (INIT-023 TASK-017, audit P3-21): every outcome's
// commit.status must be a declared lifecycle status of every applies_to
// type. An applies_to Workflow with a commit.status is an error — workflow
// definitions carry no status front matter, so nothing could receive it.
// The `merge` key and every other commit key are ignored. A nil lifecycle
// returns nil (the check is off).
//
// It runs on workflow.Service Create, Update and ValidateBody only (and on
// the engine's workflow planning-run start), never from Validate, the
// projection or the workflow loaders: a stricter projection-time check
// would drop a workflow that is already on the authoritative branch (the
// INIT-023 TASK-011 rule, Decision D10).
//
// One error per offending outcome, naming every applies_to type that does
// not declare the status; Field is steps[i].outcomes[j].commit.status.
func ValidateCommitStatuses(wf *domain.WorkflowDefinition, l *domain.StatusLifecycle) []domain.ValidationError {
	if wf == nil || l == nil {
		return nil
	}
	var errs []domain.ValidationError
	for i := range wf.Steps {
		for j, o := range wf.Steps[i].Outcomes {
			status := o.Commit["status"]
			if status == "" {
				continue
			}
			var problems []string
			for _, at := range wf.AppliesTo {
				t := domain.ArtifactType(at)
				if t == domain.ArtifactTypeWorkflow {
					problems = append(problems, "Workflow (workflow definitions carry no status)")
					continue
				}
				if !l.IsState(t, domain.ArtifactStatus(status)) {
					problems = append(problems, fmt.Sprintf("%s (declared: %s)", at, joinStatuses(l.States(t))))
				}
			}
			if len(problems) == 0 {
				continue
			}
			errs = append(errs, domain.ValidationError{
				RuleID:   CommitStatusRuleID,
				Severity: "error",
				Field:    fmt.Sprintf("steps[%d].outcomes[%d].commit.status", i, j),
				Message:  fmt.Sprintf("commit.status %q is not a status of %s", status, strings.Join(problems, "; ")),
			})
		}
	}
	return errs
}

func joinStatuses(ss []domain.ArtifactStatus) string {
	if len(ss) == 0 {
		return "none"
	}
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = string(s)
	}
	return strings.Join(parts, ", ")
}
