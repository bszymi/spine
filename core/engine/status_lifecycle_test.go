package engine

// INIT-023 TASK-017 (audit P3-21) regression tests: the real
// Orchestrator.MergeRunBranch over a real git.CLIClient, with merge binding
// and the declared status lifecycle on. The P3-21 PoC (a standard run that
// publishes a sibling's and an initiative's status change, and a cascade
// that stamps every branch-added artifact) is ported here inverted; the
// design-check PoCs (rewrite differential, artifacts_dir, union merge)
// likewise. On refusal every test asserts that main and the bound commit
// are unchanged.

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
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/testutil"
)

const (
	lcRunID   = "run-lc000001"
	lcBranch  = "spine/run/" + lcRunID
	lcTask    = "initiatives/init-001/epics/epic-001/tasks/task-001.md"
	lcSibling = "initiatives/init-001/epics/epic-001/tasks/task-002.md"
	lcInit    = "initiatives/init-001/initiative.md"
	lcNewTask = "initiatives/init-001/epics/epic-001/tasks/task-003.md"
	lcNewADR  = "architecture/adr/ADR-0099-new.md"
)

// lcArt is a minimal artifact (the design-phase PoC's shape).
func lcArt(id, typ, status string) string {
	return "---\nid: " + id + "\ntype: " + typ + "\ntitle: " + id + "\nstatus: " + status + "\n---\n# " + id + "\n"
}

// lcRepo is a temporary repository with main and the run branch.
type lcRepo struct {
	t   *testing.T
	dir string
	cli *git.CLIClient
}

func newLCRepo(t *testing.T, seed map[string]string) *lcRepo {
	t.Helper()
	testutil.HermeticGit(t)
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")
	r := &lcRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	for p, c := range seed {
		r.write(p, c)
	}
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", "seed")
	r.git("branch", lcBranch)
	r.cli = git.NewCLIClient(r.dir)
	return r
}

func (r *lcRepo) git(args ...string) string {
	r.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", r.dir}, args...)...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *lcRepo) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil { //nolint:gosec // G306: test fixture
		r.t.Fatal(err)
	}
}

func (r *lcRepo) rm(rel string) { r.git("rm", "-q", rel) }

func (r *lcRepo) symlink(rel, target string) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		r.t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		r.t.Fatal(err)
	}
}

// on commits edit's changes on branch and goes back to main.
func (r *lcRepo) on(branch, msg string, edit func()) string {
	r.t.Helper()
	r.git("checkout", "-q", branch)
	edit()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
	head := r.git("rev-parse", "HEAD")
	r.git("checkout", "-q", "main")
	return head
}

func (r *lcRepo) onRun(msg string, edit func()) string { return r.on(lcBranch, msg, edit) }
func (r *lcRepo) onMain(msg string, edit func())       { r.on("main", msg, edit) }
func (r *lcRepo) head(ref string) string               { return r.git("rev-parse", ref) }

// statusAt is the lifecycle status of path at rev: "-" when absent, "!"
// when it is not a lifecycle artifact there.
func (r *lcRepo) statusAt(rev, path string) string {
	r.t.Helper()
	out, err := exec.Command("git", "-C", r.dir, "show", rev+":"+path).Output()
	if err != nil {
		return "-"
	}
	_, s, ok := artifact.LifecycleFields(out)
	if !ok {
		return "!"
	}
	return string(s)
}

// countingCLI counts the merges an orchestrator asks for.
type countingCLI struct {
	*git.CLIClient
	mu         sync.Mutex
	merges     int
	failPushes int // PushCommit fails transiently this many times
	// beforeMergeBound, when set, runs once before the next MergeBound:
	// another writer landing on main between the lifecycle's judgement
	// and the merge.
	beforeMergeBound func()
}

func (c *countingCLI) PushCommit(ctx context.Context, remote, branch, sha string) error {
	c.mu.Lock()
	fail := c.failPushes > 0
	if fail {
		c.failPushes--
	}
	c.mu.Unlock()
	if fail {
		return &git.GitError{Kind: git.ErrKindTransient, Op: "push", Message: "network error"}
	}
	return c.CLIClient.PushCommit(ctx, remote, branch, sha)
}

func (c *countingCLI) Merge(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	if opts.Strategy != "abort" {
		c.mu.Lock()
		c.merges++
		c.mu.Unlock()
	}
	return c.CLIClient.Merge(ctx, opts)
}

func (c *countingCLI) MergeBound(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	c.mu.Lock()
	c.merges++
	hook := c.beforeMergeBound
	c.beforeMergeBound = nil
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return c.CLIClient.MergeBound(ctx, opts)
}

type lcOpts struct {
	mode       domain.RunMode
	taskPath   string
	meta       map[string]string // commit meta without the binding
	wf         *domain.WorkflowDefinition
	noBinding  bool
	noLifecyle bool
	dir        string // artifacts_dir
	boundAt    string // bind this commit instead of the run-branch tip
}

type lcEnv struct {
	r     *lcRepo
	git   *countingCLI
	store *mockRunStore
	orch  *Orchestrator
	run   *domain.Run
	bound string
	main0 string // main before the merge
}

