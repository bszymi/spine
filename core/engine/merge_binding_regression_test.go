package engine_test

// INIT-023 TASK-005 regression tests (audit F2, merge half), ported from
// the audit's two governed-merge PoCs and run against the real
// git.CLIClient on temporary repositories, with merge binding ON:
//
//   - AC 1 (push during review; push in the committing window) and AC 2
//     (sneak after validate; assertion under rebind; clean run) — the PoCs,
//     inverted: what they showed landing on main must not;
//   - AC 3 / AC 24 — auto-push with a bare origin: the push publishes
//     exactly the merge commit, a transient push failure recovers, and
//     replays add nothing;
//   - AC 10 / AC 11 / AC 25 — the cascade at the bound commit, excluded
//     commits, and a tip reset that must not fail the merge;
//   - AC 26 — the call sequence of one bound merge.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/testutil"
)

const (
	protectedMain = "version: 1\nrules:\n  - branch: main\n    protections: [no-delete, no-direct-write]\n"
	taskPending   = "---\nid: TASK-001\ntype: Task\ntitle: T\nstatus: Pending\n---\n# T\n"
)

// outcomeStore is memStore plus the repository merge outcomes it drops.
type outcomeStore struct {
	*memStore
	mu       sync.Mutex
	outcomes map[string]domain.RepositoryMergeOutcome
}

func newOutcomeStore() *outcomeStore {
	return &outcomeStore{memStore: newMemStore(), outcomes: map[string]domain.RepositoryMergeOutcome{}}
}

func (s *outcomeStore) UpsertRepositoryMergeOutcome(_ context.Context, o *domain.RepositoryMergeOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes[o.RunID+"/"+o.RepositoryID] = *o
	return nil
}

func (s *outcomeStore) GetRepositoryMergeOutcome(_ context.Context, runID, repoID string) (*domain.RepositoryMergeOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.outcomes[runID+"/"+repoID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "merge outcome not found")
	}
	return &o, nil
}

func (s *outcomeStore) ListRepositoryMergeOutcomes(_ context.Context, runID string) ([]domain.RepositoryMergeOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.RepositoryMergeOutcome
	for k := range s.outcomes {
		if s.outcomes[k].RunID == runID {
			out = append(out, s.outcomes[k])
		}
	}
	return out, nil
}

// flakyCLI is a CLIClient whose first MergeBound and/or PushCommit calls
// fail with a transient error. pushLands lets the failing push through
// before reporting the error (the client saw a network error after the
// remote updated). It records every MergeBound result.
type flakyCLI struct {
	*git.CLIClient
	mu         sync.Mutex
	failMerges int
	failPushes int
	pushLands  bool
	results    []git.MergeResult
}

var errBlip = &git.GitError{Kind: git.ErrKindTransient, Op: "push", Message: "network error"}

func (f *flakyCLI) MergeBound(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	f.mu.Lock()
	if f.failMerges > 0 {
		f.failMerges--
		f.mu.Unlock()
		return git.MergeResult{}, &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "repository locked"}
	}
	f.mu.Unlock()
	res, err := f.CLIClient.MergeBound(ctx, opts)
	if err == nil {
		f.mu.Lock()
		f.results = append(f.results, res)
		f.mu.Unlock()
	}
	return res, err
}

func (f *flakyCLI) PushCommit(ctx context.Context, remote, branch, sha string) error {
	f.mu.Lock()
	fail := f.failPushes > 0
	if fail {
		f.failPushes--
	}
	f.mu.Unlock()
	if fail {
		if f.pushLands {
			if err := f.CLIClient.PushCommit(ctx, remote, branch, sha); err != nil {
				return err
			}
		}
		return errBlip
	}
	return f.CLIClient.PushCommit(ctx, remote, branch, sha)
}

type regEnv struct {
	t      *testing.T
	dir    string
	origin string
	cli    *git.CLIClient
	flaky  *flakyCLI
	store  *outcomeStore
	actors *mockActorGateway
	orch   *engine.Orchestrator
	run    *domain.Run
	branch string
}

type regOpts struct {
	autoPush bool
	policy   engine.MovedHeadPolicy
	op       func(c *git.CLIClient) engine.GitOperator // nil: a flakyCLI
}

func (e *regEnv) git(args ...string) string {
	e.t.Helper()
	return regGit(e.t, e.dir, args...)
}

func regGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (e *regEnv) isAncestor(a, d string) bool {
	e.t.Helper()
	cmd := exec.Command("git", "-C", e.dir, "merge-base", "--is-ancestor", a, d)
	err := cmd.Run()
	if err == nil {
		return true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false
	}
	e.t.Fatalf("merge-base --is-ancestor %s %s: %v", a, d, err)
	return false
}

