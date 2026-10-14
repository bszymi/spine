package engine

// INIT-023 TASK-005, merge side, against the fake binder: binding off
// keeps today's calls (AC 7), enabling fails closed (AC 8), a missing,
// invalid or foreign binding fails closed (AC 9, AC 29), the structural
// check refuses every merge shape but the three allowed ones and pushes
// nothing (AC 12), and the collision renumber is skipped (AC 18).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// committingFix is a publish-workflow run in committing with its publish
// step in progress, or a legacy run in committing.
func committingFix(t *testing.T, legacy, bindingOn bool) *bindFix {
	t.Helper()
	wf := publishWorkflow()
	if legacy {
		wf = legacyWorkflow()
	}
	f := newBindFix(t, wf, bindingOn, "")
	f.run.Status = domain.RunStatusCommitting
	if legacy {
		e := f.exec("review", domain.StepStatusCompleted)
		e.OutcomeID = "accepted"
	} else {
		e := f.exec("review", domain.StepStatusCompleted)
		e.OutcomeID = "accepted"
		p := f.exec("publish", domain.StepStatusInProgress)
		p.ActorID = EngineMergeActorID
	}
	return f
}

func (f *bindFix) merge() error {
	return f.orch.MergeRunBranch(context.Background(), f.run.RunID)
}

// requireMergeFailedRoute asserts the permanent-failure route: publish
// workflows go back to execute (run active), legacy ones fail the run.
func requireMergeFailedRoute(t *testing.T, f *bindFix, legacy bool) {
	t.Helper()
	if legacy {
		if f.run.Status != domain.RunStatusFailed {
			t.Fatalf("legacy run status = %s, want failed", f.run.Status)
		}
		return
	}
	if f.run.Status != domain.RunStatusActive {
		t.Fatalf("run status = %s, want active (merge_failed route-back)", f.run.Status)
	}
	if p := f.latestExec("publish"); p.OutcomeID != "merge_failed" {
		t.Fatalf("publish outcome = %q, want merge_failed", p.OutcomeID)
	}
	if e := f.latestExec("execute"); e == nil || e.Status != domain.StepStatusWaiting {
		t.Fatalf("no execute step created by the route-back: %+v", e)
	}
}

func TestEnableMergeBinding(t *testing.T) {
	o := &Orchestrator{git: &plainGit{}}
	if err := o.EnableMergeBinding(MergeBindingConfig{}); err == nil || o.MergeBindingEnabled() {
		t.Fatalf("EnableMergeBinding on a non-CommitBinder = %v, enabled=%v; want an error and binding off", err, o.MergeBindingEnabled())
	}
	o = &Orchestrator{git: newFakeBinderGit()}
	if err := o.EnableMergeBinding(MergeBindingConfig{MovedHead: "sometimes"}); err == nil || o.MergeBindingEnabled() {
		t.Fatalf("unknown policy = %v, enabled=%v", err, o.MergeBindingEnabled())
	}
	if err := o.EnableMergeBinding(MergeBindingConfig{}); err != nil || !o.MergeBindingEnabled() || o.movedHead != MovedHeadReject {
		t.Fatalf("default config = %v, enabled=%v, policy=%q", err, o.MergeBindingEnabled(), o.movedHead)
	}
	if err := o.EnableMergeBinding(MergeBindingConfig{MovedHead: MovedHeadRebind}); err != nil || o.movedHead != MovedHeadRebind {
		t.Fatalf("rebind config = %v, policy=%q", err, o.movedHead)
	}
}

// TestBindingOff_MergeCallsUnchanged is AC 7 for the merge: a zero-value
// orchestrator over an operator that also implements CommitBinder never
// calls it, merges the branch name, pushes origin/main and runs the
// branch cascade exactly as before.
func TestBindingOff_MergeCallsUnchanged(t *testing.T) {
	f := committingFix(t, true, false)
	f.run.CommitMeta = map[string]string{"status": "Completed"}
	f.git.files[f.run.BranchName+":tasks/t.md"] = "---\nid: TASK-1\nstatus: In Progress\n---\n"
	if err := f.merge(); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}
	if n := f.git.callCount("binder:"); n != 0 {
		t.Fatalf("binding off called CommitBinder %d times: %v", n, f.git.calls)
	}
	var merges []git.MergeOpts
	for _, m := range f.git.plainMerges {
		if m.Strategy != "abort" {
			merges = append(merges, m)
		}
	}
	if len(merges) != 1 || merges[0].SourceSHA != "" || merges[0].Source != f.run.BranchName || merges[0].Target != "main" {
		t.Fatalf("merges = %+v", merges)
	}
	if len(f.git.plainPushes) != 1 || f.git.plainPushes[0] != "origin main" {
		t.Fatalf("pushes = %v, want [origin main]", f.git.plainPushes)
	}
	want := []string{"Checkout " + f.run.BranchName, "WriteAndStageFile tasks/t.md", "Commit", "Checkout main", "Merge " + f.run.BranchName}
	var got []string
	for _, c := range f.git.calls {
		for _, w := range want {
			if c == w {
				got = append(got, c)
				break
			}
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("cascade/merge sequence = %q, want %q", got, want)
	}
	if f.run.Status != domain.RunStatusCompleted {
		t.Fatalf("run status = %s", f.run.Status)
	}
}