func newLCEnv(t *testing.T, r *lcRepo, o lcOpts) *lcEnv {
	t.Helper()
	if o.mode == "" {
		o.mode = domain.RunModeStandard
	}
	if o.taskPath == "" {
		o.taskPath = lcTask
	}
	meta := map[string]string{}
	for k, v := range o.meta {
		meta[k] = v
	}
	e := &lcEnv{r: r, git: &countingCLI{CLIClient: r.cli}, main0: r.head("main")}
	e.bound = o.boundAt
	if e.bound == "" {
		e.bound = r.head(lcBranch)
	}
	if !o.noBinding {
		meta[CommitMetaBoundSHA] = e.bound
		meta[CommitMetaBoundBy] = boundByStep("review", lcRunID+"-review-1")
	}
	e.run = &domain.Run{
		RunID: lcRunID, TraceID: "trace-lifecycle-0123456789", Status: domain.RunStatusCommitting,
		Mode: o.mode, TaskPath: o.taskPath, BranchName: lcBranch, CommitMeta: meta,
		WorkflowPath: "workflows/wf.yaml", WorkflowVersion: "v1",
	}
	e.store = &mockRunStore{runs: map[string]*domain.Run{lcRunID: e.run}}
	var loader WorkflowLoader = &stubWorkflowLoader{}
	if o.wf != nil {
		loader = &mockWorkflowLoader{wfDef: o.wf}
		e.run.WorkflowPath, e.run.WorkflowID = o.wf.Path, o.wf.ID
		if findStepDef(o.wf, "publish") != nil {
			e.store.createdSteps = append(e.store.createdSteps,
				&domain.StepExecution{ExecutionID: lcRunID + "-review-1", RunID: lcRunID, StepID: "review",
					Status: domain.StepStatusCompleted, OutcomeID: "accepted", Attempt: 1},
				&domain.StepExecution{ExecutionID: lcRunID + "-publish-1", RunID: lcRunID, StepID: "publish",
					Status: domain.StepStatusInProgress, ActorID: EngineMergeActorID, Attempt: 1})
			e.run.CurrentStepID = "publish"
		}
	}
	e.orch = &Orchestrator{
		workflows: &mockWorkflowResolver{}, store: e.store, actors: &mockActorAssigner{},
		artifacts: &mockArtifactReader{}, events: &mockEventEmitter{}, git: e.git, wfLoader: loader,
		policy: branchprotect.NewPermissive(),
	}
	if !o.noBinding {
		if err := e.orch.EnableMergeBinding(MergeBindingConfig{}); err != nil {
			t.Fatal(err)
		}
	}
	if !o.noLifecyle {
		e.orch.WithStatusLifecycle(domain.DefaultStatusLifecycle())
	}
	if o.dir != "" {
		e.orch.WithArtifactsDir(o.dir)
	}
	return e
}

func (e *lcEnv) merge() {
	e.r.t.Helper()
	if err := e.orch.MergeRunBranch(context.Background(), lcRunID); err != nil {
		e.r.t.Fatalf("MergeRunBranch: %v", err)
	}
}

func (e *lcEnv) outcome() *domain.RepositoryMergeOutcome {
	for i := range e.store.mergeOutcomes {
		if e.store.mergeOutcomes[i].RepositoryID == repository.PrimaryRepositoryID {
			return &e.store.mergeOutcomes[i]
		}
	}
	return nil
}

// plan runs the lifecycle evaluation the merge runs, without merging.
func (e *lcEnv) plan() (*lifecyclePlan, *StatusLifecycleError) {
	e.r.t.Helper()
	_, plan, err := e.orch.applyStatusLifecycleAt(context.Background(), e.run, e.run.CommitMeta["status"], e.bound)
	var le *StatusLifecycleError
	if errors.As(err, &le) {
		return nil, le
	}
	if err != nil {
		e.r.t.Fatalf("applyStatusLifecycleAt: %v", err)
	}
	return plan, nil
}

// requireRefused merges and asserts the lifecycle refusal: nothing merged
// (main and the run branch unchanged), the outcome failed/precondition
// with a status_lifecycle_violation detail naming want, and the run out of
// committing (failed for the stub loader's workflow).
func (e *lcEnv) requireRefused(want ...string) {
	t := e.r.t
	t.Helper()
	_, le := e.plan()
	if le == nil {
		t.Fatal("the plan accepted the change")
	}
	reasons := map[string]bool{}
	for _, v := range le.Violations {
		reasons[v.Reason] = true
	}
	for _, w := range want {
		if !reasons[w] {
			t.Fatalf("violations = %+v, want reason %s", le.Violations, w)
		}
	}
	tip := e.r.head(lcBranch)
	e.merge()
	if got := e.r.head("main"); got != e.main0 {
		t.Fatalf("main moved: %s -> %s", e.main0, got)
	}
	if got := e.r.head(lcBranch); got != tip {
		t.Fatalf("run branch moved: %s -> %s", tip, got)
	}
	if e.git.merges != 0 {
		t.Fatalf("%d merges attempted", e.git.merges)
	}
	o := e.outcome()
	if o == nil || o.Status != domain.RepositoryMergeStatusFailed || o.FailureClass != domain.MergeFailurePrecondition ||
		!strings.HasPrefix(o.FailureDetail, "status_lifecycle_violation:") {
		t.Fatalf("primary outcome = %+v, want failed/precondition status_lifecycle_violation", o)
	}
	if e.run.Status == domain.RunStatusCommitting || e.run.Status == domain.RunStatusCompleted {
		t.Fatalf("run status = %s, want routed (failed or active)", e.run.Status)
	}
}

// requireMerged merges and asserts the run completed.
func (e *lcEnv) requireMerged() {
	t := e.r.t
	t.Helper()
	if _, le := e.plan(); le != nil {
		t.Fatalf("the plan refused: %v", le)
	}
	e.merge()
	if e.run.Status != domain.RunStatusCompleted {
		t.Fatalf("run status = %s (outcome %+v), want completed", e.run.Status, e.outcome())
	}
	if e.r.head("main") == e.main0 {
		t.Fatal("main did not move")
	}
}

