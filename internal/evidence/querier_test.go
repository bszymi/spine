package evidence_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/evidence"
)

func TestRefsForRun_NilRun_ReturnsPrimaryBranch(t *testing.T) {
	got := evidence.RefsForRun(nil, "main")
	if len(got) != 1 || got[0] != "main" {
		t.Errorf("got %v, want [main]", got)
	}
}

func TestRefsForRun_EmptyPrimaryBranch_DefaultsToMain(t *testing.T) {
	// PrimaryAuthoritativeBranchDefault gates this — the spine
	// convention default. Codex pass-6 finding: NEVER fall back to
	// "HEAD" because HEAD is the working tree's mutable current
	// branch and concurrent checkouts surface stale evidence.
	got := evidence.RefsForRun(nil, "")
	if len(got) != 1 || got[0] != "main" {
		t.Errorf("got %v, want [main]", got)
	}
}

func TestRefsForRun_EmptyBranch_ReturnsPrimary(t *testing.T) {
	got := evidence.RefsForRun(&domain.Run{Status: domain.RunStatusActive}, "main")
	if len(got) != 1 || got[0] != "main" {
		t.Errorf("got %v, want [main]", got)
	}
}

func TestRefsForRun_TerminalRuns_PrefersPrimaryBranch_AnchorsCodexP1P3P6(t *testing.T) {
	// All terminal statuses prefer the named primary branch:
	// completed runs durably committed evidence there (engine merge
	// step), and failed/cancelled runs typically have their run
	// branches deleted by the engine cleanup path. Run branch is a
	// fallback for the rare preserved-branch path. Three-codex-pass
	// trail: P1 wanted branch-first for failed; P3 corrected because
	// failed/cancelled typically DELETE the branch; P6 corrected
	// "HEAD" → named branch (HEAD is mutable working-tree state).
	for _, status := range []domain.RunStatus{
		domain.RunStatusCompleted,
		domain.RunStatusFailed,
		domain.RunStatusCancelled,
	} {
		run := &domain.Run{Status: status, BranchName: "spine/run/abc"}
		got := evidence.RefsForRun(run, "main")
		if len(got) != 2 || got[0] != "main" || got[1] != "spine/run/abc" {
			t.Errorf("status=%s: got %v, want [main spine/run/abc]", status, got)
		}
	}
}

func TestRefsForRun_PartiallyMerged_PrefersRunBranch(t *testing.T) {
	// Partially-merged is intentionally NON-TERMINAL in Spine's
	// state machine (run.go:32-39) — the engine preserves the run
	// branch so the operator can inspect cross-repo state mid-recovery.
	// Evidence on the branch is the more complete picture; the
	// primary branch has only the partial subset that already merged.
	run := &domain.Run{Status: domain.RunStatusPartiallyMerged, BranchName: "spine/run/abc"}
	got := evidence.RefsForRun(run, "main")
	if len(got) != 2 || got[0] != "spine/run/abc" || got[1] != "main" {
		t.Errorf("partially-merged: got %v, want [spine/run/abc main]", got)
	}
}

func TestRefsForRun_NonMainPrimary_HonoredEverywhere(t *testing.T) {
	// Workspaces with non-main authoritative branches get explicit
	// support via the second arg; the policy rules above hold with
	// the substituted ref.
	run := &domain.Run{Status: domain.RunStatusCompleted, BranchName: "spine/run/x"}
	got := evidence.RefsForRun(run, "trunk")
	if len(got) != 2 || got[0] != "trunk" || got[1] != "spine/run/x" {
		t.Errorf("got %v, want [trunk spine/run/x]", got)
	}
}

func TestQuerier_NilQuerier_ReturnsNil(t *testing.T) {
	var q *evidence.Querier
	got, err := q.SummarizeForRun(context.Background(), &domain.Run{RunID: "x"})
	if err != nil {
		t.Fatalf("nil querier returned error: %v", err)
	}
	if got != nil {
		t.Errorf("nil querier returned non-nil summary")
	}
}

func TestQuerier_NilReader_ReturnsNil(t *testing.T) {
	q := evidence.NewQuerier(nil)
	got, err := q.SummarizeForRun(context.Background(), &domain.Run{RunID: "x"})
	if err != nil {
		t.Fatalf("nil reader returned error: %v", err)
	}
	if got != nil {
		t.Errorf("nil reader querier returned non-nil summary")
	}
}

// TestQuerier_PlanningRun_ReturnsNil_AnchorsCodexP5 mirrors the EV-*
// rules' planning-run skip: planning runs do not produce execution
// evidence, so the querier returns (nil, nil) and the handler omits
// the `evidence` field rather than reporting missing for a run
// state that has no evidence concept.
func TestQuerier_PlanningRun_ReturnsNil_AnchorsCodexP5(t *testing.T) {
	q := evidence.NewQuerier(newFakeReaderWithFiles(nil))
	run := &domain.Run{
		RunID:                "run-planning-1",
		Mode:                 domain.RunModePlanning,
		Status:               domain.RunStatusActive,
		AffectedRepositories: []string{"spine"},
	}
	got, err := q.SummarizeForRun(context.Background(), run)
	if err != nil {
		t.Fatalf("planning run returned error: %v", err)
	}
	if got != nil {
		t.Errorf("planning run returned non-nil summary: %+v", got)
	}
}