// TestMissingOrInvalidBindingFailsClosed is AC 9 and AC 29.
func TestMissingOrInvalidBindingFailsClosed(t *testing.T) {
	valid := sha40('a')
	goodBy := boundByStep("review", bindRunID+"-review-1")
	cases := []struct {
		name string
		meta map[string]string
	}{
		{"no binding (in flight at enablement)", map[string]string{"status": "Completed"}},
		{"bound_sha main", map[string]string{CommitMetaBoundSHA: "main", CommitMetaBoundBy: goodBy}},
		{"bound_sha 7 characters", map[string]string{CommitMetaBoundSHA: valid[:7], CommitMetaBoundBy: goodBy}},
		{"bound_by missing", map[string]string{CommitMetaBoundSHA: valid}},
		{"bound_by x", map[string]string{CommitMetaBoundSHA: valid, CommitMetaBoundBy: "x"}},
		{"bound_by names another run's step", map[string]string{CommitMetaBoundSHA: valid, CommitMetaBoundBy: "step:review:run-deadbeef-review-1"}},
		{"bound_by names another run's convergence", map[string]string{CommitMetaBoundSHA: valid, CommitMetaBoundBy: "convergence:run-deadbeef:div-1"}},
	}
	for _, legacy := range []bool{false, true} {
		for _, tc := range cases {
			name := tc.name + "/publish"
			if legacy {
				name = tc.name + "/legacy"
			}
			t.Run(name, func(t *testing.T) {
				f := committingFix(t, legacy, true)
				f.run.CommitMeta = tc.meta
				if err := f.merge(); err != nil {
					t.Fatalf("MergeRunBranch: %v", err)
				}
				if len(f.git.merges) != 0 || f.git.callCount("binder:PushCommit") != 0 {
					t.Fatalf("merged or pushed without a valid binding: %v", f.git.calls)
				}
				po := f.primaryOutcome()
				if po == nil || po.Status != domain.RepositoryMergeStatusFailed || po.FailureClass != domain.MergeFailurePrecondition {
					t.Fatalf("primary outcome = %+v, want failed/precondition", po)
				}
				requireMergeFailedRoute(t, f, legacy)
			})
		}
	}
	t.Run("valid binding merges", func(t *testing.T) {
		f := committingFix(t, false, true)
		f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: valid, CommitMetaBoundBy: goodBy}
		m, b := sha40('c'), sha40('b')
		f.git.mergeRes = git.MergeResult{SHA: m, BaseSHA: b}
		f.git.parents[m] = []string{b, valid}
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		if len(f.git.merges) != 1 || f.git.merges[0].SourceSHA != valid || f.git.merges[0].Source != f.run.BranchName {
			t.Fatalf("MergeBound calls = %+v", f.git.merges)
		}
		if len(f.git.pushes) != 1 || f.git.pushes[0] != "origin main "+m {
			t.Fatalf("pushes = %v", f.git.pushes)
		}
		if f.run.Status != domain.RunStatusCompleted || f.latestExec("publish").OutcomeID != "published" {
			t.Fatalf("run %s, publish %q", f.run.Status, f.latestExec("publish").OutcomeID)
		}
		if len(f.git.plainPushes) != 0 {
			t.Fatalf("binding on used Push: %v", f.git.plainPushes)
		}
	})
	t.Run("transient VerifyCommit failure retries", func(t *testing.T) {
		f := committingFix(t, false, true)
		f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: valid, CommitMetaBoundBy: goodBy}
		f.git.verifyErr = &git.GitError{Kind: git.ErrKindTransient, Op: "verify_commit", Message: "repository locked"}
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		if f.run.Status != domain.RunStatusCommitting || len(f.git.merges) != 0 {
			t.Fatalf("run %s, merges %d; want committing and no merge", f.run.Status, len(f.git.merges))
		}
	})
	t.Run("missing bound object fails closed", func(t *testing.T) {
		f := committingFix(t, false, true)
		f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: valid, CommitMetaBoundBy: goodBy}
		f.git.verifyErr = &git.GitError{Kind: git.ErrKindNotFound, Op: "verify_commit", Message: "gone"}
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		if len(f.git.merges) != 0 {
			t.Fatal("merged a missing bound commit")
		}
		requireMergeFailedRoute(t, f, false)
	})
}

