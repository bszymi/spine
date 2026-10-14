package engine

// INIT-023 TASK-006 unit tests over the fake binder (TASK-005's
// fakeBinderGit, extended with MergeBases): who may decide (AC 6), state
// checks (AC 8), fail-closed evaluation (AC 11), the idempotent hold
// (AC 12), no side effects while held (AC 15), the consumption marker
// (AC 16), the binding prerequisite and decidable holds (AC 29), and the
// reject/approve race (AC 34).

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
)

// govGit is fakeBinderGit with MergeBases and programmable Diff errors.
type govGit struct {
	*fakeBinderGit
	bases    map[string][]string // "a b" → bases; default [sha40('0')]
	basesErr error
	diffErr  map[string]error // "a b" → Diff error
}

func (g *govGit) MergeBases(_ context.Context, a, b string) ([]string, error) {
	g.record("MergeBases " + a + " " + b)
	if g.basesErr != nil {
		return nil, g.basesErr
	}
	if v, ok := g.bases[a+" "+b]; ok {
		return v, nil
	}
	return []string{sha40('0')}, nil
}

func (g *govGit) Diff(ctx context.Context, a, b string) ([]git.FileDiff, error) {
	if err := g.diffErr[a+" "+b]; err != nil {
		g.record("Diff " + a + " " + b)
		return nil, err
	}
	return g.fakeBinderGit.Diff(ctx, a, b)
}

const (
	gBound = "1111111111111111111111111111111111111111"
	gBase  = "2222222222222222222222222222222222222222"
	gMerge = "3333333333333333333333333333333333333333"
)

type govFake struct {
	t      *testing.T
	g      *govGit
	store  *govStore
	merger *govMergerStore
	events *mockEventEmitter
	orch   *Orchestrator
	run    *domain.Run
	wf     *domain.WorkflowDefinition
}

