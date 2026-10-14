package engine

// INIT-023 TASK-005 AC 15 (audit P3-37, dormant: no code-repository
// catalog is wired in `spine serve` or SMP). The gap3 harness shape —
// real git.CLIClient clients on a primary repository and on a code
// repository (bare origin + working clone) — with merge binding on:
//
//	(a) a push to the code repository's run branch after the gate capture
//	    never reaches its origin default branch;
//	(b) a code-repository client without the CommitBinder capability
//	    refuses StartRun before any branch exists;
//	(c) after a merge_failed loop-back, newly reviewed code-repository work
//	    is merged again (binding off keeps today's skip);
//	(d) a missing per-repository binding fails closed;
//	(e) an operator retry rebinds only to a head_sha it names;
//	(f) the recovery ledger is a plumbing child of the bound commit.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/testutil"
)

func crGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func crIsAncestor(dir, a, d string) bool {
	return exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", a, d).Run() == nil
}

// failingCLI wraps a CLIClient: the next failBound MergeBound calls (and,
// for binding off, failMerge plain non-abort Merge calls) fail with err.
// While misMerge is set, the next MergeBound merges it in place of
// opts.SourceSHA (a faulty binder). It records every push.
type failingCLI struct {
	*git.CLIClient
	mu          sync.Mutex
	failBound   int
	failMerge   int
	err         error
	misMerge    string
	pushCommits []string // "remote branch sha"
	plainPushes []string // "remote ref"
}

func (f *failingCLI) PushCommit(ctx context.Context, remote, branch, sha string) error {
	f.mu.Lock()
	f.pushCommits = append(f.pushCommits, remote+" "+branch+" "+sha)
	f.mu.Unlock()
	return f.CLIClient.PushCommit(ctx, remote, branch, sha)
}

func (f *failingCLI) Push(ctx context.Context, remote, ref string) error {
	f.mu.Lock()
	f.plainPushes = append(f.plainPushes, remote+" "+ref)
	f.mu.Unlock()
	return f.CLIClient.Push(ctx, remote, ref)
}

func (f *failingCLI) take(n *int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if *n > 0 {
		*n--
		return true
	}
	return false
}

func (f *failingCLI) MergeBound(ctx context.Context, o git.MergeOpts) (git.MergeResult, error) {
	if f.take(&f.failBound) {
		return git.MergeResult{}, f.err
	}
	f.mu.Lock()
	if f.misMerge != "" {
		o.SourceSHA, f.misMerge = f.misMerge, ""
	}
	f.mu.Unlock()
	return f.CLIClient.MergeBound(ctx, o)
}

func (f *failingCLI) Merge(ctx context.Context, o git.MergeOpts) (git.MergeResult, error) {
	if o.Strategy != "abort" && o.Source != "--abort" && f.take(&f.failMerge) {
		return git.MergeResult{}, f.err
	}
	return f.CLIClient.Merge(ctx, o)
}

// plainCodeClient hides the CommitBinder capability of a CLIClient.
type plainCodeClient struct{ git.GitClient }

var errConflict = &git.GitError{Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict"}

type crEnv struct {
	t          *testing.T
	ctx        context.Context
	primary    string
	codeOrigin string
	codeWork   string
	prim       *failingCLI
	code       *failingCLI
	store      *mockRunStore
	orch       *Orchestrator
	run        *domain.Run
}

func codeRepoWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "wf-code", Path: "workflows/code.yaml", Version: "1", EntryStep: "execute",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "Done", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted", NextStep: "publish"},
					{ID: "needs_rework", Name: "Rework", NextStep: "execute"},
				}},
			{ID: "publish", Name: "Publish", Type: domain.StepTypeInternal,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly, Handler: "merge"},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "published", Name: "Published", NextStep: "end"},
					{ID: "merge_failed", Name: "Merge failed", NextStep: "execute"},
				}},
		},
	}
}