// TestVerifyBoundMergeRefusesOtherShapes is AC 12 (with DC-3): every
// result that is not built from exactly the base and the bound commit is
// refused before any push — and taken back (review round 1): the local
// target goes back to the state before the merge, compare-and-swap on
// the refused merge, so the next governed merge cannot build on it.
func TestVerifyBoundMergeRefusesOtherShapes(t *testing.T) {
	bound, base, tip, m, x, w := sha40('a'), sha40('b'), sha40('f'), sha40('c'), sha40('9'), sha40('8')
	cases := []struct {
		name  string
		res   git.MergeResult
		setup func(g *fakeBinderGit)
		reset string // the expected ResetBranch call, "" for none
	}{
		{"merged the tip instead", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) { g.parents[m] = []string{base, tip} }, "main " + base + " " + m},
		{"merged the bound commit and the tip", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) { g.parents[m] = []string{base, bound, tip} }, "main " + base + " " + m},
		{"first parent is not the base (a rewind)", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) {
			g.parents[m] = []string{x, bound}
			g.notAncestor[base+">"+x] = true
		}, "main " + base + " " + m},
		// A newer first parent with anything but the merged commit as the
		// second parent is no target move: refused, but the take-back
		// still keeps the newer write (it descends from the base). The
		// [newer, bound] shape is TestTargetMovedDuringMergeRetries.
		{"newer first parent, second parent the tip", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) { g.parents[m] = []string{w, tip} }, "main " + w + " " + m},
		{"newer first parent and a third parent", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) { g.parents[m] = []string{w, bound, tip} }, "main " + w + " " + m},
		{"parents swapped", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) {
			g.parents[m] = []string{bound, base}
			g.notAncestor[base+">"+bound] = true
		}, "main " + base + " " + m},
		{"up to date but the base lacks the bound commit", git.MergeResult{SHA: base, BaseSHA: base}, func(g *fakeBinderGit) { g.notAncestor[bound+">"+base] = true }, ""},
		{"fast-forward that does not descend from the base", git.MergeResult{SHA: bound, BaseSHA: base}, func(g *fakeBinderGit) { g.notAncestor[base+">"+bound] = true }, "main " + base + " " + bound},
		{"empty BaseSHA", git.MergeResult{SHA: m}, func(g *fakeBinderGit) { g.parents[m] = []string{base, bound} }, ""},
		{"parents unreadable", git.MergeResult{SHA: m, BaseSHA: base}, func(*fakeBinderGit) {}, "main " + base + " " + m},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := committingFix(t, false, true)
			f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
			f.git.setHead(f.run.BranchName, tip)
			f.git.mergeRes = tc.res
			tc.setup(f.git)
			if err := f.merge(); err != nil {
				t.Fatalf("MergeRunBranch: %v", err)
			}
			if n := f.git.callCount("binder:PushCommit"); n != 0 {
				t.Fatalf("pushed a merge that failed verification: %v", f.git.pushes)
			}
			po := f.primaryOutcome()
			if po == nil || po.Status != domain.RepositoryMergeStatusFailed {
				t.Fatalf("primary outcome = %+v, want failed", po)
			}
			requireMergeFailedRoute(t, f, false)
			switch {
			case tc.reset == "" && len(f.git.resets) != 0:
				t.Fatalf("ResetBranch calls = %v, want none (the target did not move, or no base is known)", f.git.resets)
			case tc.reset != "" && (len(f.git.resets) != 1 || f.git.resets[0] != tc.reset):
				t.Fatalf("ResetBranch calls = %v, want exactly [%s]", f.git.resets, tc.reset)
			case tc.reset != "" && !strings.Contains(po.FailureDetail, "was restored to"):
				t.Fatalf("failure detail %q does not say the merge was taken back", po.FailureDetail)
			}
		})
	}
	for name, res := range map[string]git.MergeResult{
		"merge commit [base, bound]": {SHA: m, BaseSHA: base},
		"already up to date":         {SHA: base, BaseSHA: base},
		"fast-forward":               {SHA: bound, BaseSHA: base},
	} {
		t.Run("accepted: "+name, func(t *testing.T) {
			f := committingFix(t, false, true)
			f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
			f.git.mergeRes = res
			f.git.parents[m] = []string{base, bound}
			if err := f.merge(); err != nil {
				t.Fatalf("MergeRunBranch: %v", err)
			}
			if len(f.git.pushes) != 1 || f.git.pushes[0] != "origin main "+res.SHA {
				t.Fatalf("pushes = %v, want exactly the merge result", f.git.pushes)
			}
			if len(f.git.resets) != 0 {
				t.Fatalf("an accepted merge was taken back: %v", f.git.resets)
			}
			if f.run.Status != domain.RunStatusCompleted {
				t.Fatalf("run status = %s", f.run.Status)
			}
		})
	}
}

