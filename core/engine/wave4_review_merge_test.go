package engine

// INIT-023 wave 4 cross-task review, finding P2-1 (merge_inflight.go): a
// merge MergeBound put on the local main whose post-merge checks never ran
// — a MergeBound error after git moved main, or the process dying in the
// merge block — was accepted as a replay on the next attempt and pushed,
// skipping the status lifecycle's V1 and the governance post-merge
// assertion. Real git. Notes: "Cross-task review (wave 4)" on TASK-006,
// TASK-012 and TASK-017.

import (
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// lateErrCLI makes MergeBound perform the real merge and then fail (mode
// "error": a transient error, as when the trailing `rev-parse HEAD` fails
// after `git merge` committed) or panic (mode "panic": the process dies
// right after git moved main), once, on the armed attempt.
type lateErrCLI struct {
	*git.CLIClient
	mode     string
	armed    bool
	failNext int  // ResetBranch fails transiently this many times
	locked   bool // every MergeBound fails transiently, merging nothing
	// permanent: the late error is classed permanent, as git.CLIClient
	// classes a killed child's empty stderr.
	permanent bool
	// headFailsAfter: after the late error, the next ResolveBranchHead
	// calls fail this many times (the local main cannot be checked).
	headFailsAfter int
	headFail       int
}

type simulatedCrash struct{}

func (c *lateErrCLI) MergeBound(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	if c.locked {
		return git.MergeResult{}, &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "repository locked"}
	}
	res, err := c.CLIClient.MergeBound(ctx, opts)
	if err != nil || !c.armed {
		return res, err
	}
	c.armed = false
	if c.mode == "panic" {
		panic(simulatedCrash{})
	}
	c.headFail = c.headFailsAfter
	if c.permanent {
		return git.MergeResult{}, &git.GitError{Kind: git.ErrKindPermanent, Op: "merge", Message: "signal: terminated"}
	}
	return git.MergeResult{}, &git.GitError{Kind: git.ErrKindTransient, Op: "rev-parse", Message: "resource temporarily unavailable"}
}

func (c *lateErrCLI) ResolveBranchHead(ctx context.Context, branch string) (string, error) {
	if c.headFail > 0 {
		c.headFail--
		return "", &git.GitError{Kind: git.ErrKindTransient, Op: "rev-parse", Message: "resource temporarily unavailable"}
	}
	return c.CLIClient.ResolveBranchHead(ctx, branch)
}

func (c *lateErrCLI) ResetBranch(ctx context.Context, branch, to, expected string) error {
	if c.failNext > 0 {
		c.failNext--
		return &git.GitError{Kind: git.ErrKindTransient, Op: "reset_branch", Message: "repository locked"}
	}
	return c.CLIClient.ResetBranch(ctx, branch, to, expected)
}

// renameFollowing is TestGovernanceGuard_RenameFollowingIsRefusedWithARemedy's
// repository: main renames drafts/wf.yaml into workflows/task-x.yaml after
// the fork, the run edits drafts/wf.yaml into a review-less workflow, so
// only the post-merge assertion sees the governed change.
func renameFollowing(t *testing.T, mode string) (*govRepo, *lateErrCLI) {
	t.Helper()
	var w *lateErrCLI
	g := newGovRepo(t, govRepoOpts{
		mainFiles: map[string]string{"drafts/wf.yaml": govTaskDefault, "workflows/task-default.yaml": govTaskDefault, "README.md": "r\n"},
		runFiles:  map[string]string{"drafts/wf.yaml": govReviewLess},
		guard:     defaultGuard(),
		wrap: func(c *git.CLIClient) GitOperator {
			w = &lateErrCLI{CLIClient: c, mode: mode, armed: true}
			return w
		},
	})
	g.git("mv", "drafts/wf.yaml", "workflows/task-x.yaml")
	g.git("commit", "-q", "-m", "main moves the draft into workflows/")
	return g, w
}