// newCREnv builds the primary repository (with a bare origin), the code
// repository (bare origin + working clone) and an orchestrator over
// real CLIClients. codeClient, when set, replaces the code repository's
// client (for the missing-capability case).
func newCREnv(t *testing.T, bindingOn bool, codeClient func(*failingCLI) git.GitClient) (*crEnv, error) {
	t.Helper()
	testutil.HermeticGit(t)
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	e := &crEnv{t: t, ctx: domain.WithActor(context.Background(), &domain.Actor{ActorID: "operator-1", Role: domain.RoleOperator})}

	e.primary = testutil.NewTempRepo(t)
	primOrigin := filepath.Join(t.TempDir(), "primary.git")
	crGit(t, e.primary, "init", "-q", "--bare", primOrigin)
	crGit(t, e.primary, "remote", "add", "origin", primOrigin)
	crGit(t, e.primary, "push", "-q", "origin", "main")

	root := t.TempDir()
	e.codeOrigin = filepath.Join(root, "code.git")
	crGit(t, root, "init", "-q", "--bare", "-b", "main", e.codeOrigin)
	seed := filepath.Join(root, "seed")
	crGit(t, root, "clone", "-q", e.codeOrigin, seed)
	crGit(t, seed, "checkout", "-q", "-b", "main")
	crGit(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	crGit(t, seed, "push", "-q", "origin", "main")
	e.codeWork = filepath.Join(root, "work")
	crGit(t, root, "clone", "-q", e.codeOrigin, e.codeWork)

	e.prim = &failingCLI{CLIClient: git.NewCLIClient(e.primary), err: errConflict}
	e.code = &failingCLI{CLIClient: git.NewCLIClient(e.codeWork), err: errConflict}
	var cc git.GitClient = e.code
	if codeClient != nil {
		cc = codeClient(e.code)
	}
	wf := codeRepoWorkflow()
	art := taskWithRepos([]string{"code-a"})
	e.store = &mockRunStore{}
	e.orch = &Orchestrator{
		workflows: &mockWorkflowResolver{result: &workflow.BindingResult{Workflow: wf, CommitSHA: "abc123", VersionLabel: "1"}},
		store:     e.store,
		actors:    &stubActorAssigner{},
		artifacts: &mockArtifactReader{artifact: art},
		events:    &mockEventEmitter{},
		git:       e.prim,
		wfLoader:  &mockWorkflowLoader{wfDef: wf},
		policy:    branchprotect.NewPermissive(),
	}
	e.orch.WithRepositoryResolver(newRepoResolver(map[string]repoLookup{"code-a": activeRepoLookup("code-a", "main")}))
	e.orch.WithRepositoryGitClients(realRepoClients{"code-a": cc})
	if bindingOn {
		if err := e.orch.EnableMergeBinding(MergeBindingConfig{}); err != nil {
			t.Fatalf("EnableMergeBinding: %v", err)
		}
	}
	res, err := e.orch.StartRun(e.ctx, art.Path)
	if err != nil {
		return e, err
	}
	e.run = res.Run
	return e, nil
}

func mustCREnv(t *testing.T, bindingOn bool) *crEnv {
	t.Helper()
	e, err := newCREnv(t, bindingOn, nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return e
}

// commitCode simulates a push to the code repository's run branch.
func (e *crEnv) commitCode(file, content string) string {
	e.t.Helper()
	crGit(e.t, e.codeWork, "checkout", "-q", e.run.BranchName)
	if err := os.WriteFile(filepath.Join(e.codeWork, file), []byte(content), 0o644); err != nil { //nolint:gosec // G306: test fixture
		e.t.Fatal(err)
	}
	crGit(e.t, e.codeWork, "add", file)
	crGit(e.t, e.codeWork, "commit", "-q", "-m", "code "+file)
	head := crGit(e.t, e.codeWork, "rev-parse", "HEAD")
	crGit(e.t, e.codeWork, "checkout", "-q", "main")
	return head
}

func (e *crEnv) submit(stepID, outcome string) error {
	e.t.Helper()
	var ex *domain.StepExecution
	for _, s := range e.store.createdSteps {
		if s.StepID == stepID && !s.Status.IsTerminal() && (ex == nil || s.Attempt > ex.Attempt) {
			ex = s
		}
	}
	if ex == nil {
		e.t.Fatalf("no live %s execution", stepID)
	}
	ex.ActorID, ex.Status = "worker", domain.StepStatusAssigned
	return e.orch.SubmitStepResult(e.ctx, ex.ExecutionID, StepResult{OutcomeID: outcome})
}

func (e *crEnv) runNow() *domain.Run {
	e.t.Helper()
	r, err := e.store.GetRun(e.ctx, e.run.RunID)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *crEnv) codeOutcome() *domain.RepositoryMergeOutcome {
	e.t.Helper()
	o, err := e.store.GetRepositoryMergeOutcome(e.ctx, e.run.RunID, "code-a")
	if err != nil {
		e.t.Fatalf("code outcome: %v", err)
	}
	return o
}

func (e *crEnv) originHas(sha string) bool { return crIsAncestor(e.codeOrigin, sha, "refs/heads/main") }

// TestCodeRepoBinding_PushAfterCaptureNotMerged is AC 15(a): the scratch
// reproduction (a commit made after the reviewed SHA landed on the code
// repository's origin main), inverted.
func TestCodeRepoBinding_PushAfterCaptureNotMerged(t *testing.T) {
	e := mustCREnv(t, true)
	r1 := e.commitCode("feature.go", "package x\n")
	if err := e.submit("execute", "completed"); err != nil {
		t.Fatal(err)
	}
	e.code.failBound = 1
	e.code.err = &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "repository locked"}
	if err := e.submit("review", "accepted"); err != nil {
		t.Fatal(err)
	}
	if r := e.runNow(); r.Status != domain.RunStatusCommitting {
		t.Fatalf("run status = %s, want committing after the transient code-repo failure", r.Status)
	}
	sneak := e.commitCode("backdoor.go", "package x // unreviewed\n")
	if err := e.orch.MergeRunBranch(e.ctx, e.run.RunID); err != nil {
		t.Fatal(err)
	}
	if !e.originHas(r1) {
		t.Fatal("the reviewed code commit is not on the code repository's origin main")
	}
	if e.originHas(sneak) {
		t.Fatal("a code commit pushed after the gate reached the code repository's origin main")
	}
	if r := e.runNow(); r.Status != domain.RunStatusCompleted {
		t.Fatalf("run status = %s", r.Status)
	}
	// The excluded commit is kept for inspection (owner question 3), in
	// the code repository as for the primary (codex review round 1).
	if got := crGit(t, e.codeWork, "for-each-ref", "--format=%(objectname)", "refs/heads/"+e.run.BranchName); got != sneak {
		t.Fatalf("code repository run branch = %q, want it kept at the excluded commit %s", got, sneak)
	}
}

// TestCodeRepoBinding_CleanMergeDeletesBranches: with nothing excluded,
// the code repository's run branch is cleaned up as before. The code
// repository's merge is published with exactly one PushCommit of the
// merge commit and never with a push of the local ref (DC-1 for code
// repositories, the AC 26 shape; review round 1).
func TestCodeRepoBinding_CleanMergeDeletesBranches(t *testing.T) {
	e := mustCREnv(t, true)
	r1 := e.commitCode("feature.go", "package x\n")
	if err := e.submit("execute", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := e.submit("review", "accepted"); err != nil {
		t.Fatal(err)
	}
	if r := e.runNow(); r.Status != domain.RunStatusCompleted || !e.originHas(r1) {
		t.Fatalf("run status %s, origin has R1=%v", r.Status, e.originHas(r1))
	}
	if got := crGit(t, e.codeWork, "for-each-ref", "refs/heads/"+e.run.BranchName); got != "" {
		t.Fatalf("code repository run branch not cleaned up: %s", got)
	}
	m := e.codeOutcome().MergeCommitSHA
	if len(e.code.pushCommits) != 1 || e.code.pushCommits[0] != "origin main "+m {
		t.Fatalf("code PushCommit calls = %q, want exactly [origin main %s]", e.code.pushCommits, m)
	}
	if len(e.code.plainPushes) != 0 {
		t.Fatalf("the code repository's merge was pushed by local ref: %q", e.code.plainPushes)
	}
	if got := crGit(t, e.codeOrigin, "rev-parse", "refs/heads/main"); got != m {
		t.Fatalf("code origin main = %s, want the merge commit %s", got, m)
	}
}

// TestCodeRepoBinding_MovedHeadAtGate: a push to the code repository's
// run branch after the producer bound it and before the review approves
// is refused at the gate (§4 rule 6) — 409 run_branch_moved_since_gate
// naming the repository, nothing recorded — and is accepted as the new
// binding only under the rebind policy (review round 1: windows (a)/(b)
// for code repositories had no test).
func TestCodeRepoBinding_MovedHeadAtGate(t *testing.T) {
	t.Run("reject", func(t *testing.T) {
		e := mustCREnv(t, true)
		r1 := e.commitCode("feature.go", "package x\n")
		if err := e.submit("execute", "completed"); err != nil {
			t.Fatal(err)
		}
		if got := e.runNow().CommitMeta[RepoBoundSHAKey("code-a")]; got != r1 {
			t.Fatalf("code-a binding after execute = %s, want %s", got, r1)
		}
		metaBefore := e.runNow().CommitMeta
		sneak := e.commitCode("backdoor.go", "package x // unreviewed\n")
		err := e.submit("review", "accepted")
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrConflict {
			t.Fatalf("review accepted over a moved code repository = %v, want ErrConflict", err)
		}
		d, ok := se.Detail.(HeadBindingDetail)
		if !ok || d.Reason != ReasonRunBranchMovedSinceGate || d.RepositoryID != "code-a" || d.HeadSHA != sneak || d.BoundSHA != r1 {
			t.Fatalf("detail = %#v, want run_branch_moved_since_gate for code-a (head %s, bound %s)", se.Detail, sneak, r1)
		}
		if got := e.runNow().CommitMeta; !mapsEqual(got, metaBefore) {
			t.Fatalf("commit_meta changed on a refused gate: %v -> %v", metaBefore, got)
		}
		var review *domain.StepExecution
		for _, s := range e.store.createdSteps {
			if s.StepID == "review" {
				review = s
			}
		}
		if review == nil || review.Status == domain.StepStatusCompleted || review.OutcomeID != "" {
			t.Fatalf("review execution changed on a refused gate: %+v", review)
		}
		if e.originHas(sneak) {
			t.Fatal("the unreviewed code commit reached the code repository's origin main")
		}
	})
	t.Run("rebind", func(t *testing.T) {
		e := mustCREnv(t, true)
		if err := e.orch.EnableMergeBinding(MergeBindingConfig{MovedHead: MovedHeadRebind}); err != nil {
			t.Fatal(err)
		}
		e.commitCode("feature.go", "package x\n")
		if err := e.submit("execute", "completed"); err != nil {
			t.Fatal(err)
		}
		moved := e.commitCode("fixup.go", "package x // reviewer saw this\n")
		if err := e.submit("review", "accepted"); err != nil {
			t.Fatalf("review accepted under rebind: %v", err)
		}
		if r := e.runNow(); r.Status != domain.RunStatusCompleted || !e.originHas(moved) {
			t.Fatalf("run %s, origin has the rebound head=%v; want completed with it merged", r.Status, e.originHas(moved))
		}
	})
}

// TestCodeRepoBinding_RefusedMergeTakenBack: a code-repository merge the
// structural check refuses (a faulty binder merged another commit) is
// not pushed and is taken off the code repository's local default
// branch, so a later merge there neither builds on it nor publishes it
// (review round 1).
func TestCodeRepoBinding_RefusedMergeTakenBack(t *testing.T) {
	e := mustCREnv(t, true)
	crGit(t, e.codeWork, "checkout", "-q", "-b", "sneak-side", "main")
	if err := os.WriteFile(filepath.Join(e.codeWork, "backdoor.go"), []byte("package x // never reviewed\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture
		t.Fatal(err)
	}
	crGit(t, e.codeWork, "add", "backdoor.go")
	crGit(t, e.codeWork, "commit", "-q", "-m", "sneak")
	sneak := crGit(t, e.codeWork, "rev-parse", "HEAD")
	crGit(t, e.codeWork, "checkout", "-q", "main")
	baseBefore := crGit(t, e.codeWork, "rev-parse", "refs/heads/main")

	r1 := e.commitCode("feature.go", "package x\n")
	if err := e.submit("execute", "completed"); err != nil {
		t.Fatal(err)
	}
	e.code.misMerge = sneak
	if err := e.submit("review", "accepted"); err != nil {
		t.Fatal(err)
	}
	o := e.codeOutcome()
	if o.Status != domain.RepositoryMergeStatusFailed || !strings.Contains(o.FailureDetail, "was restored to "+baseBefore) {
		t.Fatalf("code outcome = %s %q, want failed and taken back to %s", o.Status, o.FailureDetail, baseBefore)
	}
	if len(e.code.pushCommits) != 0 || len(e.code.plainPushes) != 0 {
		t.Fatalf("a refused code-repository merge was pushed: %q %q", e.code.pushCommits, e.code.plainPushes)
	}
	if got := crGit(t, e.codeWork, "rev-parse", "refs/heads/main"); got != baseBefore {
		t.Fatalf("code repository local main = %s, want it taken back to %s", got, baseBefore)
	}
	if st := crGit(t, e.codeWork, "status", "--porcelain"); st != "" {
		t.Fatalf("the take-back left the code working tree dirty:\n%s", st)
	}

	// The operator retries; the resumed merge publishes the bound commit
	// and nothing of the refused merge.
	if r := e.runNow(); r.Status != domain.RunStatusPartiallyMerged {
		t.Fatalf("run status = %s, want partially-merged", r.Status)
	}
	if _, err := e.orch.RetryRepositoryMerge(e.ctx, e.run.RunID, "code-a", "faulty merge refused, try again"); err != nil {
		t.Fatalf("RetryRepositoryMerge: %v", err)
	}
	e.resume()
	if !e.originHas(r1) || e.originHas(sneak) {
		t.Fatalf("code origin main has R1=%v sneak=%v; want R1 only", e.originHas(r1), e.originHas(sneak))
	}
}

// TestCodeRepoBinding_ResolvedExternallyCleansUp: a code repository the
// operator resolved externally is not judged for excluded commits (its
// default branch here need not contain the run tip), so the completed
// run's branches are cleaned up as with binding off, not all kept under
// a false warning (review round 1).
func TestCodeRepoBinding_ResolvedExternallyCleansUp(t *testing.T) {
	for _, bindingOn := range []bool{true, false} {
		e := mustCREnv(t, bindingOn)
		e.commitCode("v1.go", "package x // 1\n")
		if err := e.submit("execute", "completed"); err != nil {
			t.Fatal(err)
		}
		e.code.failBound, e.code.failMerge = 1, 1 // binding on merges with MergeBound, off with Merge
		if err := e.submit("review", "accepted"); err != nil {
			t.Fatal(err)
		}
		if r := e.runNow(); r.Status != domain.RunStatusPartiallyMerged {
			t.Fatalf("binding=%v: run status = %s, want partially-merged", bindingOn, r.Status)
		}
		if _, err := e.orch.ResolveRepositoryMergeExternally(e.ctx, e.run.RunID, "code-a", "merged by hand", ""); err != nil {
			t.Fatalf("binding=%v: ResolveRepositoryMergeExternally: %v", bindingOn, err)
		}
		e.resume()
		if r := e.runNow(); r.Status != domain.RunStatusCompleted {
			t.Fatalf("binding=%v: run status = %s, want completed", bindingOn, r.Status)
		}
		if got := crGit(t, e.codeWork, "for-each-ref", "refs/heads/"+e.run.BranchName); got != "" {
			t.Fatalf("binding=%v: code repository run branch kept: %s", bindingOn, got)
		}
		if got := crGit(t, e.primary, "for-each-ref", "refs/heads/"+e.run.BranchName); got != "" {
			t.Fatalf("binding=%v: primary run branch kept: %s", bindingOn, got)
		}
	}
}

// TestCodeRepoBinding_MissingCapabilityRefusesStart is AC 15(b).
func TestCodeRepoBinding_MissingCapabilityRefusesStart(t *testing.T) {
	e, err := newCREnv(t, true, func(c *failingCLI) git.GitClient { return plainCodeClient{c.CLIClient} })
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrPrecondition {
		t.Fatalf("StartRun = %v, want ErrPrecondition", err)
	}
	if e.store.createdRun != nil {
		t.Fatal("a run row was created")
	}
	for _, dir := range []string{e.primary, e.codeWork} {
		if out := crGit(t, dir, "for-each-ref", "refs/heads/spine/"); out != "" {
			t.Fatalf("a run branch exists in %s: %s", dir, out)
		}
	}
}

// loopBack drives: code repo merges R1, the primary merge fails
// permanently, merge_failed routes back to execute, new work R2 is
// reviewed and the run merges again.
func loopBack(t *testing.T, e *crEnv) (r1, r2 string) {
	t.Helper()
	r1 = e.commitCode("v1.go", "package x // 1\n")
	if err := e.submit("execute", "completed"); err != nil {
		t.Fatal(err)
	}
	e.prim.failBound, e.prim.failMerge = 1, 1
	if err := e.submit("review", "accepted"); err != nil {
		t.Fatal(err)
	}
	if o := e.codeOutcome(); o.Status != domain.RepositoryMergeStatusMerged {
		t.Fatalf("code outcome after the first attempt = %s, want merged", o.Status)
	}
	if r := e.runNow(); r.Status != domain.RunStatusActive {
		t.Fatalf("run status after the permanent primary failure = %s, want active (routed back)", r.Status)
	}
	r2 = e.commitCode("v2.go", "package x // 2\n")
	if err := e.submit("execute", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := e.submit("review", "accepted"); err != nil {
		t.Fatal(err)
	}
	return r1, r2
}

// TestCodeRepoBinding_LoopBackMergesNewWork is AC 15(c), with the
// binding-off control that pins the documented limitation.
func TestCodeRepoBinding_LoopBackMergesNewWork(t *testing.T) {
	t.Run("binding on", func(t *testing.T) {
		e := mustCREnv(t, true)
		r1, r2 := loopBack(t, e)
		if !e.originHas(r1) || !e.originHas(r2) {
			t.Fatalf("origin main has R1=%v R2=%v; want both", e.originHas(r1), e.originHas(r2))
		}
		if r := e.runNow(); r.Status != domain.RunStatusCompleted {
			t.Fatalf("run status = %s", r.Status)
		}
	})
	t.Run("binding off keeps the skip (KNOWN-LIMITATIONS)", func(t *testing.T) {
		e := mustCREnv(t, false)
		r1, r2 := loopBack(t, e)
		if !e.originHas(r1) || e.originHas(r2) {
			t.Fatalf("binding off: origin main has R1=%v R2=%v; want R1 only (the documented skip)", e.originHas(r1), e.originHas(r2))
		}
	})
}

// TestCodeRepoBinding_MissingRepoBinding is AC 15(d).
func TestCodeRepoBinding_MissingRepoBinding(t *testing.T) {
	e := mustCREnv(t, true)
	run := *e.run
	run.CommitMeta = map[string]string{
		CommitMetaBoundSHA: crGit(t, e.primary, "rev-parse", "main"),
		CommitMetaBoundBy:  boundByStep("review", run.RunID+"-review-1"),
	}
	out, err := e.orch.attemptCodeRepositoryMerge(e.ctx, &run, "code-a")
	if err != nil {
		t.Fatalf("attemptCodeRepositoryMerge: %v", err)
	}
	if out.Status != domain.RepositoryMergeStatusFailed || out.FailureClass != domain.MergeFailurePrecondition {
		t.Fatalf("outcome = %s/%s, want failed/precondition", out.Status, out.FailureClass)
	}
}

// partiallyMerged drives a run to partially-merged: the code repository's
// merge fails permanently once, the primary merges.
func partiallyMerged(t *testing.T, e *crEnv) string {
	t.Helper()
	r1 := e.commitCode("v1.go", "package x // 1\n")
	if err := e.submit("execute", "completed"); err != nil {
		t.Fatal(err)
	}
	e.code.failBound = 1
	if err := e.submit("review", "accepted"); err != nil {
		t.Fatal(err)
	}
	if r := e.runNow(); r.Status != domain.RunStatusPartiallyMerged {
		t.Fatalf("run status = %s, want partially-merged", r.Status)
	}
	return r1
}

// resume is the scheduler's resume of a partially-merged run.
func (e *crEnv) resume() {
	e.t.Helper()
	ok, err := e.store.TransitionRunStatus(e.ctx, e.run.RunID, domain.RunStatusPartiallyMerged, domain.RunStatusCommitting)
	if err != nil || !ok {
		e.t.Fatalf("resume: %v %v", ok, err)
	}
	if err := e.orch.MergeRunBranch(e.ctx, e.run.RunID); err != nil {
		e.t.Fatalf("resumed MergeRunBranch: %v", err)
	}
}

func (e *crEnv) ledgerTrailers(sha string) string {
	return crGit(e.t, e.primary, "log", "-1", "--format=%B", sha)
}

// TestCodeRepoBinding_RetryRebindsOnlyToANamedHead is AC 15(e) (DC-7).
func TestCodeRepoBinding_RetryRebindsOnlyToANamedHead(t *testing.T) {
	t.Run("head_sha equal to the current head rebinds", func(t *testing.T) {
		e := mustCREnv(t, true)
		partiallyMerged(t, e)
		fix := e.commitCode("fix.go", "package x // operator fix\n")
		res, err := e.orch.RetryRepositoryMergeAt(e.ctx, e.run.RunID, "code-a", "conflict fixed on the branch", fix)
		if err != nil {
			t.Fatalf("RetryRepositoryMergeAt: %v", err)
		}
		if got := e.runNow().CommitMeta[RepoBoundSHAKey("code-a")]; got != fix {
			t.Fatalf("code-a binding = %s, want the named head %s", got, fix)
		}
		if body := e.ledgerTrailers(res.LedgerCommitSHA); !strings.Contains(body, "Rebound-SHA: "+fix) {
			t.Fatalf("ledger commit lacks the Rebound-SHA trailer:\n%s", body)
		}
		e.resume()
		if !e.originHas(fix) {
			t.Fatal("the rebound head did not merge")
		}
	})
	t.Run("a different head_sha is a conflict and changes nothing", func(t *testing.T) {
		e := mustCREnv(t, true)
		r1 := partiallyMerged(t, e)
		e.commitCode("later.go", "package x\n")
		metaBefore := e.runNow().CommitMeta
		outBefore := *e.codeOutcome()
		_, err := e.orch.RetryRepositoryMergeAt(e.ctx, e.run.RunID, "code-a", "retry", r1)
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrConflict {
			t.Fatalf("RetryRepositoryMergeAt(stale head) = %v, want ErrConflict", err)
		}
		if d, ok := se.Detail.(HeadBindingDetail); !ok || d.Reason != ReasonRunBranchMoved {
			t.Fatalf("detail = %#v", se.Detail)
		}
		if got := e.runNow().CommitMeta; !mapsEqual(got, metaBefore) {
			t.Fatalf("commit_meta changed: %v -> %v", metaBefore, got)
		}
		if got := *e.codeOutcome(); got.Status != outBefore.Status {
			t.Fatalf("outcome changed: %s -> %s", outBefore.Status, got.Status)
		}
	})
	t.Run("without head_sha the bound commit merges again", func(t *testing.T) {
		e := mustCREnv(t, true)
		r1 := partiallyMerged(t, e)
		sneak := e.commitCode("sneak.go", "package x // pushed after the gate\n")
		if _, err := e.orch.RetryRepositoryMerge(e.ctx, e.run.RunID, "code-a", "transient, try again"); err != nil {
			t.Fatalf("RetryRepositoryMerge: %v", err)
		}
		if got := e.runNow().CommitMeta[RepoBoundSHAKey("code-a")]; got != r1 {
			t.Fatalf("code-a binding = %s, want it kept at %s", got, r1)
		}
		e.resume()
		if !e.originHas(r1) || e.originHas(sneak) {
			t.Fatalf("origin main has R1=%v sneak=%v; want R1 only", e.originHas(r1), e.originHas(sneak))
		}
	})
	t.Run("malformed head_sha", func(t *testing.T) {
		e := mustCREnv(t, true)
		partiallyMerged(t, e)
		_, err := e.orch.RetryRepositoryMergeAt(e.ctx, e.run.RunID, "code-a", "retry", "abc1234")
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrInvalidParams {
			t.Fatalf("RetryRepositoryMergeAt(abc1234) = %v, want ErrInvalidParams", err)
		}
	})
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestCodeRepoBinding_LedgerCommitUnderBinding is AC 15(f).
func TestCodeRepoBinding_LedgerCommitUnderBinding(t *testing.T) {
	e := mustCREnv(t, true)
	partiallyMerged(t, e)
	bound := e.runNow().CommitMeta[CommitMetaBoundSHA]
	branchBefore := crGit(t, e.primary, "rev-parse", "refs/heads/"+e.run.BranchName)
	res, err := e.orch.RetryRepositoryMerge(e.ctx, e.run.RunID, "code-a", "retry after a transient failure")
	if err != nil {
		t.Fatalf("RetryRepositoryMerge: %v", err)
	}
	l := res.LedgerCommitSHA
	if parent := crGit(t, e.primary, "rev-parse", l+"^"); parent != bound {
		t.Fatalf("ledger parent = %s, want the bound commit %s", parent, bound)
	}
	meta := e.runNow().CommitMeta
	if meta[CommitMetaBoundSHA] != l || meta[CommitMetaBoundBy] != boundByRecovery(e.run.RunID, "merge_recovery_retry") {
		t.Fatalf("binding after the ledger = %v", meta)
	}
	if got := crGit(t, e.primary, "rev-parse", "refs/heads/"+e.run.BranchName); got != branchBefore {
		t.Fatalf("the ledger moved the run branch %s -> %s", branchBefore, got)
	}
	e.resume()
	if out := crGit(t, e.primary, "log", "main", "--format=%H", "--grep", "Operation: merge_recovery_retry"); !strings.Contains(out, l) {
		t.Fatalf("git log main --grep does not find the ledger commit %s:\n%s", l, out)
	}
	if o, _ := e.store.GetRepositoryMergeOutcome(e.ctx, e.run.RunID, repository.PrimaryRepositoryID); o == nil || o.Status != domain.RepositoryMergeStatusMerged {
		t.Fatalf("primary outcome = %+v", o)
	}
}