// newGovFake builds a committing run bound at gBound whose diff from the
// merge base changes paths, with the guard on (unless guard is nil) and
// push enabled. The merge commit gMerge = [gBase, gBound] changes the
// same paths.
func newGovFake(t *testing.T, wf *domain.WorkflowDefinition, guard *GovernanceGuard, merger bool, paths ...string) *govFake {
	t.Helper()
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	run := &domain.Run{
		RunID: bindRunID, TraceID: govTrace, Status: domain.RunStatusCommitting,
		TaskPath: "tasks/t.md", WorkflowPath: wf.Path, WorkflowID: wf.ID, WorkflowVersion: "v1",
		BranchName: "spine/run/" + bindRunID, CurrentStepID: "publish",
		CommitMeta: map[string]string{
			CommitMetaBoundSHA: gBound,
			CommitMetaBoundBy:  boundByStep("review", bindRunID+"-review-1"),
		},
	}
	steps := []*domain.StepExecution{
		{ExecutionID: bindRunID + "-execute-1", RunID: bindRunID, StepID: "execute", Status: domain.StepStatusCompleted, ActorID: "author", Attempt: 1},
		{ExecutionID: bindRunID + "-review-1", RunID: bindRunID, StepID: "review", Status: domain.StepStatusCompleted, ActorID: "reviewer", Attempt: 1},
	}
	if findStepDef(wf, "publish") != nil {
		steps = append(steps, &domain.StepExecution{ExecutionID: bindRunID + "-publish-1", RunID: bindRunID,
			StepID: "publish", Status: domain.StepStatusInProgress, ActorID: EngineMergeActorID, Attempt: 1})
	}
	f := &govFake{t: t, run: run, wf: wf, events: &mockEventEmitter{}}
	f.store = newGovStore(run, steps...)
	var st RunStore = f.store
	if merger {
		f.merger = &govMergerStore{govStore: f.store}
		st = f.merger
	}
	f.g = &govGit{fakeBinderGit: newFakeBinderGit(), bases: map[string][]string{}, diffErr: map[string]error{}}
	f.g.setHead(run.BranchName, gBound)
	var diffs []git.FileDiff
	for _, p := range paths {
		diffs = append(diffs, git.FileDiff{Path: p, Status: "modified"})
	}
	f.g.diffs[sha40('0')+" "+gBound] = diffs
	f.g.diffs[gBase+" "+gMerge] = diffs
	f.g.mergeRes = git.MergeResult{SHA: gMerge, BaseSHA: gBase}
	f.g.parents[gMerge] = []string{gBase, gBound}
	f.orch = &Orchestrator{
		workflows: &mockWorkflowResolver{}, store: st, actors: &mockActorAssigner{},
		artifacts: &mockArtifactReader{}, events: f.events, git: f.g,
		wfLoader: &mockWorkflowLoader{wfDef: wf}, policy: branchprotect.NewPermissive(),
	}
	if err := f.orch.EnableMergeBinding(MergeBindingConfig{}); err != nil {
		t.Fatal(err)
	}
	if guard != nil {
		if err := f.orch.WithGovernanceGuard(*guard); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *govFake) merge() error { return f.orch.MergeRunBranch(context.Background(), f.run.RunID) }

func (f *govFake) decide(d GovernanceDecision, sha string, a domain.Actor, reason string) (*GovernanceDecisionResult, error) {
	return f.orch.DecideGovernanceMerge(context.Background(), GovernanceDecisionRequest{
		RunID: f.run.RunID, CommitSHA: sha, Decision: d, Actor: a, Reason: reason})
}

func (f *govFake) eventsOf(t domain.EventType) []domain.Event {
	var out []domain.Event
	for i := range f.events.events {
		if f.events.events[i].Type == t {
			out = append(out, f.events.events[i])
		}
	}
	return out
}

func (f *govFake) step(id string) *domain.StepExecution {
	var out *domain.StepExecution
	for _, e := range f.store.createdSteps {
		if e.StepID == id && (out == nil || e.Attempt >= out.Attempt) {
			out = e
		}
	}
	return out
}

func copyMeta(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func requireHoldErr(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrGovernanceApprovalRequired) {
		t.Fatalf("err = %v, want a hold (ErrGovernanceApprovalRequired)", err)
	}
	requireSpineCode(t, err, domain.ErrPrecondition)
}

var govPaths = []string{".spine/branch-protection.yaml", "workflows/task-default.yaml"}

// TestWithGovernanceGuard_Prerequisites is AC 29's first part: binding
// off, or an operator without MergeBases, refuses to enable the guard and
// leaves it off; invalid paths are refused; Enabled:false turns it off.
func TestWithGovernanceGuard_Prerequisites(t *testing.T) {
	fb := newFakeBinderGit() // a CommitBinder without MergeBases
	o := &Orchestrator{git: fb}
	if err := o.WithGovernanceGuard(GovernanceGuard{Enabled: true}); err == nil || o.GovernanceGuardEnabled() {
		t.Fatalf("binding off: err %v, enabled %v; want an error and the guard off", err, o.GovernanceGuardEnabled())
	}
	if err := o.EnableMergeBinding(MergeBindingConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := o.WithGovernanceGuard(GovernanceGuard{Enabled: true}); err == nil || o.GovernanceGuardEnabled() {
		t.Fatalf("no MergeBases: err %v, enabled %v; want an error and the guard off", err, o.GovernanceGuardEnabled())
	}
	// Binding off with an operator that can list merge bases: still refused
	// (I2 needs the bound commit).
	gNoBind := &govGit{fakeBinderGit: newFakeBinderGit()}
	oNoBind := &Orchestrator{git: gNoBind}
	if err := oNoBind.WithGovernanceGuard(GovernanceGuard{Enabled: true}); err == nil || oNoBind.GovernanceGuardEnabled() {
		t.Fatalf("binding off with MergeBases: err %v, enabled %v; want an error and the guard off", err, oNoBind.GovernanceGuardEnabled())
	}
	g := &govGit{fakeBinderGit: newFakeBinderGit()}
	o2 := &Orchestrator{git: g}
	if err := o2.EnableMergeBinding(MergeBindingConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := o2.WithGovernanceGuard(GovernanceGuard{Enabled: true, Paths: []string{"../x"}}); err == nil || o2.GovernanceGuardEnabled() {
		t.Fatalf("invalid path: err %v, enabled %v", err, o2.GovernanceGuardEnabled())
	}
	cfg := GovernanceGuard{Enabled: true, WorkflowDirs: []string{"workflows/", "spine/workflows/"}, ExemptOwnRunEvidence: true}
	if err := o2.WithGovernanceGuard(cfg); err != nil || !o2.GovernanceGuardEnabled() {
		t.Fatalf("valid config: %v", err)
	}
	got, ok := o2.GovernanceGuardConfig()
	if !ok || strings.Join(got.WorkflowDirs, ",") != "workflows/,spine/workflows/" || !got.ExemptOwnRunEvidence {
		t.Fatalf("config = %+v %v", got, ok)
	}
	if err := o2.WithGovernanceGuard(GovernanceGuard{}); err != nil || o2.GovernanceGuardEnabled() {
		t.Fatalf("Enabled:false did not turn the guard off: %v", err)
	}
}

// TestGovernanceDecision_WhoMayApprove is AC 6. Mutation: removing the
// role check lets a contributor approve.
func TestGovernanceDecision_WhoMayApprove(t *testing.T) {
	for _, a := range []domain.Actor{
		{ActorID: "c", Role: domain.RoleContributor},
		{ActorID: "r", Role: domain.RoleReviewer},
		{ActorID: "x"},
		{Role: domain.RoleAdmin},
	} {
		f := newGovFake(t, publishWorkflow(), defaultGuard(), true, govPaths...)
		before := copyMeta(f.run.CommitMeta)
		_, err := f.decide(GovernanceApprove, gBound, a, "")
		requireSpineCode(t, err, domain.ErrForbidden)
		if len(f.merger.mergeCalls) != 0 || len(governanceKeys(f.run.CommitMeta)) != 0 || len(f.run.CommitMeta) != len(before) {
			t.Fatalf("%+v: commit_meta written on a refused decision: %v", a, f.run.CommitMeta)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("%+v: events emitted on a refused decision", a)
		}
	}
	for _, a := range []domain.Actor{govOperator, govAdmin} {
		f := newGovFake(t, publishWorkflow(), defaultGuard(), true, govPaths...)
		res, err := f.decide(GovernanceApprove, gBound, a, "")
		if err != nil || res.MergeError != "" || f.run.Status != domain.RunStatusCompleted {
			t.Fatalf("%s approve: %v %+v (run %s)", a.Role, err, res, f.run.Status)
		}
		if got := f.run.CommitMeta[domain.CommitMetaGovernanceApprovalActorRole]; got != string(a.Role) {
			t.Fatalf("recorded role %q, want %q", got, a.Role)
		}
	}
}

// TestGovernanceDecision_StateChecks is AC 8 (and AC 7's direct SHA case).
func TestGovernanceDecision_StateChecks(t *testing.T) {
	type tc struct {
		name  string
		setup func(*govFake)
		req   GovernanceDecisionRequest
		code  domain.ErrorCode
		paths []string
		guard *GovernanceGuard
	}
	approve := func(sha string) GovernanceDecisionRequest {
		return GovernanceDecisionRequest{CommitSHA: sha, Decision: GovernanceApprove, Actor: govOperator}
	}
	for _, c := range []tc{
		{name: "guard off", req: approve(gBound), code: domain.ErrUnavailable, paths: govPaths},
		{name: "not committing", setup: func(f *govFake) { f.run.Status = domain.RunStatusActive },
			req: approve(gBound), code: domain.ErrConflict, paths: govPaths, guard: defaultGuard()},
		{name: "no governed paths", req: approve(gBound), code: domain.ErrPrecondition, paths: []string{"src/a.go"}, guard: defaultGuard()},
		{name: "malformed sha", req: approve("abc123"), code: domain.ErrInvalidParams, paths: govPaths, guard: defaultGuard()},
		{name: "sha with a ref", req: approve("refs/heads/main"), code: domain.ErrInvalidParams, paths: govPaths, guard: defaultGuard()},
		{name: "another sha", req: approve(strings.Repeat("9", 40)), code: domain.ErrConflict, paths: govPaths, guard: defaultGuard()},
		{name: "reject without reason", req: GovernanceDecisionRequest{CommitSHA: gBound, Decision: GovernanceReject, Actor: govOperator, Reason: "  "},
			code: domain.ErrInvalidParams, paths: govPaths, guard: defaultGuard()},
		{name: "reason too long", req: GovernanceDecisionRequest{CommitSHA: gBound, Decision: GovernanceReject, Actor: govOperator,
			Reason: strings.Repeat("x", MaxGovernanceDecisionReason+1)}, code: domain.ErrInvalidParams, paths: govPaths, guard: defaultGuard()},
		{name: "unknown decision", req: GovernanceDecisionRequest{CommitSHA: gBound, Decision: "merge", Actor: govOperator},
			code: domain.ErrInvalidParams, paths: govPaths, guard: defaultGuard()},
		{name: "unknown run", setup: func(f *govFake) { delete(f.store.runs, bindRunID) },
			req: approve(gBound), code: domain.ErrNotFound, paths: govPaths, guard: defaultGuard()},
		{name: "unevaluable", setup: func(f *govFake) { f.g.basesErr = errors.New("git down") },
			req: approve(gBound), code: domain.ErrUnavailable, paths: govPaths, guard: defaultGuard()},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newGovFake(t, publishWorkflow(), c.guard, true, c.paths...)
			if c.setup != nil {
				c.setup(f)
			}
			req := c.req
			req.RunID = bindRunID
			_, err := f.orch.DecideGovernanceMerge(context.Background(), req)
			requireSpineCode(t, err, c.code)
			if len(f.merger.mergeCalls) != 0 || len(f.events.events) != 0 {
				t.Fatalf("a refused decision wrote %v / emitted %d events", f.merger.mergeCalls, len(f.events.events))
			}
		})
	}
}

// TestGovernanceGuard_FailsClosed is AC 11 with the design check's
// post-merge cases: a MergeBases or Diff error refuses (unavailable, not a
// hold) without merging; after the merge, a Diff error refuses without a
// push and takes the local target back to the base.
func TestGovernanceGuard_FailsClosed(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*govFake)
	}{
		{"merge bases error", func(f *govFake) {
			f.g.basesErr = &git.GitError{Kind: git.ErrKindPermanent, Op: "merge-base", Message: "boom"}
		}},
		{"no merge base", func(f *govFake) { f.g.bases["main "+gBound] = []string{} }},
		{"diff error", func(f *govFake) { f.g.diffErr[sha40('0')+" "+gBound] = errors.New("diff failed") }},
		{"malformed merge base", func(f *govFake) { f.g.bases["main "+gBound] = []string{"HEAD"} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newGovFake(t, publishWorkflow(), defaultGuard(), true, govPaths...)
			c.setup(f)
			err := f.merge()
			if err == nil || errors.Is(err, ErrGovernanceApprovalRequired) {
				t.Fatalf("err = %v, want a refusal that is not a hold", err)
			}
			requireSpineCode(t, err, domain.ErrUnavailable)
			if n := f.g.callCount("binder:MergeBound"); n != 0 {
				t.Fatalf("MergeBound called %d times", n)
			}
			if f.run.Status != domain.RunStatusCommitting {
				t.Fatalf("run = %s, want committing", f.run.Status)
			}
			if len(f.eventsOf(domain.EventGovernanceMergeHeld)) != 0 || f.store.holdWrites != 0 {
				t.Fatal("an unevaluable merge was recorded as a hold")
			}
		})
	}
	for _, c := range []struct {
		name      string
		err       error
		permanent bool
	}{
		{"post-merge diff error, permanent", &git.GitError{Kind: git.ErrKindPermanent, Op: "diff", Message: "bad object"}, true},
		{"post-merge diff error, transient", &git.GitError{Kind: git.ErrKindTransient, Op: "diff", Message: "index.lock"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newGovFake(t, publishWorkflow(), defaultGuard(), true, govPaths...)
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalSHA] = gBound
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalActorID] = "op-1"
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalActorRole] = "operator"
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalApprovedAt] = "2026-10-14T00:00:00Z"
			f.g.diffErr[gBase+" "+gMerge] = c.err
			_ = f.merge()
			if n := len(f.g.pushes); n != 0 {
				t.Fatalf("pushed %v", f.g.pushes)
			}
			if len(f.g.resets) != 1 || f.g.resets[0] != "main "+gBase+" "+gMerge {
				t.Fatalf("take-back = %v, want main restored to the base", f.g.resets)
			}
			if _, ok := f.run.CommitMeta[domain.CommitMetaGovernanceApprovalMergeSHA]; ok {
				t.Fatal("a refused merge recorded the consumption marker")
			}
			if c.permanent {
				if f.run.Status != domain.RunStatusActive || f.step("publish").OutcomeID != "merge_failed" {
					t.Fatalf("permanent: run %s, publish %s; want routed back", f.run.Status, f.step("publish").OutcomeID)
				}
			} else if f.run.Status != domain.RunStatusCommitting {
				t.Fatalf("transient: run %s, want committing (retry)", f.run.Status)
			}
		})
	}
}

