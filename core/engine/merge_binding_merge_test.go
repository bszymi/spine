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
// refused before any push.
func TestVerifyBoundMergeRefusesOtherShapes(t *testing.T) {
	bound, base, tip, m, x := sha40('a'), sha40('b'), sha40('f'), sha40('c'), sha40('9')
	cases := []struct {
		name  string
		res   git.MergeResult
		setup func(g *fakeBinderGit)
	}{
		{"merged the tip instead", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) { g.parents[m] = []string{base, tip} }},
		{"merged the bound commit and the tip", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) { g.parents[m] = []string{base, bound, tip} }},
		{"first parent is not the base (a rewind)", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) { g.parents[m] = []string{x, bound} }},
		{"parents swapped", git.MergeResult{SHA: m, BaseSHA: base}, func(g *fakeBinderGit) { g.parents[m] = []string{bound, base} }},
		{"up to date but the base lacks the bound commit", git.MergeResult{SHA: base, BaseSHA: base}, func(g *fakeBinderGit) { g.notAncestor[bound+">"+base] = true }},
		{"fast-forward that does not descend from the base", git.MergeResult{SHA: bound, BaseSHA: base}, func(g *fakeBinderGit) { g.notAncestor[base+">"+bound] = true }},
		{"empty BaseSHA", git.MergeResult{SHA: m}, func(g *fakeBinderGit) { g.parents[m] = []string{base, bound} }},
		{"parents unreadable", git.MergeResult{SHA: m, BaseSHA: base}, func(*fakeBinderGit) {}},
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
			if f.run.Status != domain.RunStatusCompleted {
				t.Fatalf("run status = %s", f.run.Status)
			}
		})
	}
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
	bound, base, c, m := sha40('a'), sha40('b'), sha40('7'), sha40('c')
	f := committingFix(t, false, true)
	f.run.CommitMeta = map[string]string{"status": "Completed", CommitMetaBoundSHA: bound, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
	f.git.files[bound+":tasks/t.md"] = "---\nid: TASK-1\nstatus: In Progress\n---\n"
	f.git.ontoSHA = c
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
	// A failed cascade falls back to the bound commit.
	f2 := committingFix(t, false, true)
	f2.run.CommitMeta = f.run.CommitMeta
	f2.git.files[bound+":tasks/t.md"] = "---\nid: TASK-1\nstatus: In Progress\n---\n"
	f2.git.ontoErr = errors.New("object store full")
	f2.git.mergeRes = git.MergeResult{SHA: m, BaseSHA: base}
	f2.git.parents[m] = []string{base, bound}
	if err := f2.merge(); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}
	if len(f2.git.merges) != 1 || f2.git.merges[0].SourceSHA != bound {
		t.Fatalf("fallback MergeBound = %+v, want the bound commit", f2.git.merges)
	}
}