// p321Repo is the design-phase PoC repository: task-001 Pending (the
// run's), task-002 Completed and INIT-001 Pending on main; the branch adds
// work, task-003 (Draft) and ADR-0099 (Proposed), and — unless clean —
// edits task-002 Completed -> Draft and INIT-001 Pending -> Completed.
func p321Repo(t *testing.T, clean bool) *lcRepo {
	t.Helper()
	r := newLCRepo(t, map[string]string{
		lcTask:    lcArt("TASK-001", "Task", "Pending"),
		lcSibling: lcArt("TASK-002", "Task", "Completed"),
		lcInit:    lcArt("INIT-001", "Initiative", "Pending"),
	})
	r.onRun("run work", func() {
		r.write("src/code.go", "package x\n")
		if !clean {
			r.write(lcSibling, lcArt("TASK-002", "Task", "Draft"))
			r.write(lcInit, lcArt("INIT-001", "Initiative", "Completed"))
		}
		r.write(lcNewTask, lcArt("TASK-003", "Task", "Draft"))
		r.write(lcNewADR, lcArt("ADR-0099", "ADR", "Proposed"))
	})
	return r
}

// TestStatusLifecycle_P321StandardRunRefused is AC 5: the P3-21 PoC as the
// regression.
func TestStatusLifecycle_P321StandardRunRefused(t *testing.T) {
	r := p321Repo(t, false)
	e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})

	_, le := e.plan()
	if le == nil {
		t.Fatal("the PoC's side edits were accepted")
	}
	want := []StatusLifecycleViolation{
		{Path: lcSibling, Type: "Task", From: "Completed", To: "Draft", Reason: LifecycleNonOwnedStatusChange},
		{Path: lcInit, Type: "Initiative", From: "Pending", To: "Completed", Reason: LifecycleNonOwnedStatusChange},
	}
	if len(le.Violations) != len(want) {
		t.Fatalf("violations = %+v, want %+v", le.Violations, want)
	}
	for i := range want {
		if v := le.Violations[i]; v.Path != want[i].Path || v.Type != want[i].Type || v.From != want[i].From ||
			v.To != want[i].To || v.Reason != want[i].Reason || len(v.Rules) != 0 {
			t.Fatalf("violation %d = %+v, want %+v", i, v, want[i])
		}
	}

	tip := r.head(lcBranch)
	e.merge()
	if r.head("main") != e.main0 || r.head(lcBranch) != tip || e.git.merges != 0 {
		t.Fatal("something was merged or the run branch moved")
	}
	if e.run.Status != domain.RunStatusFailed {
		t.Fatalf("run status = %s, want failed (no publish step)", e.run.Status)
	}
	o := e.outcome()
	wantDetail := "status_lifecycle_violation: " + lcSibling + ": non_owned_status_change (Task Completed -> Draft); " +
		lcInit + ": non_owned_status_change (Initiative Pending -> Completed)"
	if o == nil || o.Status != domain.RepositoryMergeStatusFailed || o.FailureClass != domain.MergeFailurePrecondition ||
		o.FailureDetail != wantDetail {
		t.Fatalf("primary outcome = %+v\nwant detail %q", o, wantDetail)
	}
	for _, p := range []string{lcSibling, lcInit, lcTask} {
		if r.statusAt("main", p) != map[string]string{lcSibling: "Completed", lcInit: "Pending", lcTask: "Pending"}[p] {
			t.Fatalf("main %s = %s", p, r.statusAt("main", p))
		}
	}
}

// AC 5 / AC 18 twin: the lifecycle without binding is a configuration
// error — the run stays in committing, nothing is merged, no outcome.
func TestStatusLifecycle_RequiresBinding(t *testing.T) {
	r := p321Repo(t, false)
	e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}, noBinding: true})
	err := e.orch.MergeRunBranch(context.Background(), lcRunID)
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrUnavailable || !strings.Contains(se.Message, "merge binding") {
		t.Fatalf("MergeRunBranch = %v, want ErrUnavailable naming merge binding", err)
	}
	if e.run.Status != domain.RunStatusCommitting || e.git.merges != 0 || len(e.store.mergeOutcomes) != 0 {
		t.Fatalf("run %s, merges %d, outcomes %d; want committing, none, none", e.run.Status, e.git.merges, len(e.store.mergeOutcomes))
	}
	if r.head("main") != e.main0 {
		t.Fatal("main moved")
	}
	// The branch cascade of binding-off did not run either.
	if r.statusAt(lcBranch, lcTask) != "Pending" {
		t.Fatalf("binding-off cascade wrote the run branch: %s", r.statusAt(lcBranch, lcTask))
	}
}

// TestStatusLifecycle_RunOwnedCascade is AC 6.
func TestStatusLifecycle_RunOwnedCascade(t *testing.T) {
	r := p321Repo(t, true)
	e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
	e.requireMerged()
	for p, want := range map[string]string{lcTask: "Completed", lcNewTask: "Draft", lcNewADR: "Proposed", lcSibling: "Completed", lcInit: "Pending"} {
		if got := r.statusAt("main", p); got != want {
			t.Errorf("main %s = %s, want %s", p, got, want)
		}
	}
	// Under binding the cascade commit is the merge's second parent, a
	// child of the bound commit, and it touches exactly the TaskPath.
	cascade := r.head("main^2")
	if got := r.head(cascade + "^"); got != e.bound {
		t.Fatalf("cascade parent = %s, want the bound commit %s", got, e.bound)
	}
	if files := r.git("show", "--name-only", "--format=", cascade); files != lcTask {
		t.Fatalf("cascade commit touches %q, want exactly %s", files, lcTask)
	}

	// Control: the lifecycle off reproduces today's cascade.
	r2 := p321Repo(t, true)
	e2 := newLCEnv(t, r2, lcOpts{meta: map[string]string{"status": "Completed"}, noLifecyle: true})
	e2.merge()
	if r2.statusAt("main", lcNewTask) != "Completed" || r2.statusAt("main", lcNewADR) != "Completed" {
		t.Fatalf("control: task-003 %s, ADR %s; want both stamped Completed (today)",
			r2.statusAt("main", lcNewTask), r2.statusAt("main", lcNewADR))
	}
}