// TestGovernanceGuard_IdempotentHold is AC 12: three attempts on a held
// run emit governance.merge_held under one event id (the episode is the
// publish execution) and write the hold to the step once; on the
// terminal-commit path the hold is on the last completed step.
func TestGovernanceGuard_IdempotentHold(t *testing.T) {
	f := newGovFake(t, publishWorkflow(), defaultGuard(), true, govPaths...)
	for i := 0; i < 3; i++ {
		requireHoldErr(t, f.merge())
	}
	held := f.eventsOf(domain.EventGovernanceMergeHeld)
	if len(held) != 3 {
		t.Fatalf("merge_held emits = %d, want 3 (one id)", len(held))
	}
	want := "evt-" + govTrace[:12] + "-governance-held-" + gBound[:12] + "-" + bindRunID + "-publish-1"
	for _, e := range held {
		if e.EventID != want {
			t.Fatalf("event id %q, want %q", e.EventID, want)
		}
	}
	if f.store.holdWrites != 1 {
		t.Fatalf("hold written %d times, want once", f.store.holdWrites)
	}
	if d := f.step("publish").ErrorDetail; d == nil || d.Classification != domain.FailureGovernanceApprovalRequired || d.StepID != "publish" {
		t.Fatalf("publish detail = %+v", d)
	}

	l := newGovFake(t, legacyWorkflow(), defaultGuard(), true, govPaths...)
	requireHoldErr(t, l.merge())
	requireHoldErr(t, l.merge())
	if d := l.step("review").ErrorDetail; d == nil || d.Classification != domain.FailureGovernanceApprovalRequired {
		t.Fatalf("terminal-commit path: last completed step detail = %+v", d)
	}
	if l.store.holdWrites != 1 {
		t.Fatalf("terminal-commit hold written %d times, want once", l.store.holdWrites)
	}
}