func (e *regEnv) write(rel, content string) {
	e.t.Helper()
	p := filepath.Join(e.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil { //nolint:gosec // G306: test fixture
		e.t.Fatal(err)
	}
}

// commitOnRun simulates a push to the run branch (in standalone the git
// endpoint serves this repository): files land in one commit on the run
// branch and the working tree goes back to main.
func (e *regEnv) commitOnRun(msg string, files map[string]string) string {
	e.t.Helper()
	e.git("checkout", "-q", e.branch)
	for p, c := range files {
		e.write(p, c)
	}
	e.git("add", "-A")
	e.git("commit", "-q", "-m", msg)
	head := e.git("rev-parse", "HEAD")
	e.git("checkout", "-q", "main")
	return head
}

func (e *regEnv) runHead() string { return e.git("rev-parse", "refs/heads/"+e.branch) }

func (e *regEnv) mainHas(rev, path string) bool {
	e.t.Helper()
	cmd := exec.Command("git", "-C", e.dir, "cat-file", "-e", rev+":"+path)
	return cmd.Run() == nil
}

func (e *regEnv) mainFile(rev, path string) string {
	e.t.Helper()
	return regGit(e.t, e.dir, "show", rev+":"+path)
}

func (e *regEnv) submit(i int, outcome, expected string) error {
	e.t.Helper()
	if i >= len(e.actors.assignments) {
		e.t.Fatalf("no assignment %d (have %d)", i, len(e.actors.assignments))
	}
	return e.orch.SubmitStepResult(context.Background(), e.actors.assignments[i].AssignmentID,
		engine.StepResult{OutcomeID: outcome, ExpectedHeadSHA: expected})
}

func (e *regEnv) runNow() *domain.Run {
	e.t.Helper()
	r, err := e.store.GetRun(context.Background(), e.run.RunID)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *regEnv) execOf(stepID string) *domain.StepExecution {
	var out *domain.StepExecution
	for _, s := range e.store.steps {
		if s.RunID == e.run.RunID && s.StepID == stepID && (out == nil || s.Attempt > out.Attempt) {
			out = s
		}
	}
	return out
}

func (e *regEnv) refExists(ref string) bool {
	return exec.Command("git", "-C", e.dir, "rev-parse", "--verify", "--quiet", ref).Run() == nil
}

func autoExec() *domain.ExecutionConfig {
	return &domain.ExecutionConfig{Mode: domain.ExecModeAutomatedOnly, EligibleActorTypes: []string{"automated_system"}}
}

// reviewWorkflow is the governance PoC's workflow: execute → review, the
// review's accepted outcome commits status and ends the run.
func reviewWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "wf", Version: "1", Status: domain.WorkflowStatusActive, AppliesTo: []string{"Task"},
		EntryStep: "execute", Path: "workflows/wf.yaml",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeAutomated, Execution: autoExec(),
				Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "done", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview, Execution: autoExec(),
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted", Commit: map[string]string{"status": "Completed"}},
					{ID: "needs_rework", Name: "Rework", NextStep: "execute"},
				}},
		},
	}
}

// taskDefaultWorkflow is the TOCTOU PoC's workflow, mirroring Spine's
// shipped task-default: execute → validate → review → publish (internal
// merge), with the status commit on the review's accepted outcome so the
// cascade runs at merge time.
func taskDefaultWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "task-default", Version: "8.0", Status: domain.WorkflowStatusActive, AppliesTo: []string{"Task"},
		EntryStep: "execute", Path: "workflows/task-default.yaml",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeAutomated, Execution: autoExec(),
				Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "done", NextStep: "validate"}}},
			{ID: "validate", Name: "Validate", Type: domain.StepTypeAutomated, Execution: autoExec(),
				Outcomes: []domain.OutcomeDefinition{{ID: "passed", Name: "ok", NextStep: "review"}, {ID: "failed", Name: "bad", NextStep: "execute"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview, Execution: autoExec(),
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted", NextStep: "publish", Commit: map[string]string{"status": "Completed"}},
					{ID: "needs_rework", Name: "rework", NextStep: "execute"},
				}},
			{ID: "publish", Name: "Publish", Type: domain.StepTypeInternal,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly, Handler: "merge"},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "published", Name: "Published", NextStep: "end"},
					{ID: "merge_failed", Name: "mf", NextStep: "execute"},
				}},
		},
	}
}

func newRegEnv(t *testing.T, wf *domain.WorkflowDefinition, o regOpts) *regEnv {
	t.Helper()
	testutil.HermeticGit(t)
	if o.autoPush {
		t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	} else {
		t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")
	}
	dir := t.TempDir()
	e := &regEnv{t: t, dir: dir}
	e.git("init", "-q", "-b", "main")
	e.write("tasks/t.md", taskPending)
	e.write(".spine/branch-protection.yaml", protectedMain)
	e.git("add", "-A")
	e.git("commit", "-q", "-m", "seed")
	if o.autoPush {
		e.origin = filepath.Join(t.TempDir(), "origin.git")
		e.git("init", "-q", "--bare", e.origin)
		e.git("remote", "add", "origin", e.origin)
		e.git("push", "-q", "origin", "main")
	}
	e.cli = git.NewCLIClient(dir)
	var op engine.GitOperator
	if o.op != nil {
		op = o.op(e.cli)
	} else {
		e.flaky = &flakyCLI{CLIClient: e.cli}
		op = e.flaky
	}
	e.store = newOutcomeStore()
	e.actors = &mockActorGateway{}
	orch, err := engine.New(&memWorkflowResolver{wfDef: wf}, e.store, e.actors,
		&memArtifactReader{artifacts: map[string]*domain.Artifact{
			"tasks/t.md": {Path: "tasks/t.md", ID: "TASK-001", Type: "Task", Status: "Pending"},
		}},
		&memEventEmitter{}, op, &memWorkflowLoader{wfDef: wf})
	if err != nil {
		t.Fatal(err)
	}
	orch.WithBranchProtectPolicy(branchprotect.New(branchprotect.StaticRules(branchprotect.BootstrapDefaults())))
	orch.WithActorSelector(&fixedActorSelector{actor: &domain.Actor{ActorID: "bot-1", Type: domain.ActorTypeAutomated, Status: domain.ActorStatusActive}})
	if err := orch.EnableMergeBinding(engine.MergeBindingConfig{MovedHead: o.policy}); err != nil {
		t.Fatalf("EnableMergeBinding: %v", err)
	}
	e.orch = orch
	res, err := orch.StartRun(context.Background(), "tasks/t.md")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	e.run, e.branch = res.Run, res.Run.BranchName
	return e
}