// TestStatusLifecycle_PlanningCascadeSequence is AC 8 (the audit's
// smp_cascade_sequence shape, on Spine's real git client).
func TestStatusLifecycle_PlanningCascadeSequence(t *testing.T) {
	const (
		epic = "initiatives/init-001/epics/epic-009/epic.md"
		t1   = "initiatives/init-001/epics/epic-009/tasks/task-001.md"
		t2   = "initiatives/init-001/epics/epic-009/tasks/task-002.md"
	)
	r := newLCRepo(t, map[string]string{"README.md": "hello\n", lcInit: lcArt("INIT-001", "Initiative", "Pending")})
	r.onRun("plan", func() {
		r.write(epic, lcArt("EPIC-009", "Epic", "Draft"))
		r.write(t1, lcArt("TASK-001", "Task", "Draft"))
		r.write(t2, lcArt("TASK-002", "Task", "Draft"))
		r.write("README.md", "hello\nnotes\n")
	})
	e := newLCEnv(t, r, lcOpts{mode: domain.RunModePlanning, taskPath: epic, meta: map[string]string{"status": "Pending"}})
	e.requireMerged()
	files := strings.Fields(r.git("show", "--name-only", "--format=", "main^2"))
	if strings.Join(files, " ") != strings.Join([]string{epic, t1, t2}, " ") {
		t.Fatalf("cascade commit touches %v, want exactly the three artifacts", files)
	}
	for _, p := range []string{epic, t1, t2} {
		if got := r.statusAt("main", p); got != "Pending" {
			t.Errorf("main %s = %s, want Pending", p, got)
		}
	}
	if got := r.git("show", "main:README.md"); got != "hello\nnotes" {
		t.Fatalf("README.md = %q", got)
	}
}

// TestStatusLifecycle_RewriteVerification is AC 17 (R1, DC-1).
func TestStatusLifecycle_RewriteVerification(t *testing.T) {
	const fm = "---\nid: TASK-001\ntype: Task\ntitle: TASK-001\n"
	for _, c := range []struct{ name, status string }{
		{"double-quoted key", "\"status\": Completed\n"},
		{"single-quoted key", "'status': Completed\n"},
		{"spaced colon", "status : Completed\n"},
		{"complex key", "? status\n: Completed\n"},
		{"continuation line", "status: Pending\n  Completed\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending")})
			r.onRun("exotic status", func() { r.write(lcTask, fm+c.status+"---\n# TASK-001\n") })
			e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "In Progress"}})
			e.requireRefused(LifecycleStatusRewriteFailed)
		})
	}
	t.Run("planning addition", func(t *testing.T) {
		const added = "initiatives/init-001/epics/epic-001/tasks/task-009.md"
		r := newLCRepo(t, map[string]string{lcInit: lcArt("INIT-001", "Initiative", "Pending")})
		r.onRun("plan", func() {
			r.write(lcTask, lcArt("TASK-001", "Task", "Draft"))
			r.write(added, fm+"\"status\": Completed\n---\n# x\n")
		})
		e := newLCEnv(t, r, lcOpts{mode: domain.RunModePlanning, meta: map[string]string{"status": "Pending"}})
		e.requireRefused(LifecycleStatusRewriteFailed)
	})
	t.Run("an empty status value must not swallow the next key", func(t *testing.T) {
		r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending")})
		r.onRun("empty status", func() {
			r.write(lcTask, "---\ntype: Task\ntitle: TASK-001\nstatus:\nid: TASK-001\n---\n# x\n")
		})
		// The regex writer's `\s*` runs over the line break and replaces
		// "status:\nid: TASK-001" with one status line: the id is gone.
		// The status re-parses as In Progress, so only the
		// everything-else-unchanged half of R1 sees it.
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "In Progress"}})
		e.requireRefused(LifecycleStatusRewriteFailed)
	})
	t.Run("control", func(t *testing.T) {
		r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending")})
		r.onRun("work", func() { r.write("src/x.go", "package x\n") })
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "In Progress"}})
		e.requireMerged()
		if got := r.statusAt("main", lcTask); got != "In Progress" {
			t.Fatalf("main task = %s, want In Progress", got)
		}
	})
}

// TestStatusLifecycle_Binding is AC 18 (DC-2): the plan judges the bound
// commit, and it runs with no commit.status.
func TestStatusLifecycle_Binding(t *testing.T) {
	t.Run("a push after the binding is neither judged nor merged", func(t *testing.T) {
		r := p321Repo(t, true)
		bound := r.head(lcBranch)
		pushed := r.onRun("after the binding", func() { r.write(lcSibling, lcArt("TASK-002", "Task", "Draft")) })
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}, boundAt: bound})
		e.requireMerged()
		if got := r.statusAt("main", lcSibling); got != "Completed" {
			t.Fatalf("main task-002 = %s, want Completed", got)
		}
		if err := exec.Command("git", "-C", r.dir, "merge-base", "--is-ancestor", pushed, "main").Run(); err == nil {
			t.Fatal("the pushed commit reached main")
		}
	})
	t.Run("no commit.status: non-owned edits are still judged", func(t *testing.T) {
		r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending"), lcSibling: lcArt("TASK-002", "Task", "Completed")})
		r.onRun("side edit", func() { r.write(lcSibling, lcArt("TASK-002", "Task", "Pending")) })
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"merge": "true"}})
		e.requireRefused(LifecycleNonOwnedStatusChange)
	})
}