// countingClients counts RepositoryGitClients.Client calls.
type countingClients struct {
	mu sync.Mutex
	n  int
}

func (c *countingClients) Client(context.Context, string) (git.GitClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return nil, errors.New("not reachable while held")
}

// TestGovernanceGuard_NoSideEffectsWhileHeld is AC 15: a held run with a
// code repository never reaches the code-repository merge, and nothing is
// committed or checked out on the run branch.
func TestGovernanceGuard_NoSideEffectsWhileHeld(t *testing.T) {
	f := newGovFake(t, publishWorkflow(), defaultGuard(), true, govPaths...)
	f.run.AffectedRepositories = []string{domain.PrimaryRepositoryID, "code-a"}
	f.run.CommitMeta[RepoBoundSHAKey("code-a")] = sha40('c')
	f.run.CommitMeta["status"] = "Completed"
	f.g.files[gBound+":tasks/t.md"] = "---\nid: T\nstatus: Pending\n---\n"
	f.g.ontoSHA = sha40('d')
	f.g.parents[sha40('d')] = []string{gBound}
	f.g.diffs[sha40('0')+" "+sha40('d')] = []git.FileDiff{{Path: ".spine/x.yaml", Status: "added"}}
	clients := &countingClients{}
	f.orch.WithRepositoryGitClients(clients)
	requireHoldErr(t, f.merge())
	if clients.n != 0 {
		t.Fatalf("code repository clients resolved %d times while held", clients.n)
	}
	for _, c := range []string{"Commit", "Checkout", "WriteAndStageFile", "binder:MergeBound", "binder:PushCommit"} {
		if n := f.g.callCount(c); n != 0 {
			t.Fatalf("%s called %d times while held", c, n)
		}
	}
}

