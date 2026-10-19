package engine

// INIT-023 wave 4 cross-task review (P3): the decision API reports a
// refused merge whose take-back failed — blocked, not held — instead of
// telling a reject the approved merge "already reached the local main".
// Note: "Cross-task review (wave 4)" on TASK-006.

import (
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// TestWave4Review_BlockedRefusedMergeIsReportedTruthfully (P3): an
// approved merge the post-merge assertion refuses, whose take-back fails,
// stays on the local main and blocks governed merges. The approval says
// so (merge_error), the status reports it blocked (not held), and a reject
// is a 409 that says why — not "the approved merge already reached the
// local main". Once the take-back works, the run's retry takes the merge
// back and routes the run.
func TestWave4Review_BlockedRefusedMergeIsReportedTruthfully(t *testing.T) {
	ctx := context.Background()
	var w *lateErrCLI
	g := newGovRepo(t, govRepoOpts{
		mainFiles: map[string]string{"drafts/wf.yaml": govTaskDefault, "workflows/task-default.yaml": govTaskDefault, "README.md": "r\n"},
		runFiles:  map[string]string{"drafts/wf.yaml": govReviewLess, "workflows/task-default.yaml": govReviewLess},
		guard:     defaultGuard(),
		merger:    true,
		wrap:      func(c *git.CLIClient) GitOperator { w = &lateErrCLI{CLIClient: c}; return w },
	})
	g.git("mv", "drafts/wf.yaml", "workflows/task-x.yaml")
	g.git("commit", "-q", "-m", "main moves the draft into workflows/")
	base := g.mainHead()
	g.toMerge()
	g.requireHeld("held for task-default.yaml")
	w.failNext = 1000
	res, err := g.decide(GovernanceApprove, g.runHead(), govOperator, "")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if res.MergeError == "" || res.RunStatus != domain.RunStatusCommitting {
		t.Fatalf("approve result %+v; want a merge error and committing", res)
	}
	st, err := g.orch.GovernanceMergeStatus(ctx, govRunID)
	if err != nil || !st.Blocked || st.Held || st.BlockedMergeSHA != g.mainHead() {
		t.Fatalf("status %+v, %v; want blocked on %s, not held", st, err, g.mainHead())
	}
	_, err = g.decide(GovernanceReject, g.runHead(), govOperator, "no")
	requireSpineCode(t, err, domain.ErrConflict)
	if strings.Contains(err.Error(), "already reached") || !strings.Contains(err.Error(), "could not be taken back") {
		t.Fatalf("reject: %v", err)
	}
	w.failNext = 0
	if err := g.orch.MergeRunBranch(ctx, govRunID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if g.mainHead() != base || g.store.runs[govRunID].Status != domain.RunStatusActive {
		t.Fatalf("main %s (base %s), run %s; want the merge taken back and the run routed back",
			g.mainHead(), base, g.store.runs[govRunID].Status)
	}
}
