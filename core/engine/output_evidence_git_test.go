package engine_test

// INIT-023 TASK-008 review round 1 (P2), on the real git.CLIClient with
// merge binding on (TASK-005's regression env): step-output evidence is
// judged on the tree. A run branch whose head moved by an empty commit —
// nothing produced — is unchanged and flagged; a real change is not.

import (
	"context"
	"reflect"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
)

func TestStepOutputEvidence_EmptyCommitIsFlagged(t *testing.T) {
	cases := []struct {
		name   string
		commit func(e *regEnv)
		want   func(base, head string) engine.BranchChange
		flag   bool
	}{
		{
			name: "an empty commit: unchanged, flagged",
			commit: func(e *regEnv) {
				e.git("checkout", "-q", e.branch)
				e.git("commit", "-q", "--allow-empty", "-m", "nothing produced")
				e.git("checkout", "-q", "main")
			},
			want: func(base, head string) engine.BranchChange {
				zero := 0
				return engine.BranchChange{RepositoryID: domain.PrimaryRepositoryID, Status: engine.BranchChangeUnchanged,
					HeadSHA: head, PreviousSHA: base, PreviousSource: engine.EvidencePreviousSourceBaseline,
					ChangedPaths: &zero, Reason: engine.EvidenceReasonTreeUnchanged}
			},
			flag: true,
		},
		{
			name: "a change and its revert: unchanged, flagged",
			commit: func(e *regEnv) {
				e.commitOnRun("add", map[string]string{"out.md": "x\n"})
				e.git("checkout", "-q", e.branch)
				e.git("revert", "--no-edit", "HEAD")
				e.git("checkout", "-q", "main")
			},
			want: func(base, head string) engine.BranchChange {
				zero := 0
				return engine.BranchChange{RepositoryID: domain.PrimaryRepositoryID, Status: engine.BranchChangeUnchanged,
					HeadSHA: head, PreviousSHA: base, PreviousSource: engine.EvidencePreviousSourceBaseline,
					ChangedPaths: &zero, Reason: engine.EvidenceReasonTreeUnchanged}
			},
			flag: true,
		},
		{
			name: "a real change: changed, not flagged",
			commit: func(e *regEnv) {
				e.commitOnRun("deliver", map[string]string{"out.md": "x\n"})
			},
			want: func(base, head string) engine.BranchChange {
				one := 1
				return engine.BranchChange{RepositoryID: domain.PrimaryRepositoryID, Status: engine.BranchChangeChanged,
					HeadSHA: head, PreviousSHA: base, PreviousSource: engine.EvidencePreviousSourceBaseline,
					ChangedPaths: &one}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := reviewWorkflow()
			wf.Steps[0].RequiredOutputs = []string{"deliverable"}
			e := newRegEnv(t, wf, regOpts{})
			base := e.runHead()
			tc.commit(e)
			head := e.runHead()
			if head == base {
				t.Fatal("fixture: the run-branch head did not move")
			}
			if err := e.orch.SubmitStepResult(context.Background(), e.actors.assignments[0].AssignmentID,
				engine.StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
				t.Fatalf("submit execute: %v", err)
			}
			got, err := e.orch.StepOutputEvidence(context.Background(), e.run.RunID)
			if err != nil || len(got) != 1 {
				t.Fatalf("StepOutputEvidence = %+v, %v; want one entry", got, err)
			}
			if want := tc.want(base, head); !reflect.DeepEqual(got[0].BranchChange, want) {
				t.Fatalf("branch_change = %+v, want %+v", got[0].BranchChange, want)
			}
			if got[0].RequiredOutputsUnchanged != tc.flag {
				t.Fatalf("required_outputs_unchanged = %v, want %v", got[0].RequiredOutputsUnchanged, tc.flag)
			}
		})
	}
}