var (
	sneakFiles = map[string]string{
		"evil.md":                       "unreviewed\n",
		".spine/branch-protection.yaml": "version: 1\nrules: []\n",
	}
	toctouSneak = map[string]string{
		"src/backdoor.go":               "package src\n// UNREVIEWED, UNTESTED\n",
		".spine/branch-protection.yaml": "version: 1\nrules: []\n",
		"workflows/task-default.yaml":   "id: task-default\n# attacker-weakened workflow\n",
	}
)

func requireReason(t *testing.T, err error, code domain.ErrorCode, reason string) {
	t.Helper()
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	d, ok := se.Detail.(engine.HeadBindingDetail)
	if !ok || d.Reason != reason {
		t.Fatalf("detail = %#v, want reason %s", se.Detail, reason)
	}
}

func (e *regEnv) requireMainProtected(rev string) {
	e.t.Helper()
	if e.mainHas(rev, "evil.md") {
		e.t.Fatalf("%s carries evil.md", rev)
	}
	if got := e.mainFile(rev, ".spine/branch-protection.yaml"); got != strings.TrimSpace(protectedMain) {
		e.t.Fatalf("%s branch protection = %q, want the main rule", rev, got)
	}
}

// TestMergeBindingRegression_PushDuringReview is AC 1(a), from
// poc/governance/unreviewed_merge_test.go.
func TestMergeBindingRegression_PushDuringReview(t *testing.T) {
	e := newRegEnv(t, reviewWorkflow(), regOpts{})
	e.commitOnRun("deliverable", map[string]string{"deliverable.md": "legit work\n"})
	if err := e.submit(0, "completed", ""); err != nil {
		t.Fatalf("submit execute: %v", err)
	}
	e.commitOnRun("sneak", sneakFiles)

	err := e.submit(1, "accepted", "")
	requireReason(t, err, domain.ErrConflict, engine.ReasonRunBranchMovedSinceGate)
	e.requireMainProtected("main")
	if r := e.runNow(); r.Status != domain.RunStatusActive {
		t.Fatalf("run status = %s, want active", r.Status)
	}
	if rev := e.execOf("review"); rev.Status == domain.StepStatusCompleted || rev.OutcomeID != "" {
		t.Fatalf("review execution = %s/%q, want not completed with no outcome", rev.Status, rev.OutcomeID)
	}
}

// TestMergeBindingRegression_PushInCommittingWindow is AC 1(b).
func TestMergeBindingRegression_PushInCommittingWindow(t *testing.T) {
	e := newRegEnv(t, reviewWorkflow(), regOpts{})
	e.flaky.failMerges = 1
	e.commitOnRun("deliverable", map[string]string{"deliverable.md": "legit work\n"})
	if err := e.submit(0, "completed", ""); err != nil {
		t.Fatalf("submit execute: %v", err)
	}
	if err := e.submit(1, "accepted", ""); err != nil {
		t.Fatalf("submit review: %v", err)
	}
	if r := e.runNow(); r.Status != domain.RunStatusCommitting {
		t.Fatalf("run status after the failed merge = %s, want committing", r.Status)
	}
	tip := e.commitOnRun("sneak", sneakFiles)

	if err := e.orch.MergeRunBranch(context.Background(), e.run.RunID); err != nil { // the scheduler retry
		t.Fatalf("MergeRunBranch retry: %v", err)
	}
	if r := e.runNow(); r.Status != domain.RunStatusCompleted {
		t.Fatalf("run status = %s, want completed", r.Status)
	}
	if !e.mainHas("main", "deliverable.md") {
		t.Fatal("main lacks the reviewed deliverable")
	}
	e.requireMainProtected("main")
	if e.isAncestor(tip, "main") {
		t.Fatal("the commit pushed after approval is an ancestor of main")
	}
	if !e.refExists("refs/heads/" + e.branch) {
		t.Fatal("run branch deleted; its excluded commits must be kept (owner question 3)")
	}
}

