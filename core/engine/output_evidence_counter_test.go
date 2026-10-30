package engine

// INIT-024 TASK-004 AC 8 (review finding N-12): output evidence counts a
// submit's changed paths through the optional ChangedPathCounter when the
// git operator has it. The comparison pair is the author's own (the
// previous binding, or the baseline, and the head they pushed), and Diff
// runs `git diff --name-status` with rename detection and no
// GIT_ALLOC_LIMIT, so the author chose what the git child loaded; the
// counter (*git.CLIClient: `diff --name-only --no-renames` as a read
// command) loads no blob. Without the capability the count comes from Diff
// as before (owner question 2 default).

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// countingEvidenceGit is evidenceGit with the counter.
type countingEvidenceGit struct {
	*evidenceGit
	countN   int
	countErr error
}

func (g *countingEvidenceGit) CountChangedPaths(_ context.Context, a, b string) (int, error) {
	g.record("CountChangedPaths " + a + " " + b)
	if g.countErr != nil {
		return 0, g.countErr
	}
	return g.countN, nil
}

func newCountingEvidenceFix(t *testing.T) (*evidenceFix, *countingEvidenceGit) {
	t.Helper()
	f := newEvidenceFix(t, casWorkflow(), false)
	cg := &countingEvidenceGit{evidenceGit: f.eg}
	f.orch.git = cg
	if err := f.orch.EnableMergeBinding(MergeBindingConfig{}); err != nil {
		t.Fatalf("EnableMergeBinding: %v", err)
	}
	return f, cg
}

func TestOutputEvidence_DiffUsesNoRenameBoundedCounter(t *testing.T) {
	ctx := context.Background()
	base := sha40('b') // the run branch's baseline (newCasFix)
	head := sha40('1')
	branch := "spine/run/run-1"

	for _, tc := range []struct {
		name     string
		n        int
		err      error
		want     BranchChange
		required bool
	}{
		{"changed paths are the counter's count", 4, nil,
			BranchChange{RepositoryID: "spine", Status: BranchChangeChanged, HeadSHA: head,
				PreviousSHA: base, PreviousSource: EvidencePreviousSourceBaseline, ChangedPaths: intPtr(4)}, false},
		{"a moved head with no changed path is unchanged", 0, nil,
			BranchChange{RepositoryID: "spine", Status: BranchChangeUnchanged, HeadSHA: head,
				PreviousSHA: base, PreviousSource: EvidencePreviousSourceBaseline, ChangedPaths: intPtr(0),
				Reason: EvidenceReasonTreeUnchanged}, true},
		{"a failed count is unknown with the kind only", 0,
			&git.GitError{Kind: git.ErrKindPermanent, Op: "count_changed_paths", Message: "output exceeds read limit"},
			BranchChange{RepositoryID: "spine", Status: BranchChangeUnknown, HeadSHA: head,
				PreviousSHA: base, PreviousSource: EvidencePreviousSourceBaseline, Reason: "diff_failed:permanent"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cg := newCountingEvidenceFix(t)
			cg.countN, cg.countErr = tc.n, tc.err
			f.eg.diffErr = errors.New("Diff must not run when the operator can count")
			f.git.setHead(branch, head)
			e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
			if err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
				t.Fatalf("submit: %v", err)
			}
			_, p, _ := f.completedPayload(t, e.ExecutionID)
			if !reflect.DeepEqual(p.BranchChange, &tc.want) {
				t.Fatalf("branch_change = %+v, want %+v", p.BranchChange, tc.want)
			}
			if p.RequiredOutputsUnchanged == nil || *p.RequiredOutputsUnchanged != tc.required {
				t.Fatalf("required_outputs_unchanged = %v, want %v", p.RequiredOutputsUnchanged, tc.required)
			}
			if got := f.git.callCount("CountChangedPaths " + base + " " + head); got != 1 {
				t.Fatalf("CountChangedPaths(previous, head) calls = %d, want 1 (calls %v)", got, f.git.calls)
			}
			if n := f.git.callCount("Diff"); n != 0 {
				t.Fatalf("Diff ran %d times although the operator counts: %v", n, f.git.calls)
			}
		})
	}
}