// TestGovernanceGuard_ConsumptionMarker is AC 16.
func TestGovernanceGuard_ConsumptionMarker(t *testing.T) {
	t.Run("approved merge writes it before the push", func(t *testing.T) {
		f := newGovFake(t, publishWorkflow(), defaultGuard(), true, govPaths...)
		var pushedAfterMarker bool
		f.store.beforeWrite = func(string) { pushedAfterMarker = len(f.g.pushes) == 0 }
		if _, err := f.decide(GovernanceApprove, gBound, govOperator, ""); err != nil {
			t.Fatal(err)
		}
		k := governanceKeys(f.run.CommitMeta)
		if k[domain.CommitMetaGovernanceApprovalMergeSHA] != gMerge || k[domain.CommitMetaGovernanceApprovalBaseSHA] != gBase {
			t.Fatalf("marker = %v", k)
		}
		if !pushedAfterMarker || len(f.g.pushes) != 1 {
			t.Fatalf("marker written after the push (pushes %v)", f.g.pushes)
		}
	})
	t.Run("ungoverned merge writes no governance keys", func(t *testing.T) {
		f := newGovFake(t, publishWorkflow(), defaultGuard(), true, "src/a.go")
		if err := f.merge(); err != nil || f.run.Status != domain.RunStatusCompleted {
			t.Fatalf("merge: %v (%s)", err, f.run.Status)
		}
		if k := governanceKeys(f.run.CommitMeta); len(k) != 0 || len(f.merger.mergeCalls) != 0 {
			t.Fatalf("governance keys written: %v", k)
		}
	})
	t.Run("marker write failure keeps the run committing", func(t *testing.T) {
		f := newGovFake(t, publishWorkflow(), defaultGuard(), true, govPaths...)
		if _, err := f.decide(GovernanceApprove, gBound, govOperator, ""); err != nil {
			t.Fatal(err)
		}
		// reset to a fresh committing run with the approval recorded and
		// a failing atomic write
		g := newGovFake(t, publishWorkflow(), defaultGuard(), true, govPaths...)
		for k, v := range governanceKeys(f.run.CommitMeta) {
			if k != domain.CommitMetaGovernanceApprovalMergeSHA && k != domain.CommitMetaGovernanceApprovalBaseSHA {
				g.run.CommitMeta[k] = v
			}
		}
		g.merger.mergeErr = errors.New("database down")
		err := g.merge()
		if err == nil || g.run.Status != domain.RunStatusCommitting {
			t.Fatalf("err %v, run %s; want an error and committing", err, g.run.Status)
		}
		if len(g.g.pushes) != 0 || len(g.g.resets) != 1 {
			t.Fatalf("pushes %v resets %v; want no push and the merge taken back", g.g.pushes, g.g.resets)
		}
	})
}