// TestTransientVerificationFailureRetries: a read the structural check
// needs that fails transiently is not a verdict on the merge (review
// round 1). After the take-back the run stays in committing for the
// scheduler's retry instead of taking the merge_failed route, nothing is
// pushed — and if the take-back could not run, it is a refusal after all
// (a retry would otherwise meet the unverified merge on the target and
// accept it as "already up to date").
func TestTransientVerificationFailureRetries(t *testing.T) {
	bound, base, m := sha40('a'), sha40('b'), sha40('c')
	blip := &git.GitError{Kind: git.ErrKindTransient, Op: "commit_parents", Message: "repository locked"}
	for _, resetFails := range []bool{false, true} {
		f := committingFix(t, false, true)
		f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
		f.git.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
		f.git.parentsErr[m] = blip
		if resetFails {
			f.git.resetErr = &git.GitError{Kind: git.ErrKindPermanent, Op: "reset_branch", Message: "cannot lock ref"}
		}
		if err := f.merge(); err != nil {
			t.Fatalf("resetFails=%v: MergeRunBranch: %v", resetFails, err)
		}
		if len(f.git.pushes) != 0 {
			t.Fatalf("resetFails=%v: pushed an unverified merge: %v", resetFails, f.git.pushes)
		}
		if len(f.git.resets) != 1 || f.git.resets[0] != "main "+base+" "+m {
			t.Fatalf("resetFails=%v: ResetBranch calls = %v", resetFails, f.git.resets)
		}
		if resetFails {
			requireMergeFailedRoute(t, f, false)
			continue
		}
		if f.run.Status != domain.RunStatusCommitting {
			t.Fatalf("run status = %s, want committing (transient: the scheduler retries)", f.run.Status)
		}
		if p := f.latestExec("publish"); p.OutcomeID != "" {
			t.Fatalf("publish outcome = %q, want none yet", p.OutcomeID)
		}
	}
}