// TestMergeBindingRegression_SneakAfterValidate is AC 2(a), from
// poc/verify-r1-1/toctou_merge_test.go.
func TestMergeBindingRegression_SneakAfterValidate(t *testing.T) {
	e := newRegEnv(t, taskDefaultWorkflow(), regOpts{})
	e.commitOnRun("legit deliverable", map[string]string{"src/feature.go": "package src\n// legit\n"})
	if err := e.submit(0, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.submit(1, "passed", ""); err != nil {
		t.Fatal(err)
	}
	e.commitOnRun("sneak", toctouSneak)
	requireReason(t, e.submit(2, "accepted", ""), domain.ErrConflict, engine.ReasonRunBranchMovedSinceGate)
	for p := range toctouSneak {
		if p == ".spine/branch-protection.yaml" {
			continue
		}
		if e.mainHas("main", p) {
			t.Fatalf("main carries %s", p)
		}
	}
	e.requireMainProtected("main")
}

// TestMergeBindingRegression_AssertionUnderRebind is AC 2(b): even under
// the rebind policy, a reviewer's assertion of what they read refuses the
// approval of anything else.
func TestMergeBindingRegression_AssertionUnderRebind(t *testing.T) {
	e := newRegEnv(t, taskDefaultWorkflow(), regOpts{policy: engine.MovedHeadRebind})
	e.commitOnRun("legit deliverable", map[string]string{"src/feature.go": "package src\n// legit\n"})
	if err := e.submit(0, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.submit(1, "passed", ""); err != nil {
		t.Fatal(err)
	}
	reviewed := e.runHead()
	mainBefore := e.git("rev-parse", "main")
	e.commitOnRun("sneak", toctouSneak)
	requireReason(t, e.submit(2, "accepted", reviewed), domain.ErrConflict, engine.ReasonRunBranchMoved)
	if got := e.git("rev-parse", "main"); got != mainBefore {
		t.Fatalf("main moved: %s -> %s", mainBefore, got)
	}
}

// TestMergeBindingRegression_CleanRun is AC 2(c).
func TestMergeBindingRegression_CleanRun(t *testing.T) {
	e := newRegEnv(t, taskDefaultWorkflow(), regOpts{})
	e.commitOnRun("legit deliverable", map[string]string{"src/feature.go": "package src\n// legit\n"})
	if err := e.submit(0, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.submit(1, "passed", ""); err != nil {
		t.Fatal(err)
	}
	head := e.runHead()
	if err := e.submit(2, "accepted", head); err != nil {
		t.Fatalf("clean approval with a matching assertion: %v", err)
	}
	if r := e.runNow(); r.Status != domain.RunStatusCompleted {
		t.Fatalf("run status = %s, want completed", r.Status)
	}
	if p := e.execOf("publish"); p == nil || p.OutcomeID != "published" {
		t.Fatalf("publish execution = %+v, want outcome published", p)
	}
	if !e.mainHas("main", "src/feature.go") {
		t.Fatal("main lacks src/feature.go")
	}
	if got := e.mainFile("main", "tasks/t.md"); !strings.Contains(got, "status: Completed") {
		t.Fatalf("task status on main: %q", got)
	}
	o, err := e.store.GetRepositoryMergeOutcome(context.Background(), e.run.RunID, repository.PrimaryRepositoryID)
	if err != nil || o.Status != domain.RepositoryMergeStatusMerged {
		t.Fatalf("primary outcome = %+v, %v", o, err)
	}
	bound := e.runNow().CommitMeta[engine.CommitMetaBoundSHA]
	if bound != head || !e.isAncestor(bound, o.MergeCommitSHA) {
		t.Fatalf("bound %s (head %s) is not an ancestor of the merge %s", bound, head, o.MergeCommitSHA)
	}
}

// TestMergeBindingRegression_AutoPushPublishesTheMerge is AC 3: the
// committing-window replay with a bare origin.
func TestMergeBindingRegression_AutoPushPublishesTheMerge(t *testing.T) {
	e := newRegEnv(t, reviewWorkflow(), regOpts{autoPush: true})
	e.flaky.failMerges = 1
	e.commitOnRun("deliverable", map[string]string{"deliverable.md": "legit work\n"})
	if err := e.submit(0, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.submit(1, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	tip := e.commitOnRun("sneak", sneakFiles)
	if err := e.orch.MergeRunBranch(context.Background(), e.run.RunID); err != nil {
		t.Fatal(err)
	}
	if len(e.flaky.results) != 1 {
		t.Fatalf("MergeBound results = %d", len(e.flaky.results))
	}
	originMain := regGit(t, e.origin, "rev-parse", "refs/heads/main")
	if originMain != e.flaky.results[0].SHA {
		t.Fatalf("origin main = %s, want the merge commit %s", originMain, e.flaky.results[0].SHA)
	}
	// The sneak commit was never pushed to origin by anyone but an
	// attacker-controlled path; it must not ride along with the merge.
	if exec.Command("git", "-C", e.origin, "cat-file", "-e", tip+"^{commit}").Run() == nil &&
		exec.Command("git", "-C", e.origin, "merge-base", "--is-ancestor", tip, "refs/heads/main").Run() == nil {
		t.Fatal("the sneak commit is reachable from origin main")
	}
}

// originCounts returns the number of merge and non-merge commits on
// origin's main that carry the run's Run-ID trailer.
func (e *regEnv) originCounts() (merges, others int) {
	e.t.Helper()
	grep := "--grep=Run-ID: " + e.run.RunID
	m := regGit(e.t, e.origin, "rev-list", "--count", "--merges", grep, "refs/heads/main")
	n := regGit(e.t, e.origin, "rev-list", "--count", "--no-merges", grep, "refs/heads/main")
	return atoi(e.t, m), atoi(e.t, n)
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func (e *regEnv) driveTaskDefaultToApproval() {
	e.t.Helper()
	e.driveTaskDefaultWith(map[string]string{"src/feature.go": "package src\n// legit\n"})
}

// driveTaskDefaultWith commits files on the run branch and submits
// execute, validate and an approving review.
func (e *regEnv) driveTaskDefaultWith(files map[string]string) {
	e.t.Helper()
	e.commitOnRun("legit deliverable", files)
	if err := e.submit(0, "completed", ""); err != nil {
		e.t.Fatal(err)
	}
	if err := e.submit(1, "passed", ""); err != nil {
		e.t.Fatal(err)
	}
	if err := e.submit(2, "accepted", ""); err != nil {
		e.t.Fatal(err)
	}
}

const childDraft = "---\nid: TASK-002\ntype: Task\ntitle: Child\nstatus: Draft\n---\n# Child\n"

// TestMergeBindingRegression_TransientPushRecovers is AC 24 (DC-1, DC-2).
// Each case runs with a cascade that rewrites only the task file and with
// one that also rewrites a child artifact the run added (review round 1:
// a replay after the local merge landed used to diff against a main that
// already held the bound commit, shrink the cascade to the task file and
// merge a second cascade commit and a second merge commit).
func TestMergeBindingRegression_TransientPushRecovers(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, lands := range []bool{false, true} {
			name := "push never landed"
			if lands {
				name = "push landed, client saw an error"
			}
			if child {
				name += ", cascade with a child artifact"
			}
			t.Run(name, func(t *testing.T) {
				testTransientPushRecovers(t, lands, child)
			})
		}
	}
}

func testTransientPushRecovers(t *testing.T, lands, child bool) {
	e := newRegEnv(t, taskDefaultWorkflow(), regOpts{autoPush: true})
	e.flaky.failPushes, e.flaky.pushLands = 1, lands
	files := map[string]string{"src/feature.go": "package src\n// legit\n"}
	if child {
		files["tasks/child.md"] = childDraft
	}
	e.driveTaskDefaultWith(files)
	if r := e.runNow(); r.Status != domain.RunStatusCommitting {
		t.Fatalf("run status after the push blip = %s, want committing", r.Status)
	}
	if err := e.orch.MergeRunBranch(context.Background(), e.run.RunID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if r := e.runNow(); r.Status != domain.RunStatusCompleted {
		t.Fatalf("run status after the retry = %s, want completed", r.Status)
	}
	if p := e.execOf("publish"); p.OutcomeID != "published" {
		t.Fatalf("publish outcome = %q, want published (no merge_failed route)", p.OutcomeID)
	}
	if e.execOf("execute").Attempt != 1 {
		t.Fatal("merge_failed routed the run back to execute")
	}
	bound := e.runNow().CommitMeta[engine.CommitMetaBoundSHA]
	if exec.Command("git", "-C", e.origin, "merge-base", "--is-ancestor", bound, "refs/heads/main").Run() != nil {
		t.Fatal("origin main lacks the bound commit")
	}
	if m, c := e.originCounts(); m != 1 || c != 1 {
		t.Fatalf("origin main carries %d merge and %d cascade commits for the run, want 1 and 1:\n%s",
			m, c, regGit(t, e.origin, "log", "--oneline", "--graph", "refs/heads/main"))
	}
	if child {
		if got := regGit(t, e.origin, "show", "refs/heads/main:tasks/child.md"); !strings.Contains(got, "status: Completed") {
			t.Fatalf("origin main:tasks/child.md = %q, want the cascade's status Completed", got)
		}
	}

	// (d) Replay after a landed merge: the partially-merged resume
	// shape moves the run back to committing; nothing is added.
	mainBefore := regGit(t, e.origin, "rev-parse", "refs/heads/main")
	localBefore := e.git("rev-parse", "refs/heads/main")
	if err := e.store.UpdateRunStatus(context.Background(), e.run.RunID, domain.RunStatusCommitting); err != nil {
		t.Fatal(err)
	}
	if err := e.orch.MergeRunBranch(context.Background(), e.run.RunID); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := regGit(t, e.origin, "rev-parse", "refs/heads/main"); got != mainBefore {
		t.Fatalf("replay moved origin main %s -> %s:\n%s", mainBefore, got, regGit(t, e.origin, "log", "--oneline", "--graph", "refs/heads/main"))
	}
	if got := e.git("rev-parse", "refs/heads/main"); got != localBefore {
		t.Fatalf("replay moved local main %s -> %s", localBefore, got)
	}

	if lands {
		return
	}
	// (c) No workspace wedge: an unrelated second run merges and
	// pushes.
	res, err := e.orch.StartRun(context.Background(), "tasks/t.md")
	if err != nil {
		t.Fatalf("second StartRun: %v", err)
	}
	second := &regEnv{t: t, dir: e.dir, origin: e.origin, cli: e.cli, flaky: e.flaky, store: e.store,
		actors: e.actors, orch: e.orch, run: res.Run, branch: res.Run.BranchName}
	second.commitOnRun("second deliverable", map[string]string{"src/second.go": "package src\n"})
	for _, st := range []struct{ step, outcome string }{{"execute", "completed"}, {"validate", "passed"}, {"review", "accepted"}} {
		ex := second.execOf(st.step)
		if err := e.orch.SubmitStepResult(context.Background(), ex.ExecutionID, engine.StepResult{OutcomeID: st.outcome}); err != nil {
			t.Fatalf("second run %s: %v", st.step, err)
		}
	}
	if r := second.runNow(); r.Status != domain.RunStatusCompleted {
		t.Fatalf("second run status = %s, want completed", r.Status)
	}
	if exec.Command("git", "-C", e.origin, "cat-file", "-e", "refs/heads/main:src/second.go").Run() != nil {
		t.Fatal("origin main lacks the second run's deliverable")
	}
}

// misMergingCLI stands in for a faulty CommitBinder (the structural check
// guards implementations other than CLIClient, such as SMP TASK-009's):
// while armed, MergeBound merges `instead` — a commit the review never saw
// — in place of opts.SourceSHA. With concurrentWrite set it instead
// models the unserialized-worktree race: another writer commits on the
// target between the base read and the merge, so the merge's first parent
// is that write while the reported base is the tip before it.
type misMergingCLI struct {
	*git.CLIClient
	dir             string
	t               *testing.T
	armed           bool
	instead         string
	concurrentWrite bool
	write           string // the concurrent write's commit
	refused         git.MergeResult
}

func (f *misMergingCLI) MergeBound(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	if !f.armed {
		return f.CLIClient.MergeBound(ctx, opts)
	}
	f.armed = false
	if f.concurrentWrite {
		base := regGit(f.t, f.dir, "rev-parse", "refs/heads/main")
		regGit(f.t, f.dir, "checkout", "-q", "main")
		if err := os.WriteFile(filepath.Join(f.dir, "concurrent.md"), []byte("an in-place write\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture
			f.t.Fatal(err)
		}
		regGit(f.t, f.dir, "add", "concurrent.md")
		regGit(f.t, f.dir, "commit", "-q", "-m", "in-place write during the merge")
		f.write = regGit(f.t, f.dir, "rev-parse", "HEAD")
		res, err := f.CLIClient.MergeBound(ctx, opts)
		res.BaseSHA = base // the tip the merge read before the write landed
		f.refused = res
		return res, err
	}
	opts.SourceSHA = f.instead
	res, err := f.CLIClient.MergeBound(ctx, opts)
	f.refused = res
	return res, err
}

// TestMergeBindingRegression_RefusedMergeTakenBack (review round 1): a
// merge the structural check refuses is taken off the local main, so the
// next governed merge neither builds on it nor publishes it. Before the
// take-back, the refused merge stayed on local main: the next round's
// MergeBound computed on it, passed its own check with it as first parent,
// and its non-forced push published the sneak to origin; with auto-push
// off the local main is the published branch.
func TestMergeBindingRegression_RefusedMergeTakenBack(t *testing.T) {
	for _, autoPush := range []bool{true, false} {
		name := "auto-push on"
		if !autoPush {
			name = "auto-push off"
		}
		t.Run(name, func(t *testing.T) {
			var mm *misMergingCLI
			e := newRegEnv(t, taskDefaultWorkflow(), regOpts{autoPush: autoPush, op: func(c *git.CLIClient) engine.GitOperator {
				mm = &misMergingCLI{CLIClient: c, t: t}
				return mm
			}})
			mm.dir = e.dir
			// The commit the faulty binder merges: carries evil.md and
			// rules: [], and was never on the run branch the review saw.
			e.git("checkout", "-q", "-b", "sneak-side", "main")
			e.write("evil.md", "unreviewed\n")
			e.write(".spine/branch-protection.yaml", "version: 1\nrules: []\n")
			e.git("add", "-A")
			e.git("commit", "-q", "-m", "sneak")
			mm.instead = e.git("rev-parse", "HEAD")
			e.git("checkout", "-q", "main")
			mm.armed = true
			baseBefore := e.git("rev-parse", "refs/heads/main")

			e.driveTaskDefaultWith(map[string]string{"src/feature.go": "package src\n// legit\n"})
			if p := e.execOf("publish"); p.OutcomeID != "merge_failed" {
				t.Fatalf("publish outcome = %q, want merge_failed (the refused merge)", p.OutcomeID)
			}
			if mm.refused.SHA == "" || mm.refused.SHA == baseBefore {
				t.Fatalf("the faulty MergeBound made no merge: %+v", mm.refused)
			}
			if got := e.git("rev-parse", "refs/heads/main"); got != baseBefore {
				t.Fatalf("local main = %s after the refusal, want it taken back to %s", got, baseBefore)
			}
			if e.isAncestor(mm.instead, "refs/heads/main") || e.mainHas("main", "evil.md") {
				t.Fatal("local main still carries the refused merge")
			}
			if st := e.git("status", "--porcelain"); st != "" {
				t.Fatalf("the take-back left the working tree dirty:\n%s", st)
			}
			if autoPush {
				if got := regGit(t, e.origin, "rev-parse", "refs/heads/main"); got != baseBefore {
					t.Fatalf("origin main moved to %s on a refused merge", got)
				}
			}

			// The next governed merge (the rework round) builds on the
			// restored main and publishes only reviewed content.
			e.commitOnRun("rework", map[string]string{"src/feature.go": "package src\n// legit v2\n"})
			for _, st := range []struct{ step, outcome string }{{"execute", "completed"}, {"validate", "passed"}, {"review", "accepted"}} {
				var ex *domain.StepExecution
				for _, s := range e.store.steps {
					if s.RunID == e.run.RunID && s.StepID == st.step && !s.Status.IsTerminal() {
						ex = s
					}
				}
				if ex == nil {
					t.Fatalf("no live %s execution in the rework round", st.step)
				}
				if err := e.orch.SubmitStepResult(context.Background(), ex.ExecutionID, engine.StepResult{OutcomeID: st.outcome}); err != nil {
					t.Fatalf("rework %s: %v", st.step, err)
				}
			}
			if r := e.runNow(); r.Status != domain.RunStatusCompleted {
				t.Fatalf("run status after the rework round = %s, want completed", r.Status)
			}
			published := "refs/heads/main"
			repo := e.dir
			if autoPush {
				repo = e.origin
			}
			if exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", mm.refused.SHA, published).Run() == nil {
				t.Fatalf("the refused merge %s reached the published main", mm.refused.SHA)
			}
			if exec.Command("git", "-C", repo, "cat-file", "-e", published+":evil.md").Run() == nil {
				t.Fatal("the published main carries evil.md")
			}
			if got := regGit(t, repo, "show", published+":.spine/branch-protection.yaml"); got != strings.TrimSpace(protectedMain) {
				t.Fatalf("the published branch protection = %q, want the main rule", got)
			}
			if exec.Command("git", "-C", repo, "cat-file", "-e", published+":src/feature.go").Run() != nil {
				t.Fatal("the published main lacks the reviewed deliverable")
			}
		})
	}
}

// TestMergeBindingRegression_RefusedMergeKeepsAConcurrentWrite: when the
// refusal comes from main moving between the base read and the merge
// (another writer — the worktree is not serialized until TASK-012), the
// take-back restores main to that write, not to the older base: the
// write is kept and only the refused merge goes.
func TestMergeBindingRegression_RefusedMergeKeepsAConcurrentWrite(t *testing.T) {
	var mm *misMergingCLI
	e := newRegEnv(t, taskDefaultWorkflow(), regOpts{op: func(c *git.CLIClient) engine.GitOperator {
		mm = &misMergingCLI{CLIClient: c, t: t, concurrentWrite: true}
		return mm
	}})
	mm.dir = e.dir
	mm.armed = true
	e.driveTaskDefaultWith(map[string]string{"src/feature.go": "package src\n// legit\n"})
	if p := e.execOf("publish"); p.OutcomeID != "merge_failed" {
		t.Fatalf("publish outcome = %q, want merge_failed (first parent is not the reported base)", p.OutcomeID)
	}
	if got := e.git("rev-parse", "refs/heads/main"); got != mm.write {
		t.Fatalf("local main = %s, want the concurrent write %s kept", got, mm.write)
	}
	if !e.mainHas("main", "concurrent.md") || e.mainHas("main", "src/feature.go") {
		t.Fatal("main must keep the concurrent write and drop only the refused merge")
	}
	if st := e.git("status", "--porcelain"); st != "" {
		t.Fatalf("working tree dirty after the take-back:\n%s", st)
	}
}

// captureLogs routes slog to a buffer for the rest of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestMergeBindingRegression_CascadeAtBoundAndExcludedCommits is AC 10
// and AC 11.
func TestMergeBindingRegression_CascadeAtBoundAndExcludedCommits(t *testing.T) {
	logs := captureLogs(t)
	e := newRegEnv(t, reviewWorkflow(), regOpts{})
	e.flaky.failMerges = 1
	child := "---\nid: TASK-002\ntype: Task\ntitle: Child\nstatus: Draft\n---\n# Child\n"
	e.commitOnRun("deliverable", map[string]string{"deliverable.md": "legit\n", "tasks/child.md": child})
	if err := e.submit(0, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.submit(1, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	bound := e.runNow().CommitMeta[engine.CommitMetaBoundSHA]
	late := "---\nid: TASK-003\ntype: Task\ntitle: Late\nstatus: Draft\n---\n"
	tip := e.commitOnRun("late artifact", map[string]string{"tasks/late.md": late})
	branchBefore := e.runHead()

	if err := e.orch.MergeRunBranch(context.Background(), e.run.RunID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"tasks/t.md", "tasks/child.md"} {
		if got := e.mainFile("main", p); !strings.Contains(got, "status: Completed") {
			t.Fatalf("main:%s = %q, want status Completed", p, got)
		}
	}
	if e.mainHas("main", "tasks/late.md") {
		t.Fatal("an artifact added after the bound commit reached main")
	}
	if got := e.runHead(); got != branchBefore {
		t.Fatalf("the cascade moved the run branch %s -> %s", branchBefore, got)
	}
	m := e.flaky.results[0].SHA
	if !e.isAncestor(bound, m) || e.isAncestor(tip, m) {
		t.Fatalf("IsAncestor(bound, M)=%v IsAncestor(tip, M)=%v; want true, false", e.isAncestor(bound, m), e.isAncestor(tip, m))
	}
	if !e.refExists("refs/heads/" + e.branch) {
		t.Fatal("run branch with excluded commits deleted")
	}
	warn := ""
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "commits after the bound head were not merged") {
			warn = line
		}
	}
	for _, want := range []string{"level=WARN", "run_id=" + e.run.RunID, "bound_sha=" + bound, "tip_sha=" + tip} {
		if !strings.Contains(warn, want) {
			t.Fatalf("excluded-commits warn %q lacks %q", warn, want)
		}
	}
	if status := regGit(t, e.dir, "status", "--porcelain"); status != "" {
		t.Fatalf("the bound merge left the working tree dirty:\n%s", status)
	}
}

// TestMergeBindingRegression_TipRewoundToAncestorIsNotExcluded is AC 11's
// last sentence.
func TestMergeBindingRegression_TipRewoundToAncestorIsNotExcluded(t *testing.T) {
	e := newRegEnv(t, reviewWorkflow(), regOpts{})
	e.flaky.failMerges = 1
	start := e.runHead()
	e.commitOnRun("deliverable", map[string]string{"deliverable.md": "legit\n"})
	if err := e.submit(0, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.submit(1, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	e.git("update-ref", "refs/heads/"+e.branch, start) // rewound to an ancestor of the bound commit
	if err := e.orch.MergeRunBranch(context.Background(), e.run.RunID); err != nil {
		t.Fatal(err)
	}
	if r := e.runNow(); r.Status != domain.RunStatusCompleted {
		t.Fatalf("run status = %s", r.Status)
	}
	if !e.mainHas("main", "deliverable.md") {
		t.Fatal("main lacks the bound deliverable")
	}
	if e.refExists("refs/heads/" + e.branch) {
		t.Fatal("a rewound (contained) tip is not excluded: the branch must be cleaned up")
	}
}

// TestMergeBindingRegression_TipResetToMainIsNotAViolation is AC 25
// (DC-3): resetting the run branch to main's tip after approval must not
// refuse the approved merge.
func TestMergeBindingRegression_TipResetToMainIsNotAViolation(t *testing.T) {
	logs := captureLogs(t)
	e := newRegEnv(t, taskDefaultWorkflow(), regOpts{})
	e.flaky.failMerges = 1
	e.write("other.md", "main moved on\n")
	e.git("add", "other.md")
	e.git("commit", "-q", "-m", "main advances after the run started")
	e.driveTaskDefaultToApproval()
	bound := e.runNow().CommitMeta[engine.CommitMetaBoundSHA]
	e.git("update-ref", "refs/heads/"+e.branch, e.git("rev-parse", "refs/heads/main"))
	if err := e.orch.MergeRunBranch(context.Background(), e.run.RunID); err != nil {
		t.Fatal(err)
	}
	if r := e.runNow(); r.Status != domain.RunStatusCompleted {
		t.Fatalf("run status = %s, want completed", r.Status)
	}
	if !e.isAncestor(bound, "main") {
		t.Fatal("main lacks the bound commit")
	}
	if p := e.execOf("publish"); p.OutcomeID != "published" {
		t.Fatalf("publish outcome = %q, want published", p.OutcomeID)
	}
	if strings.Contains(logs.String(), "commits after the bound head were not merged") {
		t.Fatal("a tip contained in the merge was reported as excluded")
	}
}

// recordingCLI records the primary-path calls of one bound merge.
type recordingCLI struct {
	*git.CLIClient
	mu    sync.Mutex
	calls []string
	merge git.MergeResult
	onto  string
}

func (r *recordingCLI) rec(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *recordingCLI) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}

func (r *recordingCLI) Checkout(ctx context.Context, b string) error {
	r.rec("Checkout " + b)
	return r.CLIClient.Checkout(ctx, b)
}

func (r *recordingCLI) Commit(ctx context.Context, o git.CommitOpts) (git.CommitResult, error) {
	r.rec("Commit")
	return r.CLIClient.Commit(ctx, o)
}

func (r *recordingCLI) Merge(ctx context.Context, o git.MergeOpts) (git.MergeResult, error) {
	if o.Strategy == "abort" || o.Source == "--abort" {
		r.rec("Merge abort")
	} else {
		r.rec("Merge " + o.Source)
	}
	return r.CLIClient.Merge(ctx, o)
}

func (r *recordingCLI) Push(ctx context.Context, remote, ref string) error {
	r.rec("Push " + remote + " " + ref)
	return r.CLIClient.Push(ctx, remote, ref)
}

func (r *recordingCLI) WriteAndStageFile(ctx context.Context, p, c string) error {
	r.rec("WriteAndStageFile " + p)
	return r.CLIClient.WriteAndStageFile(ctx, p, c)
}

func (r *recordingCLI) CommitFilesOnto(ctx context.Context, base string, files map[string]string, o git.CommitOpts) (git.CommitResult, error) {
	res, err := r.CLIClient.CommitFilesOnto(ctx, base, files, o)
	r.rec("CommitFilesOnto " + base)
	r.mu.Lock()
	r.onto = res.SHA
	r.mu.Unlock()
	return res, err
}

func (r *recordingCLI) MergeBound(ctx context.Context, o git.MergeOpts) (git.MergeResult, error) {
	r.rec("MergeBound " + o.SourceSHA)
	res, err := r.CLIClient.MergeBound(ctx, o)
	r.mu.Lock()
	r.merge = res
	r.mu.Unlock()
	return res, err
}

func (r *recordingCLI) PushCommit(ctx context.Context, remote, branch, sha string) error {
	r.rec("PushCommit " + remote + " " + branch + " " + sha)
	return r.CLIClient.PushCommit(ctx, remote, branch, sha)
}

// TestMergeBindingRegression_CallSequence is AC 26 (catches M4′).
func TestMergeBindingRegression_CallSequence(t *testing.T) {
	var rec *recordingCLI
	e := newRegEnv(t, reviewWorkflow(), regOpts{autoPush: true, op: func(c *git.CLIClient) engine.GitOperator {
		rec = &recordingCLI{CLIClient: c}
		return rec
	}})
	e.commitOnRun("deliverable", map[string]string{"deliverable.md": "legit\n"})
	if err := e.submit(0, "completed", ""); err != nil {
		t.Fatal(err)
	}
	rec.reset()
	if err := e.submit(1, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	if r := e.runNow(); r.Status != domain.RunStatusCompleted {
		t.Fatalf("run status = %s", r.Status)
	}
	var mergeBound, pushCommit []string
	for _, c := range rec.calls {
		switch {
		case strings.HasPrefix(c, "MergeBound "):
			mergeBound = append(mergeBound, c)
		case strings.HasPrefix(c, "PushCommit "):
			pushCommit = append(pushCommit, c)
		case strings.HasPrefix(c, "Push "), c == "Commit", strings.HasPrefix(c, "WriteAndStageFile"),
			c == "Checkout "+e.branch, strings.HasPrefix(c, "Merge ") && c != "Merge abort":
			t.Fatalf("binding-on merge path called %q (all calls: %q)", c, rec.calls)
		}
	}
	if rec.onto == "" {
		t.Fatal("the status cascade made no commit")
	}
	if len(mergeBound) != 1 || mergeBound[0] != "MergeBound "+rec.onto {
		t.Fatalf("MergeBound calls = %q, want exactly one of the cascade commit %s", mergeBound, rec.onto)
	}
	if len(pushCommit) != 1 || pushCommit[0] != "PushCommit origin main "+rec.merge.SHA {
		t.Fatalf("PushCommit calls = %q, want exactly one of the merge %s", pushCommit, rec.merge.SHA)
	}
	if got := regGit(t, e.origin, "rev-parse", "refs/heads/main"); got != rec.merge.SHA {
		t.Fatalf("origin main = %s, want %s", got, rec.merge.SHA)
	}
}