// crashMerge drives the run to its merge and recovers the simulated crash
// of the first attempt.
func crashMerge(t *testing.T, g *govRepo) {
	t.Helper()
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(simulatedCrash); !ok {
					panic(r)
				}
			}
		}()
		g.toMerge()
	}()
}

// restarted is a fresh orchestrator over the same repository, store and
// client, with binding and the guard on: the process after a restart.
func restarted(t *testing.T, g *govRepo) *Orchestrator {
	t.Helper()
	o := &Orchestrator{
		workflows: g.orch.workflows, store: g.orch.store, actors: g.orch.actors,
		artifacts: g.orch.artifacts, events: g.events, git: g.orch.git,
		wfLoader: g.orch.wfLoader, policy: g.orch.policy,
	}
	if err := o.EnableMergeBinding(MergeBindingConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := o.WithGovernanceGuard(*defaultGuard()); err != nil {
		t.Fatal(err)
	}
	return o
}

// requireRenameRefused asserts the rename-following merge never reached
// main and the run was routed back (merge_failed), not completed.
func requireRenameRefused(t *testing.T, g *govRepo, base string) {
	t.Helper()
	if got := g.mainFile("workflows/task-x.yaml"); got == govReviewLess {
		t.Fatalf("main's workflows/task-x.yaml carries the run's unapproved review-less edit")
	}
	if g.mainHead() != base {
		t.Fatalf("main = %s, want it back at %s", g.mainHead(), base)
	}
	r := g.store.runs[govRunID]
	if r.Status == domain.RunStatusCompleted {
		t.Fatalf("the run completed with an unverified merge")
	}
	if r.Status != domain.RunStatusActive || g.latest("publish").OutcomeID != "merge_failed" {
		t.Fatalf("run %s publish %q; want the post-merge refusal's merge_failed route", r.Status, g.latest("publish").OutcomeID)
	}
	if v := r.CommitMeta[CommitMetaMergeUnverified]; v != "" {
		t.Fatalf("merge-in-flight record left: %q", v)
	}
}

// TestWave4Review_LateMergeErrorIsTakenBack (P2-1 b): MergeBound moves main
// and then reports a transient error. The merge is taken back in the
// block, unverified; the retry merges again and the post-merge assertion
// refuses it. Before, the retry found main up to date and published the
// review-less workflow.
func TestWave4Review_LateMergeErrorIsTakenBack(t *testing.T) {
	g, _ := renameFollowing(t, "error")
	base := g.mainHead()
	g.toMerge()
	r := g.store.runs[govRunID]
	if g.mainHead() != base || r.Status != domain.RunStatusCommitting {
		t.Fatalf("after the late error: main %s (base %s), run %s; want main taken back, committing", g.mainHead(), base, r.Status)
	}
	if v := r.CommitMeta[CommitMetaMergeUnverified]; v != "" {
		t.Fatalf("merge-in-flight record = %q, want cleared after a clean take-back", v)
	}
	if err := g.orch.MergeRunBranch(context.Background(), govRunID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	requireRenameRefused(t, g, base)
}

// TestWave4Review_CrashedMergeIsTakenBackOnRestart (P2-1 a): the process
// dies right after git moved main. The run carries the merge-in-flight
// record; after a restart the next attempt takes the merge back before
// anything reads main, merges again and refuses it.
func TestWave4Review_CrashedMergeIsTakenBackOnRestart(t *testing.T) {
	g, _ := renameFollowing(t, "panic")
	base := g.mainHead()
	crashMerge(t, g)
	r := g.store.runs[govRunID]
	if g.mainHead() == base {
		t.Fatal("setup: the crash should leave the merge on main")
	}
	b, s, ok := parseUnverifiedMerge(r.CommitMeta[CommitMetaMergeUnverified])
	if !ok || b != base {
		t.Fatalf("merge-in-flight record = %q, want %s <source>", r.CommitMeta[CommitMetaMergeUnverified], base)
	}
	if parents := strings.Fields(g.git("rev-list", "--parents", "-n", "1", "main")); len(parents) != 3 || parents[1] != base || parents[2] != s {
		t.Fatalf("main %v is not the recorded merge of %s onto %s", parents, s, base)
	}
	o := restarted(t, g)
	if err := o.MergeRunBranch(context.Background(), govRunID); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	requireRenameRefused(t, g, base)
}

// TestWave4Review_BuriedUnverifiedMergeFailsClosed: after the crash
// another writer commits on top of the unverified merge before the run's
// next attempt. It can no longer be taken back: the attempt is refused as
// unavailable (the run stays committing, nothing merged, the record kept,
// governed merges blocked on it) and the retry budget never abandons the
// run. Once an operator resets main so it no longer contains the merge,
// the next attempt clears the record and merges (and refuses) as usual.
func TestWave4Review_BuriedUnverifiedMergeFailsClosed(t *testing.T) {
	ctx := context.Background()
	g, _ := renameFollowing(t, "panic")
	base := g.mainHead()
	crashMerge(t, g)
	m0 := g.mainHead()
	g.writeCommit(map[string]string{"notes.txt": "a write on top\n"}, "in-place write after the restart")
	o := restarted(t, g)
	err := o.MergeRunBranch(ctx, govRunID)
	requireSpineCode(t, err, domain.ErrUnavailable)
	r := g.store.runs[govRunID]
	if r.Status != domain.RunStatusCommitting || r.CommitMeta[CommitMetaMergeUnverified] == "" {
		t.Fatalf("run %s record %q; want committing with the record kept", r.Status, r.CommitMeta[CommitMetaMergeUnverified])
	}
	if got := r.CommitMeta[CommitMetaGovernanceBlockedTarget]; got != m0+" "+base {
		t.Fatalf("blocked target = %q, want %s %s", got, m0, base)
	}
	w4ExhaustedOn(t, g.store.mockRunStore, r)
	if abandoned, err := o.AbandonExhaustedMerge(ctx, govRunID, 3); err != nil || abandoned {
		t.Fatalf("AbandonExhaustedMerge = %v, %v; want the run kept committing", abandoned, err)
	}
	g.git("reset", "-q", "--hard", base)
	if err := o.MergeRunBranch(ctx, govRunID); err != nil {
		t.Fatalf("after the operator's reset: %v", err)
	}
	requireRenameRefused(t, g, base)
}

// TestWave4Review_FailedLateTakeBackStaysCommitting: the late error's
// take-back fails too. The run stays committing (transient, never the
// permanent route), the record is kept and governed merges are blocked on
// the merge; the next attempt takes it back first and then refuses the
// merge as usual.
func TestWave4Review_FailedLateTakeBackStaysCommitting(t *testing.T) {
	g, w := renameFollowing(t, "error")
	w.failNext = 1
	base := g.mainHead()
	g.toMerge()
	r := g.store.runs[govRunID]
	if r.Status != domain.RunStatusCommitting || r.CommitMeta[CommitMetaMergeUnverified] == "" {
		t.Fatalf("run %s record %q; want committing with the record kept", r.Status, r.CommitMeta[CommitMetaMergeUnverified])
	}
	if o := g.primaryOutcome(); o == nil || !o.FailureClass.IsTransient() {
		t.Fatalf("outcome %+v, want a transient failure", o)
	}
	if r.CommitMeta[CommitMetaGovernanceBlockedTarget] == "" {
		t.Fatal("no governance block recorded for the merge that could not be taken back")
	}
	if err := g.orch.MergeRunBranch(context.Background(), govRunID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	requireRenameRefused(t, g, base)
}

// TestWave4Review_UncheckedLateMergeStaysCommitting: MergeBound moves main
// and then fails permanently (a killed git child: empty stderr), and the
// local main cannot be read right after, so the block cannot tell whether
// the merge is there. The run stays committing (transient) with the record
// kept, instead of taking the permanent route with an unverified merge
// possibly on main; the next attempt takes the merge back first and
// refuses it.
func TestWave4Review_UncheckedLateMergeStaysCommitting(t *testing.T) {
	g, w := renameFollowing(t, "error")
	w.permanent, w.headFailsAfter = true, 1
	base := g.mainHead()
	g.toMerge()
	r := g.store.runs[govRunID]
	if r.Status != domain.RunStatusCommitting || r.CommitMeta[CommitMetaMergeUnverified] == "" {
		t.Fatalf("run %s record %q; want committing with the record kept", r.Status, r.CommitMeta[CommitMetaMergeUnverified])
	}
	if o := g.primaryOutcome(); o == nil || !o.FailureClass.IsTransient() {
		t.Fatalf("outcome %+v, want a transient failure", o)
	}
	if g.mainHead() == base {
		t.Fatal("setup: the unchecked merge should still be on main")
	}
	if err := g.orch.MergeRunBranch(context.Background(), govRunID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	requireRenameRefused(t, g, base)
}

// TestWave4Review_LateMergeErrorV1 (P2-1, status lifecycle): the
// criss-cross whose merge resolution reopens a Completed task the run does
// not own. MergeBound merges and then fails; the merge is taken back and
// the retry's V1 refuses it, so main's TASK-002 stays Completed.
func TestWave4Review_LateMergeErrorV1(t *testing.T) {
	seed := map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending"), lcSibling: lcArt("TASK-002", "Task", "Pending")}
	r, _ := lcCrissCross(t, seed,
		func(r *lcRepo) { r.write(lcSibling, lcArt("TASK-002", "Task", "Completed")) },
		func(r *lcRepo) { r.write(lcSibling, lcArt("TASK-002", "Task", "Pending")) }, lcDate(1))
	e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
	w := &lateErrCLI{CLIClient: r.cli, mode: "error", armed: true}
	e.orch.git = &lcLateErr{countingCLI: e.git, late: w}
	if err := e.orch.EnableMergeBinding(MergeBindingConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := e.orch.MergeRunBranch(context.Background(), lcRunID); err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	if r.head("main") != e.main0 || e.run.Status != domain.RunStatusCommitting {
		t.Fatalf("after the late error: main moved %v, run %s", r.head("main") != e.main0, e.run.Status)
	}
	e.requireMergeResultRefused(lcSibling)
	if got := r.statusAt("main", lcSibling); got != "Completed" {
		t.Fatalf("main's TASK-002 = %s, want Completed", got)
	}
}

// lcLateErr is lcEnv's counting client with lateErrCLI's MergeBound.
type lcLateErr struct {
	*countingCLI
	late *lateErrCLI
}

func (c *lcLateErr) MergeBound(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	c.mu.Lock()
	c.merges++
	c.mu.Unlock()
	return c.late.MergeBound(ctx, opts)
}

// TestWave4Review_ApprovedMergeCrashBeforeItsMarker (P2-1, finding case
// 3): the approved merge's process dies after git moved main, before the
// consumption marker. After the restart the merge is taken back, merged
// again and recorded with its marker, and only then pushed: the approved
// change keeps its provenance.
func TestWave4Review_ApprovedMergeCrashBeforeItsMarker(t *testing.T) {
	var w *lateErrCLI
	g := newGovRepo(t, govRepoOpts{
		runFiles: map[string]string{"workflows/task-default.yaml": govReviewLess},
		guard:    defaultGuard(),
		merger:   true,
		wrap:     func(c *git.CLIClient) GitOperator { w = &lateErrCLI{CLIClient: c, mode: "panic"}; return w },
	})
	g.toMerge()
	g.requireHeld("held for task-default.yaml")
	w.armed = true
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(simulatedCrash); !ok {
					panic(r)
				}
			}
		}()
		_, _ = g.decide(GovernanceApprove, g.runHead(), govOperator, "")
	}()
	r := g.store.runs[govRunID]
	if r.CommitMeta[domain.CommitMetaGovernanceApprovalMergeSHA] != "" || r.CommitMeta[CommitMetaMergeUnverified] == "" {
		t.Fatalf("setup: marker %q record %q; want no marker and the record", r.CommitMeta[domain.CommitMetaGovernanceApprovalMergeSHA], r.CommitMeta[CommitMetaMergeUnverified])
	}
	o := restarted(t, g)
	if err := o.MergeRunBranch(context.Background(), govRunID); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	if r.Status != domain.RunStatusCompleted {
		t.Fatalf("run %s, want completed", r.Status)
	}
	if got := r.CommitMeta[domain.CommitMetaGovernanceApprovalMergeSHA]; got != g.mainHead() {
		t.Fatalf("consumption marker %q, want the merge on main %s", got, g.mainHead())
	}
	if g.mainFile("workflows/task-default.yaml") != govReviewLess {
		t.Fatal("the approved change did not land")
	}
	if v := r.CommitMeta[CommitMetaMergeUnverified]; v != "" {
		t.Fatalf("the accepted merge left its merge-in-flight record: %q", v)
	}
}

// TestWave4Review_ReplayWithoutARecordIsStillAccepted: a run whose
// commits are already on main and that carries no merge-in-flight record
// (here an operator landed the run branch) replays as before — the
// record changes nothing for a legitimate replay.
func TestWave4Review_ReplayWithoutARecordIsStillAccepted(t *testing.T) {
	g := newGovRepo(t, govRepoOpts{runFiles: map[string]string{"src/a.go": "package a\n"}, guard: defaultGuard()})
	g.git("merge", "-q", "--no-ff", "-m", "operator lands the run branch", g.branch)
	landed := g.mainHead()
	g.toMerge()
	r := g.store.runs[govRunID]
	if r.Status != domain.RunStatusCompleted || g.mainHead() != landed {
		t.Fatalf("run %s main %s (landed %s); want the replay accepted", r.Status, g.mainHead(), landed)
	}
	if v, ok := r.CommitMeta[CommitMetaMergeUnverified]; ok && v != "" {
		t.Fatalf("a replay wrote a merge-in-flight record: %q", v)
	}
}

// w4ExhaustedOn records a spent primary outcome on a mockRunStore.
func w4ExhaustedOn(t *testing.T, s *mockRunStore, r *domain.Run) {
	t.Helper()
	f := &govFake{t: t, run: r, store: &govStore{mockRunStore: s}}
	w4Exhausted(t, f)
}

// TestWave4Review_RetryBudgetSparesAnUnverifiedMerge: with the status
// lifecycle on and the guard off (no governance block to fall back on), a
// run whose merge-in-flight record is set is not abandoned — its next
// attempt takes the merge back first — and once the record is cleared the
// budget applies.
func TestWave4Review_RetryBudgetSparesAnUnverifiedMerge(t *testing.T) {
	ctx := context.Background()
	f := newW4Fake(t, nil, true, w4Modified(w4Task)...)
	f.run.CommitMeta[CommitMetaMergeUnverified] = gBase + " " + gBound
	w4Exhausted(t, f)
	abandoned, err := f.orch.AbandonExhaustedMerge(ctx, f.run.RunID, 3)
	if err != nil || abandoned || f.run.Status != domain.RunStatusCommitting {
		t.Fatalf("AbandonExhaustedMerge = %v, %v, run %s; want the run kept committing", abandoned, err, f.run.Status)
	}
	f.run.CommitMeta[CommitMetaMergeUnverified] = ""
	abandoned, err = f.orch.AbandonExhaustedMerge(ctx, f.run.RunID, 3)
	if err != nil || !abandoned {
		t.Fatalf("after the record cleared: AbandonExhaustedMerge = %v, %v; want abandoned", abandoned, err)
	}
}