// TestStatusLifecycle_RemovalGuard is AC 19 (N3, DC-3).
func TestStatusLifecycle_RemovalGuard(t *testing.T) {
	seed := func() map[string]string {
		return map[string]string{
			lcTask:      lcArt("TASK-001", "Task", "Pending"),
			lcSibling:   lcArt("TASK-002", "Task", "Completed"),
			"notes.md":  "# not an artifact\n",
			"README.md": "hello\n",
		}
	}
	// recreated is TASK-002 under another path, with little in common
	// with the original so git reports D + A, not a rename.
	recreated := "---\nid: TASK-002\ntype: Task\ntitle: Reopened under another name\nstatus: Draft\n---\n" +
		strings.Repeat("Entirely different content that git will not pair with the deleted file.\n", 12)
	const recreatedPath = "initiatives/init-001/epics/epic-001/tasks/task-002-again.md"

	refused := []struct {
		name string
		mode domain.RunMode
		edit func(r *lcRepo)
	}{
		{"deleted", "", func(r *lcRepo) { r.rm(lcSibling) }},
		{"second status key", "", func(r *lcRepo) {
			r.write(lcSibling, "---\nid: TASK-002\ntype: Task\ntitle: TASK-002\nstatus: Completed\nstatus: Pending\n---\n")
		}},
		{"retyped", "", func(r *lcRepo) { r.write(lcSibling, lcArt("TASK-002", "Taskx", "Completed")) }},
		{"symlink", "", func(r *lcRepo) { r.symlink(lcSibling, "task-001.md") }},
		{"recreated (standard)", "", func(r *lcRepo) { r.rm(lcSibling); r.write(recreatedPath, recreated) }},
		{"recreated (planning)", domain.RunModePlanning, func(r *lcRepo) { r.rm(lcSibling); r.write(recreatedPath, recreated) }},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			r := newLCRepo(t, seed())
			taskPath := lcTask
			meta := map[string]string{"status": "Completed"}
			if c.mode == domain.RunModePlanning {
				taskPath = "initiatives/init-001/epics/epic-001/tasks/task-010.md"
				meta = map[string]string{"status": "Pending"}
			}
			r.onRun("edit", func() {
				if c.mode == domain.RunModePlanning {
					r.write(taskPath, lcArt("TASK-010", "Task", "Draft"))
				}
				c.edit(r)
			})
			if c.name == "recreated (standard)" {
				if ds := r.git("diff", "--name-status", "main", lcBranch); !strings.Contains(ds, "D\t"+lcSibling) {
					t.Fatalf("git did not report D + A:\n%s", ds)
				}
			}
			e := newLCEnv(t, r, lcOpts{mode: c.mode, taskPath: taskPath, meta: meta})
			e.requireRefused(LifecycleNonOwnedRemoval)
			if got := r.statusAt("main", lcSibling); got != "Completed" {
				t.Fatalf("main task-002 = %s", got)
			}
			if got := r.statusAt("main", recreatedPath); got != "-" {
				t.Fatalf("the recreated TASK-002 reached main: %s", got)
			}
		})
	}

	merges := []struct {
		name string
		prep func(r *lcRepo)
		edit func(r *lcRepo)
	}{
		{"delete a non-artifact .md", nil, func(r *lcRepo) { r.rm("notes.md") }},
		{"delete an artifact added on the same branch",
			func(r *lcRepo) {
				r.onRun("add", func() { r.write(lcNewTask, lcArt("TASK-003", "Task", "Draft")) })
			},
			func(r *lcRepo) { r.rm(lcNewTask) }},
		{"git mv with the status unchanged", nil, func(r *lcRepo) {
			r.git("mv", lcSibling, "initiatives/init-001/epics/epic-001/tasks/task-002-moved.md")
		}},
	}
	for _, c := range merges {
		t.Run(c.name, func(t *testing.T) {
			r := newLCRepo(t, seed())
			if c.prep != nil {
				c.prep(r)
			}
			r.onRun("edit", func() { c.edit(r) })
			e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
			e.requireMerged()
		})
	}

	t.Run("a field edit that makes the projection drop the row", func(t *testing.T) {
		// A Task the projection keeps (artifact.Parse accepts it) loses its
		// required title: type and status still read, but the projection
		// would delete its row — a removal by edit.
		full := func(title string) string {
			return "---\nid: TASK-002\ntype: Task\n" + title + "status: Completed\n" +
				"epic: /initiatives/init-001/epics/epic-001/epic.md\ninitiative: /initiatives/init-001/initiative.md\n---\n# TASK-002\n"
		}
		r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending"), lcSibling: full("title: Two\n")})
		r.onRun("drop the title", func() { r.write(lcSibling, full("")) })
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
		e.requireRefused(LifecycleNonOwnedRemoval)

		// Control: an edit that keeps it parseable merges.
		r2 := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending"), lcSibling: full("title: Two\n")})
		r2.onRun("retitle", func() { r2.write(lcSibling, full("title: Renamed\n")) })
		newLCEnv(t, r2, lcOpts{meta: map[string]string{"status": "Completed"}}).requireMerged()
	})

	t.Run("O3': a run deleting its own TaskPath", func(t *testing.T) {
		r := newLCRepo(t, seed())
		r.onRun("delete own", func() { r.rm(lcTask) })
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"merge": "true"}})
		e.requireRefused(LifecycleNotAnArtifact)
	})
}