// TestTargetMovedDuringMergeRetries (review round 2): a merge whose
// parents are [W, S] — W a newer descendant of the reported base, S the
// merged commit — is the shape of a write landing on the target between
// MergeBound's base read and its merge (no worktree lock until TASK-012).
// It is refused and taken back to W, and then retried (the run stays in
// committing) instead of failing a legacy run or sending a publish run
// back to execute; the retry merges on W and completes. It stays a
// permanent refusal when the take-back did not restore the target to W.
func TestTargetMovedDuringMergeRetries(t *testing.T) {
	bound, base, m, w, m2, x := sha40('a'), sha40('b'), sha40('c'), sha40('8'), sha40('7'), sha40('9')
	for _, legacy := range []bool{false, true} {
		name := "publish workflow"
		if legacy {
			name = "legacy terminal-commit workflow"
		}
		t.Run(name, func(t *testing.T) {
			f := committingFix(t, legacy, true)
			f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
			f.git.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
			f.git.parents[m] = []string{w, bound}
			if err := f.merge(); err != nil {
				t.Fatalf("MergeRunBranch: %v", err)
			}
			if len(f.git.pushes) != 0 {
				t.Fatalf("pushed a merge on an unverified base: %v", f.git.pushes)
			}
			if len(f.git.resets) != 1 || f.git.resets[0] != "main "+w+" "+m {
				t.Fatalf("ResetBranch calls = %v, want exactly [main %s %s] (the write kept)", f.git.resets, w, m)
			}
			if n := f.git.callCount("binder:CommitParents " + m); n != 1 {
				t.Fatalf("CommitParents(M) called %d times, want once (the take-back reuses the verdict's parents)", n)
			}
			if f.run.Status != domain.RunStatusCommitting {
				t.Fatalf("run status = %s, want committing (the scheduler retries)", f.run.Status)
			}
			if !legacy {
				if p := f.latestExec("publish"); p.OutcomeID != "" {
					t.Fatalf("publish outcome = %q, want none (no merge_failed route)", p.OutcomeID)
				}
			}
			po := f.primaryOutcome()
			if po == nil || po.Status != domain.RepositoryMergeStatusFailed || po.FailureClass != domain.MergeFailureRemoteUnavailable ||
				!po.FailureClass.IsTransient() || !strings.Contains(po.FailureDetail, "target moved during the merge") ||
				!strings.Contains(po.FailureDetail, "was restored to "+w) {
				t.Fatalf("primary outcome = %+v, want failed, transient class remote_unavailable, naming the move and the restore", po)
			}

			// The scheduler's retry computes on the write and merges.
			f.git.mergeRes = git.MergeResult{SHA: m2, BaseSHA: w}
			f.git.parents[m2] = []string{w, bound}
			if err := f.merge(); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if f.run.Status != domain.RunStatusCompleted {
				t.Fatalf("run status after the retry = %s, want completed", f.run.Status)
			}
			if len(f.git.pushes) != 1 || f.git.pushes[0] != "origin main "+m2 {
				t.Fatalf("pushes = %v, want exactly the retry's merge %s", f.git.pushes, m2)
			}
			if len(f.git.resets) != 1 {
				t.Fatalf("the accepted retry was taken back: %v", f.git.resets)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		setup func(g *fakeBinderGit)
		reset string
		want  string // in the failure detail
	}{
		{"take-back failed", func(g *fakeBinderGit) {
			g.resetErr = &git.GitError{Kind: git.ErrKindPermanent, Op: "reset_branch", Message: "cannot lock ref"}
		}, "main " + w + " " + m, "still holds the refused merge"},
		{"first parent does not descend from the base", func(g *fakeBinderGit) {
			g.parents[m] = []string{x, bound}
			g.notAncestor[base+">"+x] = true
		}, "main " + base + " " + m, "was restored to " + base},
	} {
		t.Run("permanent: "+tc.name, func(t *testing.T) {
			f := committingFix(t, false, true)
			f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
			f.git.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
			f.git.parents[m] = []string{w, bound}
			tc.setup(f.git)
			if err := f.merge(); err != nil {
				t.Fatalf("MergeRunBranch: %v", err)
			}
			if len(f.git.pushes) != 0 {
				t.Fatalf("pushed: %v", f.git.pushes)
			}
			if len(f.git.resets) != 1 || f.git.resets[0] != tc.reset {
				t.Fatalf("ResetBranch calls = %v, want exactly [%s]", f.git.resets, tc.reset)
			}
			po := f.primaryOutcome()
			if po == nil || po.Status != domain.RepositoryMergeStatusFailed || po.FailureClass.IsTransient() || !strings.Contains(po.FailureDetail, tc.want) {
				t.Fatalf("primary outcome = %+v, want a permanent failure naming %q", po, tc.want)
			}
			requireMergeFailedRoute(t, f, false)
		})
	}
}

// TestRefusedMergeTakeBackFailure: a take-back the binder refuses (the
// target moved on) still refuses the merge and pushes nothing, and the
// outcome says the local target still holds the refused merge.
func TestRefusedMergeTakeBackFailure(t *testing.T) {
	bound, base, tip, m := sha40('a'), sha40('b'), sha40('f'), sha40('c')
	f := committingFix(t, false, true)
	f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
	f.git.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
	f.git.parents[m] = []string{base, tip}
	f.git.resetErr = &git.GitError{Kind: git.ErrKindPermanent, Op: "reset_branch", Message: "cannot lock ref"}
	if err := f.merge(); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}
	if len(f.git.pushes) != 0 {
		t.Fatalf("pushed: %v", f.git.pushes)
	}
	po := f.primaryOutcome()
	if po == nil || po.Status != domain.RepositoryMergeStatusFailed || !strings.Contains(po.FailureDetail, "still holds the refused merge") {
		t.Fatalf("primary outcome = %+v, want failed and naming the leftover merge", po)
	}
	requireMergeFailedRoute(t, f, false)
}

// TestExcludedTipKeepsBranch: a tip the merge did not contain is
// reported and the run branch is kept (owner question 3); a contained
// tip is cleaned up.
func TestExcludedTipKeepsBranch(t *testing.T) {
	bound, base, tip, m := sha40('a'), sha40('b'), sha40('f'), sha40('c')
	for _, excluded := range []bool{true, false} {
		f := committingFix(t, false, true)
		f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
		f.git.setHead(f.run.BranchName, tip)
		f.git.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
		f.git.parents[m] = []string{base, bound}
		if excluded {
			f.git.notAncestor[tip+">"+m] = true
		}
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		deleted := f.git.callCount("DeleteBranch") > 0
		if deleted == excluded {
			t.Fatalf("excluded=%v: branch deleted=%v", excluded, deleted)
		}
		if f.run.Status != domain.RunStatusCompleted {
			t.Fatalf("excluded=%v: run status = %s", excluded, f.run.Status)
		}
	}
}

// countingCollision counts DetectAndRenumber calls.
type countingCollision struct{ calls int }

func (c *countingCollision) DetectAndRenumber(context.Context, *domain.Run, int) (string, error) {
	c.calls++
	return "tasks/renumbered.md", nil
}

// TestCollisionRenumberSkippedUnderBinding is AC 18.
func TestCollisionRenumberSkippedUnderBinding(t *testing.T) {
	for _, on := range []bool{true, false} {
		f := committingFix(t, true, on)
		f.run.Mode = domain.RunModePlanning
		f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: sha40('a'), CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
		cc := &countingCollision{}
		f.orch.WithCollisionHandler(cc)
		conflict := &git.GitError{Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict"}
		f.git.mergeErr = conflict
		if !on {
			f.orch.git = &conflictingPlainGit{err: conflict}
		}
		if err := f.merge(); err != nil {
			t.Fatalf("binding=%v: MergeRunBranch: %v", on, err)
		}
		if on && cc.calls != 0 {
			t.Fatalf("binding on called DetectAndRenumber %d times", cc.calls)
		}
		if !on && cc.calls != 1 {
			t.Fatalf("control (binding off): DetectAndRenumber calls = %d, want 1", cc.calls)
		}
	}
}

// conflictingPlainGit fails every non-abort merge.
type conflictingPlainGit struct {
	stubGitOperator
	err error
}

func (c *conflictingPlainGit) Merge(_ context.Context, o git.MergeOpts) (git.MergeResult, error) {
	if o.Strategy == "abort" {
		return git.MergeResult{}, nil
	}
	return git.MergeResult{}, c.err
}

// TestPushFailureClassificationUnderBinding: a transient PushCommit
// failure keeps the run committing; a permanent one takes the
// merge_failed route.
func TestPushFailureClassificationUnderBinding(t *testing.T) {
	bound, base, m := sha40('a'), sha40('b'), sha40('c')
	for _, transient := range []bool{true, false} {
		f := committingFix(t, false, true)
		f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
		f.git.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
		f.git.parents[m] = []string{base, bound}
		kind := git.ErrKindPermanent
		if transient {
			kind = git.ErrKindTransient
		}
		f.git.pushErr = &git.GitError{Kind: kind, Op: "push", Message: "push"}
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		if transient {
			if f.run.Status != domain.RunStatusCommitting {
				t.Fatalf("transient push failure: run %s, want committing", f.run.Status)
			}
			continue
		}
		requireMergeFailedRoute(t, f, false)
	}
}

func TestCascadeAtBoundCommit_Unit(t *testing.T) {
	bound, base, c, m, tip := sha40('a'), sha40('b'), sha40('7'), sha40('c'), sha40('f')
	f := committingFix(t, false, true)
	f.run.CommitMeta = map[string]string{"status": "Completed", CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
	f.git.files[bound+":tasks/t.md"] = "---\nid: TASK-1\nstatus: In Progress\n---\n"
	f.git.ontoSHA = c
	f.git.parents[c] = []string{bound}
	f.git.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
	f.git.parents[m] = []string{base, c}
	if err := f.merge(); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}
	if len(f.git.ontoCalls) != 1 || !strings.Contains(f.git.ontoCalls[0]["tasks/t.md"], "status: Completed") {
		t.Fatalf("CommitFilesOnto calls = %v", f.git.ontoCalls)
	}
	if len(f.git.merges) != 1 || f.git.merges[0].SourceSHA != c {
		t.Fatalf("MergeBound SourceSHA = %+v, want the cascade commit %s", f.git.merges, c)
	}
	for _, call := range f.git.calls {
		if call == "Checkout "+f.run.BranchName || strings.HasPrefix(call, "WriteAndStageFile") || call == "Commit" {
			t.Fatalf("binding-on cascade touched the run branch or the working tree: %q", call)
		}
	}
	// A failed cascade falls back to the bound commit — including a
	// cascade commit whose parents cannot be read.
	for name, setup := range map[string]func(g *fakeBinderGit){
		"CommitFilesOnto failed": func(g *fakeBinderGit) { g.ontoErr = errors.New("object store full") },
		"parents unreadable":     func(g *fakeBinderGit) { g.ontoSHA = c },
	} {
		f2 := committingFix(t, false, true)
		f2.run.CommitMeta = f.run.CommitMeta
		f2.git.files[bound+":tasks/t.md"] = "---\nid: TASK-1\nstatus: In Progress\n---\n"
		setup(f2.git)
		f2.git.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
		f2.git.parents[m] = []string{base, bound}
		if err := f2.merge(); err != nil {
			t.Fatalf("%s: MergeRunBranch: %v", name, err)
		}
		if len(f2.git.merges) != 1 || f2.git.merges[0].SourceSHA != bound {
			t.Fatalf("%s: fallback MergeBound = %+v, want the bound commit", name, f2.git.merges)
		}
	}
	// A cascade commit that is not a child of exactly the bound commit —
	// built on the run-branch tip, say — would carry unreviewed commits
	// into a merge whose parents are [base, it]: refused, nothing merged
	// (review round 1).
	for name, parents := range map[string][]string{
		"built on the tip": {tip},
		"two parents":      {bound, tip},
		"root commit":      {},
	} {
		f3 := committingFix(t, false, true)
		f3.run.CommitMeta = f.run.CommitMeta
		f3.git.files[bound+":tasks/t.md"] = "---\nid: TASK-1\nstatus: In Progress\n---\n"
		f3.git.ontoSHA = c
		f3.git.parents[c] = parents
		if err := f3.merge(); err != nil {
			t.Fatalf("%s: MergeRunBranch: %v", name, err)
		}
		if n := f3.git.callCount("binder:MergeBound"); n != 0 {
			t.Fatalf("%s: merged a cascade commit that is not a child of the bound commit", name)
		}
		if len(f3.git.pushes) != 0 {
			t.Fatalf("%s: pushed: %v", name, f3.git.pushes)
		}
		po := f3.primaryOutcome()
		if po == nil || po.Status != domain.RepositoryMergeStatusFailed || !strings.Contains(po.FailureDetail, "child of the bound commit") {
			t.Fatalf("%s: primary outcome = %+v, want a failed binding violation", name, po)
		}
		requireMergeFailedRoute(t, f3, false)
	}
}

// TestCascadeBaseAtReplay: when main already contains the bound commit,
// the cascade diffs against main as it was just before this run's earlier
// merge (review round 1), found on main's first-parent chain; anything
// else keeps the plain merge-base.
func TestCascadeBaseAtReplay(t *testing.T) {
	bound, c, orig := sha40('a'), sha40('7'), sha40('1')
	first, merge, write := sha40('2'), sha40('3'), sha40('4')
	newFix := func(t *testing.T) *bindFix {
		f := committingFix(t, false, true)
		f.git.mergeBases["main "+bound] = bound // main contains bound
		f.git.mergeBases[first+" "+bound] = orig
		return f
	}
	t.Run("this run's merge behind an in-place write", func(t *testing.T) {
		f := newFix(t)
		f.git.setHead("main", write)
		f.git.parents[write] = []string{merge}
		f.git.parents[merge] = []string{first, c}
		f.git.parents[c] = []string{bound}
		if got, err := f.orch.cascadeBaseAt(context.Background(), f.run, bound); err != nil || got != orig {
			t.Fatalf("cascadeBaseAt = %s, %v; want the merge-base against the earlier merge's first parent %s", got, err, orig)
		}
	})
	t.Run("the merge merged the bound commit itself", func(t *testing.T) {
		f := newFix(t)
		f.git.setHead("main", merge)
		f.git.parents[merge] = []string{first, bound}
		if got, _ := f.orch.cascadeBaseAt(context.Background(), f.run, bound); got != orig {
			t.Fatalf("cascadeBaseAt = %s, want %s", got, orig)
		}
	})
	t.Run("another run's merge is not this run's", func(t *testing.T) {
		f := newFix(t)
		other := sha40('5')
		f.git.setHead("main", merge)
		f.git.parents[merge] = []string{first, other}
		f.git.parents[other] = []string{sha40('6')}
		f.git.parents[first] = []string{bound}
		if got, _ := f.orch.cascadeBaseAt(context.Background(), f.run, bound); got != bound {
			t.Fatalf("cascadeBaseAt = %s, want the plain merge-base %s", got, bound)
		}
	})
	t.Run("the walk is bounded", func(t *testing.T) {
		f := newFix(t)
		x := sha40('d')
		f.git.setHead("main", x)
		f.git.parents[x] = []string{x} // an endless first-parent chain
		if got, _ := f.orch.cascadeBaseAt(context.Background(), f.run, bound); got != bound {
			t.Fatalf("cascadeBaseAt = %s, want the plain merge-base %s", got, bound)
		}
		if n := f.git.callCount("binder:CommitParents " + x); n != priorMergeWalkLimit {
			t.Fatalf("walked %d steps, want the limit %d", n, priorMergeWalkLimit)
		}
	})
	t.Run("main does not contain bound: plain merge-base, no walk", func(t *testing.T) {
		f := committingFix(t, false, true)
		f.git.mergeBases["main "+bound] = orig
		if got, _ := f.orch.cascadeBaseAt(context.Background(), f.run, bound); got != orig {
			t.Fatalf("cascadeBaseAt = %s, want %s", got, orig)
		}
		if n := f.git.callCount("binder:"); n != 0 {
			t.Fatalf("the replay walk ran on a first attempt: %v", f.git.calls)
		}
	})
}

// TestCodeRepoMergeBoundRefusesOtherShapes is AC 12 for a code
// repository (review round 1): the structural check refuses every shape
// but the three allowed ones, pushes nothing, takes the local default
// branch back, and an accepted merge pushes exactly the merge commit with
// PushCommit — never the local ref with Push.
func TestCodeRepoMergeBoundRefusesOtherShapes(t *testing.T) {
	repoBound, base, tip, m := sha40('a'), sha40('b'), sha40('f'), sha40('c')
	newCodeFix := func(t *testing.T) (*bindFix, *fakeBinderGit) {
		f := committingFix(t, false, true)
		f.run.AffectedRepositories = []string{"spine", "code-a"}
		f.run.CommitMeta = map[string]string{
			CommitMetaBoundSHA:        sha40('e'),
			CommitMetaBoundBy:         boundByStep("review", bindRunID+"-review-1"),
			RepoBoundSHAKey("code-a"): repoBound,
		}
		code := newFakeBinderGit()
		code.setHead(f.run.BranchName, tip)
		f.orch.WithRepositoryResolver(newRepoResolver(map[string]repoLookup{"code-a": activeRepoLookup("code-a", "main")}))
		f.orch.WithRepositoryGitClients(realRepoClients{"code-a": fakeCodeBinder{code}})
		return f, code
	}
	for _, tc := range []struct {
		name  string
		res   git.MergeResult
		setup func(g *fakeBinderGit)
		reset string
	}{
		{"[base, tip]", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) { g.parents[m] = []string{base, tip} }, "main " + base + " " + m},
		{"[base, bound, tip]", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) { g.parents[m] = []string{base, repoBound, tip} }, "main " + base + " " + m},
		{"first parent not the base", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) {
			g.parents[m] = []string{tip, repoBound}
			g.notAncestor[base+">"+tip] = true
		}, "main " + base + " " + m},
		{"empty BaseSHA", git.MergeResult{SHA: m}, func(g *fakeBinderGit) { g.parents[m] = []string{base, repoBound} }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, code := newCodeFix(t)
			code.mergeRes = tc.res
			tc.setup(code)
			out, err := f.orch.attemptCodeRepositoryMerge(context.Background(), f.run, "code-a")
			if err != nil {
				t.Fatalf("attemptCodeRepositoryMerge: %v", err)
			}
			if out.Status != domain.RepositoryMergeStatusFailed {
				t.Fatalf("outcome = %s, want failed", out.Status)
			}
			if len(code.pushes) != 0 || len(code.plainPushes) != 0 {
				t.Fatalf("a refused code-repository merge was pushed: %v %v", code.pushes, code.plainPushes)
			}
			if tc.reset == "" && len(code.resets) != 0 || tc.reset != "" && (len(code.resets) != 1 || code.resets[0] != tc.reset) {
				t.Fatalf("ResetBranch calls = %v, want %q", code.resets, tc.reset)
			}
		})
	}
	// Review round 2: [W, bound] with W a newer descendant of the base is
	// the target moving during the merge — refused, taken back to W, and
	// a transient outcome, so the run stays in committing and retries.
	t.Run("target moved: transient, the write kept", func(t *testing.T) {
		f, code := newCodeFix(t)
		w := sha40('8')
		code.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
		code.parents[m] = []string{w, repoBound}
		out, err := f.orch.attemptCodeRepositoryMerge(context.Background(), f.run, "code-a")
		if err != nil {
			t.Fatalf("attemptCodeRepositoryMerge: %v", err)
		}
		if out.Status != domain.RepositoryMergeStatusFailed || !out.FailureClass.IsTransient() ||
			!strings.Contains(out.FailureDetail, "target moved during the merge") {
			t.Fatalf("outcome = %+v, want failed with a transient class naming the move", out)
		}
		if len(code.pushes) != 0 || len(code.plainPushes) != 0 {
			t.Fatalf("pushed: %v %v", code.pushes, code.plainPushes)
		}
		if len(code.resets) != 1 || code.resets[0] != "main "+w+" "+m {
			t.Fatalf("ResetBranch calls = %v, want exactly [main %s %s]", code.resets, w, m)
		}
	})
	t.Run("accepted: pushes exactly the merge commit", func(t *testing.T) {
		f, code := newCodeFix(t)
		code.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
		code.parents[m] = []string{base, repoBound}
		out, err := f.orch.attemptCodeRepositoryMerge(context.Background(), f.run, "code-a")
		if err != nil || out.Status != domain.RepositoryMergeStatusMerged || out.MergeCommitSHA != m {
			t.Fatalf("outcome = %+v, %v; want merged at %s", out, err, m)
		}
		if len(code.merges) != 1 || code.merges[0].SourceSHA != repoBound {
			t.Fatalf("MergeBound calls = %+v, want exactly one of the bound commit", code.merges)
		}
		if len(code.pushes) != 1 || code.pushes[0] != "origin main "+m || len(code.plainPushes) != 0 {
			t.Fatalf("pushes = %v, plain = %v; want exactly PushCommit origin main %s", code.pushes, code.plainPushes, m)
		}
		if len(code.resets) != 0 {
			t.Fatalf("an accepted merge was taken back: %v", code.resets)
		}
	})
}