func TestQuerier_DispatchesToBuildSummary_UsingMainBranch(t *testing.T) {
	files := map[string][]byte{
		// Production querier reads from the primary branch (default
		// "main"), NOT from HEAD. Codex pass-6 invariant.
		"main\t" + evidence.EvidencePath("run-1", "spine"): fixtureEvidence(t, "run-1", "spine", nil),
	}
	q := evidence.NewQuerier(newFakeReaderWithFiles(files))
	run := &domain.Run{RunID: "run-1", Status: domain.RunStatusCompleted, AffectedRepositories: []string{"spine"}}
	got, err := q.SummarizeForRun(context.Background(), run)
	if err != nil {
		t.Fatalf("SummarizeForRun: %v", err)
	}
	if got == nil || got.Status != evidence.RunEvidencePassed {
		t.Errorf("got = %+v, want non-nil with status=passed", got)
	}
}

// TestQuerier_FailedRunWithPreservedBranch_FallsBackToBranch covers
// the rare-but-real codex-pass-1 P2 case: a failed run whose branch
// was preserved for debugging (e.g., promoted from partially-merged
// before being marked failed) carries evidence the operator must be
// able to inspect. Primary-branch-first ordering means the typical
// "branch deleted post-failure" path is a single read; the branch
// fallback handles the preserved case without operator-facing data
// loss.
func TestQuerier_FailedRunWithPreservedBranch_FallsBackToBranch(t *testing.T) {
	files := map[string][]byte{
		// Evidence ONLY on the run branch — main has nothing because
		// the merge never completed.
		"spine/run/failing\t" + evidence.EvidencePath("run-1", "spine"): fixtureEvidence(t, "run-1", "spine", nil),
	}
	q := evidence.NewQuerier(newFakeReaderWithFiles(files))
	run := &domain.Run{
		RunID:                "run-1",
		Status:               domain.RunStatusFailed,
		BranchName:           "spine/run/failing",
		AffectedRepositories: []string{"spine"},
	}
	got, err := q.SummarizeForRun(context.Background(), run)
	if err != nil {
		t.Fatalf("SummarizeForRun: %v", err)
	}
	if got == nil || got.Repositories[0].Present != true {
		t.Fatalf("expected evidence found on run branch for failed run, got %+v", got)
	}
	if got.Status != evidence.RunEvidencePassed {
		t.Errorf("status = %s, want passed (evidence loaded from run branch)", got.Status)
	}
}

// TestQuerier_CompletedRunFallsBackToBranch verifies the second-ref
// fallback path — a completed run whose primary read returns
// Found=false on main still surfaces evidence preserved on the run
// branch (the rare-but-real "branch was kept around" case).
func TestQuerier_CompletedRunFallsBackToBranch(t *testing.T) {
	files := map[string][]byte{
		// Main has nothing for run-1; the branch (preserved) does.
		"spine/run/preserved\t" + evidence.EvidencePath("run-1", "spine"): fixtureEvidence(t, "run-1", "spine", nil),
	}
	q := evidence.NewQuerier(newFakeReaderWithFiles(files))
	run := &domain.Run{
		RunID:                "run-1",
		Status:               domain.RunStatusCompleted,
		BranchName:           "spine/run/preserved",
		AffectedRepositories: []string{"spine"},
	}
	got, _ := q.SummarizeForRun(context.Background(), run)
	if got == nil || !got.Repositories[0].Present {
		t.Fatalf("expected fallback to run branch on completed run, got %+v", got)
	}
}

// TestQuerier_WithPrimaryBranch_OverridesDefault confirms a workspace
// with a non-default authoritative branch gets evidence read from
// that branch, not the spine default.
func TestQuerier_WithPrimaryBranch_OverridesDefault(t *testing.T) {
	files := map[string][]byte{
		"trunk\t" + evidence.EvidencePath("run-1", "spine"): fixtureEvidence(t, "run-1", "spine", nil),
	}
	q := evidence.NewQuerier(newFakeReaderWithFiles(files)).WithPrimaryBranch("trunk")
	run := &domain.Run{RunID: "run-1", Status: domain.RunStatusCompleted, AffectedRepositories: []string{"spine"}}
	got, _ := q.SummarizeForRun(context.Background(), run)
	if got == nil || !got.Repositories[0].Present {
		t.Fatalf("expected evidence on trunk for completed run, got %+v", got)
	}
}

func TestQuerier_WithPrimaryBranch_EmptyIgnored(t *testing.T) {
	// Setting empty must not silently override the configured value
	// to "" — empty is a no-op rather than a re-default.
	files := map[string][]byte{
		"main\t" + evidence.EvidencePath("run-1", "spine"): fixtureEvidence(t, "run-1", "spine", nil),
	}
	q := evidence.NewQuerier(newFakeReaderWithFiles(files)).WithPrimaryBranch("")
	run := &domain.Run{RunID: "run-1", Status: domain.RunStatusCompleted, AffectedRepositories: []string{"spine"}}
	got, _ := q.SummarizeForRun(context.Background(), run)
	if got == nil || !got.Repositories[0].Present {
		t.Fatalf("expected default branch retained on empty override, got %+v", got)
	}
}