// TestGovernanceGuard_MissingBindingIsNotAHold is AC 29's second part: a
// committing run without a trusted binding takes TASK-005's
// bindingFailure route — no governance hold detail, no merge_held event.
func TestGovernanceGuard_MissingBindingIsNotAHold(t *testing.T) {
	for _, mutate := range []func(map[string]string){
		func(m map[string]string) { delete(m, CommitMetaBoundSHA); delete(m, CommitMetaBoundBy) },
		func(m map[string]string) { m[CommitMetaBoundSHA] = "main" },
		func(m map[string]string) { m[CommitMetaBoundBy] = "step:review:run-other-review-1" },
	} {
		f := newGovFake(t, publishWorkflow(), defaultGuard(), true, govPaths...)
		mutate(f.run.CommitMeta)
		_ = f.merge()
		if errorsHeld := len(f.eventsOf(domain.EventGovernanceMergeHeld)); errorsHeld != 0 || f.store.holdWrites != 0 {
			t.Fatalf("a missing binding became a hold")
		}
		if f.run.Status != domain.RunStatusActive || f.step("publish").OutcomeID != "merge_failed" {
			t.Fatalf("run %s publish %q, want TASK-005's merge_failed route", f.run.Status, f.step("publish").OutcomeID)
		}
	}
}