// TestStatusLifecycle_NoDeclaredEffect is AC 20 (O1′/O2′, DC-4).
func TestStatusLifecycle_NoDeclaredEffect(t *testing.T) {
	const added = "initiatives/init-001/epics/epic-001/tasks/task-020.md"
	for _, c := range []struct {
		authored string
		refuse   bool
	}{{"Completed", true}, {"Draft", false}} {
		t.Run("workflow-lifecycle shape adds a Task authored "+c.authored, func(t *testing.T) {
			r := newLCRepo(t, map[string]string{"workflows/x.yaml": "id: x\n"})
			r.onRun("draft", func() {
				r.write("workflows/x.yaml", "id: x\nversion: 2\n")
				r.write(added, lcArt("TASK-020", "Task", c.authored))
			})
			e := newLCEnv(t, r, lcOpts{mode: domain.RunModePlanning, taskPath: "workflows/x.yaml", meta: map[string]string{"merge": "true"}})
			if c.refuse {
				e.requireRefused(LifecycleIllegalInitialStatus)
				return
			}
			e.requireMerged()
			if got := r.statusAt("main", added); got != "Draft" {
				t.Fatalf("main %s = %s, want Draft", added, got)
			}
		})
	}
	t.Run("standard run moves its own TaskPath with no effect", func(t *testing.T) {
		r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending")})
		r.onRun("self-complete", func() { r.write(lcTask, lcArt("TASK-001", "Task", "Completed")) })
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"merge": "true"}})
		e.requireRefused(LifecycleStatusChangeWithoutEffect)
	})
}

// TestStatusLifecycle_ArtifactsDir is AC 21 (path scope, DC-5).
func TestStatusLifecycle_ArtifactsDir(t *testing.T) {
	const (
		taskRel = "initiatives/i/epics/e/tasks/t.md"
		sibRel  = "initiatives/i/epics/e/tasks/u.md"
	)
	seed := func() map[string]string {
		return map[string]string{
			"spine/" + taskRel:      lcArt("TASK-001", "Task", "Pending"),
			"spine/" + sibRel:       lcArt("TASK-002", "Task", "Completed"),
			"docs/outside.md":       lcArt("TASK-077", "Task", "Completed"),
			"spine/templates/x.md":  lcArt("TASK-000", "Task", "Draft"),
			"spine/.spine-keep.txt": "x\n",
		}
	}
	t.Run("commit.status is applied under artifacts_dir", func(t *testing.T) {
		r := newLCRepo(t, seed())
		r.onRun("work", func() { r.write("spine/src.txt", "work\n") })
		e := newLCEnv(t, r, lcOpts{taskPath: taskRel, meta: map[string]string{"status": "Completed"}, dir: "spine"})
		e.requireMerged()
		if got := r.statusAt("main", "spine/"+taskRel); got != "Completed" {
			t.Fatalf("main spine/%s = %s, want Completed", taskRel, got)
		}
	})
	t.Run("a non-owned status edit under spine/ is refused", func(t *testing.T) {
		r := newLCRepo(t, seed())
		r.onRun("edit", func() { r.write("spine/"+sibRel, lcArt("TASK-002", "Task", "Pending")) })
		e := newLCEnv(t, r, lcOpts{taskPath: taskRel, meta: map[string]string{"status": "Completed"}, dir: "spine"})
		e.requireRefused(LifecycleNonOwnedStatusChange)
	})
	t.Run("edits outside the scope are ignored", func(t *testing.T) {
		r := newLCRepo(t, seed())
		r.onRun("edit", func() {
			r.write("docs/outside.md", lcArt("TASK-077", "Task", "Pending"))
			r.write("spine/templates/x.md", lcArt("TASK-000", "Task", "Completed"))
		})
		e := newLCEnv(t, r, lcOpts{taskPath: taskRel, meta: map[string]string{"status": "Completed"}, dir: "spine"})
		e.requireMerged()
	})
}

// TestStatusLifecycle_RenameOntoTaskPath is AC 23 (DC-7).
func TestStatusLifecycle_RenameOntoTaskPath(t *testing.T) {
	const drafted = "initiatives/init-001/epics/epic-001/tasks/task-050.md"
	r := newLCRepo(t, map[string]string{lcSibling: lcArt("TASK-002", "Task", "Completed")})
	r.onRun("draft", func() { r.write(drafted, lcArt("TASK-050", "Task", "Draft")) })
	r.onRun("swap", func() {
		r.rm(drafted)
		r.git("mv", lcSibling, drafted)
	})
	if ds := r.git("diff", "--name-status", "-M", "main", lcBranch); !strings.Contains(ds, "R100\t"+lcSibling+"\t"+drafted) {
		t.Fatalf("git does not report the rename onto the TaskPath:\n%s", ds)
	}
	e := newLCEnv(t, r, lcOpts{mode: domain.RunModePlanning, taskPath: drafted, meta: map[string]string{"status": "Pending"}})
	_, le := e.plan()
	found := false
	for _, v := range le.Violations {
		if v.Reason == LifecycleIllegalTransition && v.Path == drafted && v.From == "Completed" && v.To == "Pending" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations = %+v, want illegal_transition Completed -> Pending on the TaskPath", le.Violations)
	}
	e.requireRefused(LifecycleIllegalTransition, LifecycleNonOwnedRemoval)
}

