package divergence_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/divergence"
	"github.com/bszymi/spine/core/domain"
)

// TestCreateExploratoryBranch_ControlByteBranchIDFailsClosed pins that
// client input reaches the branchprotect control-byte guard (INIT-023
// TASK-001, cross-task review wave 1). The divergence API
// (POST /runs/{run_id}/divergences/{divergence_id}/branches, contributor)
// passes the request's branch_id unvalidated into the
// spine/<run>/<divergence>/<branch_id> name that createBranchRecord
// evaluates. Nothing on this path checks the bytes before
// Policy.Evaluate, so the guard is what must refuse a control byte, and
// it must refuse before any git branch or branch record exists. The
// fake git client accepts every name, so without the guard the branch
// would be created here.
//
// Today the refusal surfaces as ErrInternal (HTTP 500,
// "branch-protection evaluation failed"). The follow-up recorded under
// TASK-016 owner question 6 validates branch_id at the entry point and
// returns ErrInvalidParams; this test asserts only fail-closed, so it
// stays valid after that change.
func TestCreateExploratoryBranch_ControlByteBranchIDFailsClosed(t *testing.T) {
	ctx := context.Background()
	newSvc := func(t *testing.T) (*divergence.Service, *fakeStore, *fakeGitClient, *domain.DivergenceContext) {
		t.Helper()
		fs := newFakeStore()
		gitClient := &fakeGitClient{}
		svc := divergence.NewService(fs, gitClient, &fakeEventRouter{})
		svc.WithBranchProtectPolicy(branchprotect.New(nil))
		divCtx, err := svc.StartDivergence(ctx, &domain.Run{RunID: "run-1"}, domain.DivergenceDefinition{
			ID:   "div-1",
			Mode: domain.DivergenceModeExploratory,
		}, "")
		if err != nil {
			t.Fatalf("start divergence: %v", err)
		}
		return svc, fs, gitClient, divCtx
	}

	for _, id := range []string{"a\x00", "a\x01", "a\nb", "a\x1f", "a\x7f"} {
		svc, fs, gitClient, divCtx := newSvc(t)
		branch, err := svc.CreateExploratoryBranch(ctx, divCtx, id, "step-1")
		if err == nil {
			t.Errorf("branch_id %q: created %+v, want a refusal", id, branch)
		}
		if len(gitClient.branches) != 0 {
			t.Errorf("branch_id %q: git branches created %q, want none", id, gitClient.branches)
		}
		if len(fs.branches) != 0 {
			t.Errorf("branch_id %q: %d branch records created, want none", id, len(fs.branches))
		}
	}

	// Control: the same wiring admits a well-formed branch_id, so the
	// refusals above come from the bytes, not from the policy refusing
	// everything.
	svc, _, gitClient, divCtx := newSvc(t)
	if _, err := svc.CreateExploratoryBranch(ctx, divCtx, "variant-1", "step-1"); err != nil {
		t.Fatalf("well-formed branch_id refused: %v", err)
	}
	if len(gitClient.branches) != 1 {
		t.Fatalf("git branches = %q, want exactly one", gitClient.branches)
	}
}