// TestGovernanceGuard_EveryHoldIsDecidable is AC 29's table: whatever the
// guard decides, whenever a run is left committing with a hold, both
// approve and reject accept its bound commit.
func TestGovernanceGuard_EveryHoldIsDecidable(t *testing.T) {
	type tc struct {
		name  string
		wf    func() *domain.WorkflowDefinition
		paths []string
		setup func(*govFake)
	}
	cases := []tc{
		{"publish, governed", publishWorkflow, govPaths, nil},
		{"legacy, governed", legacyWorkflow, govPaths, nil},
		{"stale approval", publishWorkflow, govPaths, func(f *govFake) {
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalSHA] = sha40('9')
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalActorID] = "op-1"
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalActorRole] = "operator"
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalApprovedAt] = "2026-10-14T00:00:00Z"
		}},
		{"reviewer approval only", publishWorkflow, govPaths, func(f *govFake) {
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalSHA] = gBound
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalActorID] = "rev"
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalActorRole] = "reviewer"
			f.run.CommitMeta[domain.CommitMetaGovernanceApprovalApprovedAt] = "2026-10-14T00:00:00Z"
		}},
		{"cascade footprint", legacyWorkflow, nil, func(f *govFake) {
			f.run.CommitMeta["status"] = "Completed"
			f.g.files[gBound+":tasks/t.md"] = "---\nid: T\nstatus: Pending\n---\n"
			f.g.ontoSHA = sha40('d')
			f.g.parents[sha40('d')] = []string{gBound}
			f.g.diffs[sha40('0')+" "+sha40('d')] = []git.FileDiff{{Path: ".spine/tasks/t.md", Status: "modified"}}
		}},
		{"two merge bases", publishWorkflow, nil, func(f *govFake) {
			f.g.bases["main "+gBound] = []string{sha40('a'), sha40('b')}
			f.g.diffs[sha40('b')+" "+gBound] = []git.FileDiff{{Path: ".spine/branch-protection.yaml", Status: "modified"}}
		}},
	}
	for _, c := range cases {
		for _, d := range []GovernanceDecision{GovernanceApprove, GovernanceReject} {
			t.Run(c.name+"/"+string(d), func(t *testing.T) {
				f := newGovFake(t, c.wf(), defaultGuard(), true, c.paths...)
				if c.setup != nil {
					c.setup(f)
				}
				requireHoldErr(t, f.merge())
				if f.run.Status != domain.RunStatusCommitting {
					t.Fatalf("held run = %s", f.run.Status)
				}
				if _, err := f.decide(d, gBound, govOperator, "decided"); err != nil {
					t.Fatalf("%s of a held run refused: %v", d, err)
				}
			})
		}
	}
}

// TestGovernanceDecision_RejectLosesTheRace is AC 34: on a workflow with
// no publish step, an approve that completes the merge lands between the
// reject's checks and its transition; the reject returns ErrConflict, the
// run stays completed and no run_failed event is emitted. Mutation:
// reverting the compare-and-swap to UpdateRunStatus fails the run.
func TestGovernanceDecision_RejectLosesTheRace(t *testing.T) {
	f := newGovFake(t, legacyWorkflow(), defaultGuard(), true, govPaths...)
	requireHoldErr(t, f.merge())
	once := sync.Once{}
	f.store.beforeWrite = func(string) {
		once.Do(func() {
			f.store.beforeWrite = nil
			if _, err := f.decide(GovernanceApprove, gBound, govAdmin, ""); err != nil {
				t.Errorf("concurrent approve: %v", err)
			}
		})
	}
	_, err := f.decide(GovernanceReject, gBound, govOperator, "too late")
	requireSpineCode(t, err, domain.ErrConflict)
	if f.run.Status != domain.RunStatusCompleted {
		t.Fatalf("run = %s, want completed", f.run.Status)
	}
	if n := len(f.eventsOf(domain.EventRunFailed)); n != 0 {
		t.Fatalf("run_failed emitted %d times", n)
	}
	for _, e := range f.eventsOf(domain.EventGovernanceMergeDecided) {
		if strings.Contains(string(e.Payload), `"decision":"rejected"`) {
			t.Fatal("a reject that lost the race emitted a decision event")
		}
	}
}