// TestStatusLifecycle_MergeResultCheck is AC 24 (V1, DC-8): `*.md
// merge=union` on main merges a concurrent status change without a
// conflict; the merge result is refused and nothing is pushed.
func TestStatusLifecycle_MergeResultCheck(t *testing.T) {
	setup := func(t *testing.T, mainChange bool) (*lcRepo, string) {
		r := newLCRepo(t, map[string]string{".gitattributes": "*.md merge=union\n", lcTask: lcArt("TASK-001", "Task", "Pending")})
		origin := filepath.Join(t.TempDir(), "origin.git")
		r.git("init", "-q", "--bare", origin)
		r.git("remote", "add", "origin", origin)
		r.git("push", "-q", "origin", "main")
		t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
		r.onRun("work", func() { r.write("src.txt", "work\n") })
		if mainChange {
			r.onMain("cancel on main", func() { r.write(lcTask, lcArt("TASK-001", "Task", "Cancelled")) })
			r.git("push", "-q", "origin", "main")
		}
		return r, origin
	}
	t.Run("refused", func(t *testing.T) {
		r, origin := setup(t, true)
		originMain := strings.TrimSpace(gitIn(t, origin, "rev-parse", "main"))
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
		e.merge()
		if got := strings.TrimSpace(gitIn(t, origin, "rev-parse", "main")); got != originMain {
			t.Fatalf("origin main moved: %s -> %s", originMain, got)
		}
		if got := r.head("main"); got != e.main0 {
			t.Fatalf("local main holds the refused merge: %s (was %s)", got, e.main0)
		}
		o := e.outcome()
		if o == nil || o.FailureClass != domain.MergeFailurePrecondition ||
			!strings.HasPrefix(o.FailureDetail, "status_lifecycle_violation: "+lcTask+": merge_result_mismatch") {
			t.Fatalf("outcome = %+v, want merge_result_mismatch", o)
		}
		if e.run.Status != domain.RunStatusFailed {
			t.Fatalf("run = %s, want failed", e.run.Status)
		}
	})
	t.Run("control: no main-side change merges", func(t *testing.T) {
		r, origin := setup(t, false)
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
		e.requireMerged()
		if got := r.statusAt("main", lcTask); got != "Completed" {
			t.Fatalf("main task = %s", got)
		}
		if got := strings.TrimSpace(gitIn(t, origin, "rev-parse", "main")); got != r.head("main") {
			t.Fatalf("origin main %s, local %s", got, r.head("main"))
		}
	})
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TestStatusLifecycle_UndeclaredBaseData is AC 25 (DC-9).
func TestStatusLifecycle_UndeclaredBaseData(t *testing.T) {
	const arch = "architecture/system.md"
	body := func(status, text string) string {
		return "---\ntype: Architecture\ntitle: System\nstatus: " + status + "\n---\n" + text
	}
	t.Run("a body edit merges", func(t *testing.T) {
		r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending"), arch: body("Active", "# v1\n")})
		r.onRun("edit body", func() { r.write(arch, body("Active", "# v2\n")) })
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
		e.requireMerged()
		if got := r.statusAt("main", arch); got != "Active" {
			t.Fatalf("main %s = %s", arch, got)
		}
	})
	t.Run("normalising the status is refused", func(t *testing.T) {
		r := newLCRepo(t, map[string]string{lcTask: lcArt("TASK-001", "Task", "Pending"), arch: body("Active", "# v1\n")})
		r.onRun("normalise", func() { r.write(arch, body("Living Document", "# v1\n")) })
		e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}})
		e.requireRefused(LifecycleNonOwnedStatusChange)
	})
}

// TestStatusLifecycle_RefusalRouting is AC 11 on real git: a publish-step
// workflow routes the refusal back to execute.
func TestStatusLifecycle_RefusalRouting(t *testing.T) {
	r := p321Repo(t, false)
	e := newLCEnv(t, r, lcOpts{meta: map[string]string{"status": "Completed"}, wf: publishWorkflow()})
	e.merge()
	if e.run.Status != domain.RunStatusActive {
		t.Fatalf("run status = %s, want active (routed back)", e.run.Status)
	}
	var publish, review, execute *domain.StepExecution
	for _, s := range e.store.createdSteps {
		switch s.StepID {
		case "publish":
			publish = s
		case "review":
			review = s
		case "execute":
			execute = s
		}
	}
	if publish == nil || publish.Status != domain.StepStatusCompleted || publish.OutcomeID != "merge_failed" {
		t.Fatalf("publish = %+v, want completed/merge_failed", publish)
	}
	if execute == nil || execute.Status != domain.StepStatusWaiting {
		t.Fatalf("execute = %+v, want a new waiting execution", execute)
	}
	if review.ErrorDetail == nil || review.ErrorDetail.Classification != domain.FailureStatusLifecycle {
		t.Fatalf("last completed step error detail = %+v, want status_lifecycle_violation", review.ErrorDetail)
	}
	if o := e.outcome(); o == nil || o.FailureClass != domain.MergeFailurePrecondition {
		t.Fatalf("outcome = %+v, want class precondition", o)
	}
	if r.head("main") != e.main0 {
		t.Fatal("main moved")
	}
}

// TestStatusLifecycle_ReplayAfterTransientPush: a transient push failure
// leaves the judged merge on the local main; the retry replays it — the
// same cascade commit, no second merge — and the projection-based
// status-conflict gate, which has moved meanwhile, is not consulted again.
func TestStatusLifecycle_ReplayAfterTransientPush(t *testing.T) {
	const epic = "initiatives/init-001/epics/epic-001/epic.md"
	r := newLCRepo(t, map[string]string{epic: lcArt("EPIC-001", "Epic", "Pending")})
	origin := filepath.Join(t.TempDir(), "origin.git")
	r.git("init", "-q", "--bare", origin)
	r.git("remote", "add", "origin", origin)
	r.git("push", "-q", "origin", "main")
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	r.onRun("work", func() { r.write("notes.txt", "done\n") })

	e := newLCEnv(t, r, lcOpts{taskPath: epic, meta: map[string]string{"status": "Completed"}})
	v := &scValidator{res: domain.ValidationResult{Status: "passed"}}
	e.orch.WithValidator(v)
	e.git.failPushes = 1
	e.merge()
	if e.run.Status != domain.RunStatusCommitting {
		t.Fatalf("after the failed push: run %s, want committing", e.run.Status)
	}
	localMerge := r.head("main")
	if localMerge == e.main0 || len(v.calls) != 1 {
		t.Fatalf("first attempt: main %s (was %s), SC calls %v", localMerge, e.main0, v.calls)
	}

	// The projection now reports an open child: a fresh judgement would
	// refuse. The replay must not.
	v.res = domain.ValidationResult{Status: "failed", Errors: []domain.ValidationError{{RuleID: "SC-002", Severity: "error"}}}
	e.merge()
	if e.run.Status != domain.RunStatusCompleted {
		t.Fatalf("replay: run %s (outcome %+v), want completed", e.run.Status, e.outcome())
	}
	if len(v.calls) != 1 {
		t.Fatalf("the replay consulted the SC gate again: %v", v.calls)
	}
	if got := r.head("main"); got != localMerge {
		t.Fatalf("the replay merged again: %s -> %s", localMerge, got)
	}
	if got := strings.TrimSpace(gitIn(t, origin, "rev-parse", "main")); got != localMerge {
		t.Fatalf("origin main = %s, want the judged merge %s", got, localMerge)
	}
	if got := r.statusAt("main", epic); got != "Completed" {
		t.Fatalf("main epic = %s", got)
	}

	// Control: a non-replay attempt does consult the gate and refuses.
	r2 := newLCRepo(t, map[string]string{epic: lcArt("EPIC-001", "Epic", "Pending")})
	r2.onRun("work", func() { r2.write("notes.txt", "done\n") })
	e2 := newLCEnv(t, r2, lcOpts{taskPath: epic, meta: map[string]string{"status": "Completed"}})
	e2.orch.WithValidator(&scValidator{res: v.res})
	e2.requireRefused(LifecycleStatusConflict)
}