// TestBoundLedgerMustBeAChildOfTheBinding: the recovery ledger becomes
// the binding, so a CommitFilesOnto result that is not a child of exactly
// the bound commit is refused and nothing is recorded (review round 1).
func TestBoundLedgerMustBeAChildOfTheBinding(t *testing.T) {
	bound, tip, l := sha40('a'), sha40('f'), sha40('5')
	for name, tc := range map[string]struct {
		parents []string
		ok      bool
	}{
		"child of the bound commit": {[]string{bound}, true},
		"built on the tip":          {[]string{tip}, false},
		"parents unreadable":        {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := committingFix(t, false, true)
			f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
			f.git.ontoSHA = l
			if tc.parents != nil {
				f.git.parents[l] = tc.parents
			}
			got, err := f.orch.writeMergeRecoveryLedgerCommit(context.Background(), f.run, "code-a",
				mergeRecoveryAction{Operation: "merge_recovery_retry", ActorID: "operator-1", Reason: "retry"})
			if tc.ok {
				if err != nil || got != l || f.run.CommitMeta[CommitMetaBoundSHA] != l {
					t.Fatalf("ledger = %s, %v; binding %s; want %s bound", got, err, f.run.CommitMeta[CommitMetaBoundSHA], l)
				}
				return
			}
			if err == nil {
				t.Fatalf("ledger %s accepted with parents %v", got, tc.parents)
			}
			if len(f.store.metaWrites) != 0 || f.run.CommitMeta[CommitMetaBoundSHA] != bound {
				t.Fatalf("a refused ledger changed the binding: %v", f.store.metaWrites)
			}
		})
	}
}

// TestColonStepIDBindingIsTrusted: a binding recorded at a step whose id
// contains ":" (workflow validation allows it) is this run's own and
// merges, instead of failing closed as untrusted (codex review, round 1).
func TestColonStepIDBindingIsTrusted(t *testing.T) {
	bound, base, m := sha40('a'), sha40('b'), sha40('c')
	f := committingFix(t, false, true)
	f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("code:review", bindRunID+"-code:review-1")}
	f.git.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
	f.git.parents[m] = []string{base, bound}
	if err := f.merge(); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}
	if len(f.git.merges) != 1 || f.git.merges[0].SourceSHA != bound {
		t.Fatalf("MergeBound calls = %+v, want one of the bound commit", f.git.merges)
	}
	if f.run.Status != domain.RunStatusCompleted {
		t.Fatalf("run status = %s, want completed", f.run.Status)
	}
}
