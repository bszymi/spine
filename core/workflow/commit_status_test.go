package workflow_test

// INIT-023 TASK-017 AC 3: workflow.Service refuses, at create/update time,
// a commit.status the applies_to types do not declare
// (architecture/workflow-validation.md §5.5 rule 1), and ValidateBody
// reports the same errors. The check is not part of Validate (the TASK-011
// rule: a projection-time check would drop workflows already on main).

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
)

// foreignStatusBody is the design-phase PoC body: applies_to Task with
// outcome statuses no Task can hold.
const foreignStatusBody = `id: foreign-status
name: Foreign Status
version: "{V}"
status: Active
description: commit.status values that are not Task statuses
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    outcomes:
      - id: accept
        name: Accept
        next_step: end
        commit:
          status: Accepted
      - id: ship
        name: Ship
        next_step: end
        commit:
          status: Shipped
`

const declaredStatusBody = `id: foreign-status
name: Foreign Status
version: "1.0"
status: Active
description: declared statuses only
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    outcomes:
      - id: accept
        name: Accept
        next_step: end
        commit:
          status: Completed
`

func foreign(version string) string { return strings.ReplaceAll(foreignStatusBody, "{V}", version) }

func requireCommitStatusErrors(t *testing.T, errs []domain.ValidationError, fields ...string) {
	t.Helper()
	if len(errs) != len(fields) {
		t.Fatalf("got %d errors %v, want %d", len(errs), errs, len(fields))
	}
	for i, f := range fields {
		if errs[i].RuleID != workflow.CommitStatusRuleID || errs[i].Severity != "error" || errs[i].Field != f {
			t.Fatalf("error %d = %+v, want rule %s on %s", i, errs[i], workflow.CommitStatusRuleID, f)
		}
	}
}

func detailErrors(t *testing.T, err error) []domain.ValidationError {
	t.Helper()
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrValidationFailed {
		t.Fatalf("err = %v, want validation_failed", err)
	}
	errs, ok := se.Detail.([]domain.ValidationError)
	if !ok {
		t.Fatalf("detail = %#v, want []ValidationError", se.Detail)
	}
	return errs
}

func headOf(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCommitStatus_ServiceCreateRefused(t *testing.T) {
	svc, repo := newSvc(t)
	svc.WithStatusLifecycle(domain.DefaultStatusLifecycle())
	head := headOf(t, repo)

	_, err := svc.Create(ctxWithActor(), "foreign-status", foreign("1.0"))
	requireCommitStatusErrors(t, detailErrors(t, err),
		"steps[0].outcomes[0].commit.status", "steps[0].outcomes[1].commit.status")
	if got := headOf(t, repo); got != head {
		t.Fatalf("HEAD moved: %s -> %s", head, got)
	}
	if _, err := os.Stat(filepath.Join(repo, "workflows", "foreign-status.yaml")); !os.IsNotExist(err) {
		t.Fatalf("the refused workflow was written: %v", err)
	}
	if !strings.Contains(detailErrors(t, err)[0].Message, `commit.status "Accepted" is not a status of Task (declared: `) {
		t.Fatalf("message = %q", detailErrors(t, err)[0].Message)
	}
}

func TestCommitStatus_ServiceUpdateRefused(t *testing.T) {
	svc, repo := newSvc(t)
	if _, err := svc.Create(ctxWithActor(), "foreign-status", declaredStatusBody); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(repo, "workflows", "foreign-status.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	head := headOf(t, repo)
	svc.WithStatusLifecycle(domain.DefaultStatusLifecycle())

	_, err = svc.Update(ctxWithActor(), "foreign-status", foreign("2.0"))
	requireCommitStatusErrors(t, detailErrors(t, err),
		"steps[0].outcomes[0].commit.status", "steps[0].outcomes[1].commit.status")
	after, _ := os.ReadFile(filepath.Join(repo, "workflows", "foreign-status.yaml"))
	if !bytes.Equal(after, before) || headOf(t, repo) != head {
		t.Fatal("the refused update changed the file or HEAD")
	}
}

func TestCommitStatus_ValidateBody(t *testing.T) {
	svc, _ := newSvc(t)
	svc.WithStatusLifecycle(domain.DefaultStatusLifecycle())
	res := svc.ValidateBody(ctxWithActor(), "foreign-status", foreign("1.0"))
	if res.Status != "failed" {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	requireCommitStatusErrors(t, res.Errors,
		"steps[0].outcomes[0].commit.status", "steps[0].outcomes[1].commit.status")

	if res := svc.ValidateBody(ctxWithActor(), "foreign-status", declaredStatusBody); res.Status != "passed" {
		t.Fatalf("declared statuses: %s %v", res.Status, res.Errors)
	}
}

func TestCommitStatus_WorkflowAppliesTo(t *testing.T) {
	l := domain.DefaultStatusLifecycle()
	wf := &domain.WorkflowDefinition{AppliesTo: []string{"Workflow"}, Steps: []domain.StepDefinition{{
		ID: "review", Outcomes: []domain.OutcomeDefinition{
			{ID: "approved", Commit: map[string]string{"status": "Active"}},
			{ID: "merged", Commit: map[string]string{"merge": "true"}},
		},
	}}}
	requireCommitStatusErrors(t, workflow.ValidateCommitStatuses(wf, l), "steps[0].outcomes[0].commit.status")

	wf.Steps[0].Outcomes = wf.Steps[0].Outcomes[1:]
	if errs := workflow.ValidateCommitStatuses(wf, l); len(errs) != 0 {
		t.Fatalf("commit.merge only: %v", errs)
	}
	if errs := workflow.ValidateCommitStatuses(wf, nil); errs != nil {
		t.Fatalf("nil lifecycle: %v", errs)
	}
}

// Zero-value control: without the option the same Create succeeds, and
// workflow.Validate passes the body (the check is not in Validate).
func TestCommitStatus_ZeroValueAndValidateUnchanged(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Create(ctxWithActor(), "foreign-status", foreign("1.0")); err != nil {
		t.Fatalf("Create without the lifecycle: %v", err)
	}
	wf, err := workflow.Parse("workflows/foreign-status.yaml", []byte(foreign("1.0")))
	if err != nil {
		t.Fatal(err)
	}
	if res := workflow.Validate(wf); res.Status != "passed" {
		t.Fatalf("Validate = %s %v, want passed", res.Status, res.Errors)
	}
	if res := svc.ValidateBody(ctxWithActor(), "foreign-status", foreign("1.0")); res.Status != "passed" {
		t.Fatalf("ValidateBody without the lifecycle = %s %v", res.Status, res.Errors)
	}
}