// TestStatusLifecycle_ResumeAfterLanding (review round 1, P3): the primary
// merge landed and passed the status-conflict gate; the run is resumed in a
// shape the replay check does not recognise — the binding moved to a
// recovery-ledger commit L (a child of the bound B, so MergeBase(main, L) is
// B, not L), or this run's merge lies beyond priorMergeWalkLimit. The plan
// then judges the TaskPath from the pre-cascade content again (Pending ->
// Completed), but main already holds Completed: the gate, whose projection
// now reports an open child, is not asked, and the resume completes.
func TestStatusLifecycle_ResumeAfterLanding(t *testing.T) {
	const epic = "initiatives/init-001/epics/epic-001/epic.md"
	sc002 := domain.ValidationResult{Status: "failed", Errors: []domain.ValidationError{{RuleID: "SC-002", Severity: "error"}}}
	land := func(t *testing.T) (*lcRepo, *lcEnv, *scValidator) {
		r := newLCRepo(t, map[string]string{epic: lcArt("EPIC-001", "Epic", "Pending")})
		r.onRun("work", func() { r.write("notes.txt", "done\n") })
		e := newLCEnv(t, r, lcOpts{taskPath: epic, meta: map[string]string{"status": "Completed"}})
		v := &scValidator{res: domain.ValidationResult{Status: "passed"}}
		e.orch.WithValidator(v)
		e.merge()
		if e.run.Status != domain.RunStatusCompleted || len(v.calls) != 1 || r.statusAt("main", epic) != "Completed" {
			t.Fatalf("first merge: run %s, SC calls %v, main epic %s", e.run.Status, v.calls, r.statusAt("main", epic))
		}
		v.res = sc002
		e.run.Status = domain.RunStatusCommitting
		return r, e, v
	}

	t.Run("binding moved to a recovery-ledger commit", func(t *testing.T) {
		r, e, v := land(t)
		ledger, err := r.cli.CommitFilesOnto(context.Background(), e.bound, nil, git.CommitOpts{Message: "merge recovery ledger", AllowEmpty: true})
		if err != nil {
			t.Fatal(err)
		}
		e.run.CommitMeta[CommitMetaBoundSHA] = ledger.SHA
		e.run.CommitMeta[CommitMetaBoundBy] = boundByRecovery(lcRunID, "merge_recovery_retry")
		if base, replay, err := e.orch.cascadeBase(context.Background(), e.run, "Completed", ledger.SHA); err != nil || replay || base != e.bound {
			t.Fatalf("cascadeBase = %s, replay %v, %v; want the bound commit, not a replay", base, replay, err)
		}
		e.merge()
		if e.run.Status != domain.RunStatusCompleted {
			t.Fatalf("resume: run %s (outcome %+v), want completed", e.run.Status, e.outcome())
		}
		if len(v.calls) != 1 {
			t.Fatalf("the resume consulted the SC gate again: %v", v.calls)
		}
		if got := r.statusAt("main", epic); got != "Completed" {
			t.Fatalf("main epic = %s", got)
		}
	})
	t.Run("this run's merge beyond the replay walk limit", func(t *testing.T) {
		r, e, v := land(t)
		for i := 0; i <= priorMergeWalkLimit; i++ {
			r.git("commit", "-q", "--allow-empty", "-m", "later work on main")
		}
		e.merge()
		if e.run.Status != domain.RunStatusCompleted {
			t.Fatalf("resume: run %s (outcome %+v), want completed", e.run.Status, e.outcome())
		}
		if len(v.calls) != 1 {
			t.Fatalf("the resume consulted the SC gate again: %v", v.calls)
		}
	})
	t.Run("control: main does not hold the status, the gate is asked", func(t *testing.T) {
		r := newLCRepo(t, map[string]string{epic: lcArt("EPIC-001", "Epic", "Pending")})
		r.onRun("work", func() { r.write("notes.txt", "done\n") })
		e := newLCEnv(t, r, lcOpts{taskPath: epic, meta: map[string]string{"status": "Completed"}})
		v := &scValidator{res: sc002}
		e.orch.WithValidator(v)
		e.requireRefused(LifecycleStatusConflict) // plans once, then merges: two asks
		if len(v.calls) != 2 {
			t.Fatalf("SC calls %v, want two", v.calls)
		}
	})
}