// TestGovernanceMergeStatus reports the hold and the approval.
func TestGovernanceMergeStatus(t *testing.T) {
	f := newGovFake(t, publishWorkflow(), &GovernanceGuard{Enabled: true, MaxReportedPaths: 1}, true, govPaths...)
	requireHoldErr(t, f.merge())
	st, err := f.orch.GovernanceMergeStatus(context.Background(), bindRunID)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Evaluated || !st.Required || !st.Held || st.BoundSHA != gBound || st.PathCount != 2 || len(st.Paths) != 1 || !st.Truncated || st.Approval != nil {
		t.Fatalf("status = %+v", st)
	}
	if _, err := f.decide(GovernanceApprove, gBound, govOperator, ""); err != nil {
		t.Fatal(err)
	}
	st, err = f.orch.GovernanceMergeStatus(context.Background(), bindRunID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Held || st.RunStatus != domain.RunStatusCompleted || st.Approval == nil || st.Approval.SHA != gBound || st.Approval.MergeSHA != gMerge {
		t.Fatalf("status after approval = %+v (approval %+v)", st, st.Approval)
	}
	if _, err := f.orch.GovernanceMergeStatus(context.Background(), "run-missing"); err == nil {
		t.Fatal("unknown run: no error")
	}
	off := newGovFake(t, publishWorkflow(), nil, true, govPaths...)
	_, err = off.orch.GovernanceMergeStatus(context.Background(), bindRunID)
	requireSpineCode(t, err, domain.ErrUnavailable)
}

type fixedPolicy struct {
	d   branchprotect.Decision
	r   []branchprotect.Reason
	err error
}

func (p fixedPolicy) Evaluate(context.Context, branchprotect.Request) (branchprotect.Decision, []branchprotect.Reason, error) {
	return p.d, p.r, p.err
}

// TestGovernanceGuard_PolicyDenyIsNotAHold: only the guard's own decision
// creates a hold. A policy that denies — even with the hold's reason code —
// while the guard sees nothing to approve is a plain refusal; a policy
// failure is internal. Neither records a hold.
func TestGovernanceGuard_PolicyDenyIsNotAHold(t *testing.T) {
	for _, c := range []struct {
		name string
		pol  fixedPolicy
		code domain.ErrorCode
	}{
		{"deny with the hold reason", fixedPolicy{d: branchprotect.DecisionDeny,
			r: []branchprotect.Reason{{Code: branchprotect.ReasonGovernanceApprovalRequired, Message: "custom"}}}, domain.ErrForbidden},
		{"deny", fixedPolicy{d: branchprotect.DecisionDeny,
			r: []branchprotect.Reason{{Code: branchprotect.ReasonRuleDenies, Message: "no"}}}, domain.ErrForbidden},
		{"policy error", fixedPolicy{d: branchprotect.DecisionDeny, err: errors.New("source down")}, domain.ErrInternal},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newGovFake(t, publishWorkflow(), defaultGuard(), true, "src/a.go")
			// preflight's governance-free check must pass for the gate to run
			f.orch.policy = gateOnlyPolicy{gate: c.pol}
			err := f.merge()
			requireSpineCode(t, err, c.code)
			if errors.Is(err, ErrGovernanceApprovalRequired) || f.store.holdWrites != 0 || len(f.eventsOf(domain.EventGovernanceMergeHeld)) != 0 {
				t.Fatal("a policy-only deny became a hold")
			}
			if f.g.callCount("binder:MergeBound") != 0 {
				t.Fatal("merged after a deny")
			}
		})
	}
}

// gateOnlyPolicy allows the preflight's governance-free governed-merge
// check and answers gate for the request that carries governance facts.
type gateOnlyPolicy struct{ gate fixedPolicy }

func (p gateOnlyPolicy) Evaluate(ctx context.Context, req branchprotect.Request) (branchprotect.Decision, []branchprotect.Reason, error) {
	if req.Governance == nil {
		return branchprotect.DecisionAllow, []branchprotect.Reason{{Code: branchprotect.ReasonGovernedMerge}}, nil
	}
	return p.gate.Evaluate(ctx, req)
}
