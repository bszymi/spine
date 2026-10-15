package engine

// INIT-023 TASK-012 regression tests (audit F8, F9), on the real
// git.CLIClient built with git.WithHomeBranch("main") as `spine serve`
// builds a workspace's primary client. Inverted from the audit PoCs:
//
//   - poc/verify-r1-11/engine_convergence_main_test.go, poc/skeptic_conv
//     (F8, engine path: AC 1);
//   - poc/gap2-git-state-concurrenc spine_worktree_race_poc_test.go and
//     spine_poison_poc_test.go, poc/verify-r2-2 (F9: AC 6, 7, 8, 9);
//   - the design check's probes (_work/task012-check): the same run
//     entered twice (AC 6b), a cancelled request (AC 24b), a HEAD read
//     during a sequence (AC 25).
//
// Each failed on 61994fe; see the task's Implementation Notes for the
// mutation runs.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/divergence"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/workflow"
)

// ---- helpers ----

func swtGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test",
		"-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func swtTry(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func swtWrite(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func swtOnMain(repo, file string) bool {
	_, err := swtTry(repo, "cat-file", "-e", "main:"+file)
	return err == nil
}

// swtAssertHome: the shared tree rests on a clean main.
func swtAssertHome(t *testing.T, repo string) {
	t.Helper()
	head, _ := swtTry(repo, "symbolic-ref", "-q", "HEAD")
	dirty, _ := swtTry(repo, "status", "--porcelain", "--untracked-files=no")
	if head != "refs/heads/main" || dirty != "" {
		t.Fatalf("shared working tree not at rest: HEAD=%q tracked changes=%q", head, dirty)
	}
}

// swtNoStatusCommitOnMain: no cascade commit on main's first-parent line
// (a status commit belongs on the run branch and reaches main only through
// the governed merge).
func swtNoStatusCommitOnMain(t *testing.T, repo string) {
	t.Helper()
	log := swtGit(t, repo, "log", "--first-parent", "--format=%s", "main")
	if strings.Contains(log, "Update artifact status") || strings.Contains(log, "Cascade artifact status") {
		t.Fatalf("a status commit landed directly on main:\n%s", log)
	}
}

const swtTask = "---\nid: TASK-%s\ntype: Task\ntitle: T\nstatus: Pending\n---\n# T\n"

// swtRepo: main plus run branches spine/run/run-<id>, each adding
// tasks/task-<id>.md and code-<id>.txt; HEAD on main.
func swtRepo(t *testing.T, ids ...string) string {
	t.Helper()
	dir := t.TempDir()
	swtGit(t, dir, "init", "-q", "-b", "main")
	swtWrite(t, dir, "README.md", "hi\n")
	swtGit(t, dir, "add", "-A")
	swtGit(t, dir, "commit", "-q", "-m", "init")
	for _, id := range ids {
		swtGit(t, dir, "checkout", "-q", "-b", "spine/run/run-"+id, "main")
		swtWrite(t, dir, "tasks/task-"+id+".md", fmt.Sprintf(swtTask, id))
		swtWrite(t, dir, "code-"+id+".txt", id+"\n")
		swtGit(t, dir, "add", "-A")
		swtGit(t, dir, "commit", "-q", "-m", "work "+id)
	}
	swtGit(t, dir, "checkout", "-q", "main")
	return dir
}

func swtRun(id string) *domain.Run {
	runID := "run-" + id
	return &domain.Run{
		RunID: runID, Status: domain.RunStatusCommitting,
		BranchName: "spine/run/" + runID, TaskPath: "tasks/task-" + id + ".md",
		TraceID: "trace-" + runID + "-0123456789", CommitMeta: map[string]string{"status": "Completed"},
	}
}

// swtOrch: a committing run for id over g, with its own store.
func swtOrch(g GitOperator, id string) (*Orchestrator, *mockRunStore) {
	r := swtRun(id)
	st := &mockRunStore{runs: map[string]*domain.Run{r.RunID: r}}
	return &Orchestrator{store: st, git: g, events: &mockEventEmitter{}, wfLoader: &stubWorkflowLoader{},
		policy: branchprotect.NewPermissive()}, st
}

// swtHookGit is the shared CLIClient with a hook after each Checkout. It
// embeds the client, so it is a git.WorkingTreeGuard like production's.
type swtHookGit struct {
	*git.CLIClient
	afterCheckout func(ctx context.Context, branch string)
}

func (h *swtHookGit) Checkout(ctx context.Context, branch string) error {
	err := h.CLIClient.Checkout(ctx, branch)
	if h.afterCheckout != nil {
		h.afterCheckout(ctx, branch)
	}
	return err
}

// swtCopyStore returns copies from GetRun, as Postgres does; the mock's
// shared pointer would hide the double-entry race.
type swtCopyStore struct{ *mockRunStore }

func (s swtCopyStore) GetRun(ctx context.Context, id string) (*domain.Run, error) {
	r, err := s.mockRunStore.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	cp := *r
	return &cp, nil
}

type swtDivStore struct{ *mockRunStore }

func (s swtDivStore) CreateDivergenceContext(_ context.Context, d *domain.DivergenceContext) error {
	if s.divergenceContexts == nil {
		s.divergenceContexts = map[string]*domain.DivergenceContext{}
	}
	s.divergenceContexts[d.DivergenceID] = d
	return nil
}

func (s swtDivStore) UpdateDivergenceContext(_ context.Context, d *domain.DivergenceContext) error {
	s.divergenceContexts[d.DivergenceID] = d
	return nil
}

func (s swtDivStore) CreateBranch(_ context.Context, b *domain.Branch) error {
	s.branches = append(s.branches, b)
	return nil
}

type swtRouter struct {
	emit func(context.Context, domain.Event)
}

func (r swtRouter) Emit(ctx context.Context, e domain.Event) error {
	if r.emit != nil {
		r.emit(ctx, e)
	}
	return nil
}
func (swtRouter) Subscribe(context.Context, domain.EventType, event.EventHandler) error { return nil }

func swtCaptureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// ---- F8 ----

// swtDivergenceWorkflow: start diverges into one branch; convergence
// resumes at the review step.
func swtDivergenceWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "wf-test", EntryStep: "start",
		Steps: []domain.StepDefinition{
			{ID: "start", Name: "Start", Diverge: "div-1", Converge: "conv-1",
				Outcomes: []domain.OutcomeDefinition{{ID: "done", NextStep: "review"}}},
			{ID: "branch-a", Name: "Branch A", Outcomes: []domain.OutcomeDefinition{{ID: "done", NextStep: "end"}}},
			{ID: "review", Name: "Human Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{{ID: "accepted", NextStep: "end"}, {ID: "rejected", NextStep: "end"}}},
		},
		DivergencePoints: []domain.DivergenceDefinition{{ID: "div-1", Mode: domain.DivergenceModeStructured,
			Branches: []domain.BranchDefinition{{ID: "a", Name: "A", StartStep: "branch-a"}}}},
		ConvergencePoints: []domain.ConvergenceDefinition{{ID: "conv-1", Strategy: domain.ConvergenceSelectOne,
			EntryPolicy: domain.EntryPolicyAllTerminal, EvaluationStep: "review"}},
	}
}

// swtDivergenceRun drives the F8 PoC's flow on a shared client: the start
// step diverges, a worker pushes UNREVIEWED.md to the divergence ref, and
// the branch step is submitted, which converges.
func swtDivergenceRun(t *testing.T, routerFor func(repo string) event.EventRouter) (repo, runBranch, mainBefore string, st *mockRunStore) {
	t.Helper()
	ctx := context.Background()
	repo = t.TempDir()
	swtGit(t, repo, "init", "-q", "-b", "main")
	swtWrite(t, repo, "tasks/task.md", "---\ntype: Task\nstatus: In Progress\n---\n# T\n")
	swtGit(t, repo, "add", "-A")
	swtGit(t, repo, "commit", "-q", "-m", "seed")
	mainBefore = swtGit(t, repo, "rev-parse", "main")
	client := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	runBranch = "spine/run/run-1"
	if err := client.CreateBranch(ctx, runBranch, "HEAD"); err != nil {
		t.Fatal(err)
	}
	st, runID := testRunWithStep()
	st.runs[runID].BranchName = runBranch
	divSvc := divergence.NewService(swtDivStore{st}, client, routerFor(repo))
	divSvc.WithBranchProtectPolicy(branchprotect.New(branchprotect.StaticRules(branchprotect.BootstrapDefaults())))
	orch := stepTestOrchestrator(st, &mockEventEmitter{}, &mockWorkflowLoader{wfDef: swtDivergenceWorkflow()}, nil, nil)
	// Production wiring: the orchestrator and the divergence service share
	// the workspace's one CLIClient.
	orch.git = client
	orch.WithDivergence(divSvc)
	orch.WithConvergence(divSvc)

	st.createdSteps[0].Status = domain.StepStatusInProgress
	st.createdSteps[0].ActorID = "planner"
	if err := orch.SubmitStepResult(ctx, st.createdSteps[0].ExecutionID, StepResult{OutcomeID: "done"}); err != nil {
		t.Fatalf("submit start: %v", err)
	}
	divBranch := "spine/run-1/run-1-div-div-1/a"
	clone := t.TempDir()
	swtGit(t, clone, "clone", "-q", repo, ".")
	swtGit(t, clone, "checkout", "-q", divBranch)
	swtWrite(t, clone, "UNREVIEWED.md", "injected by a branch worker\n")
	swtGit(t, clone, "add", "-A")
	swtGit(t, clone, "commit", "-q", "-m", "A1 branch work")
	swtGit(t, clone, "push", "-q", "origin", divBranch)

	var branchExec *domain.StepExecution
	for _, s := range st.createdSteps {
		if s.BranchID != "" && s.StepID == "branch-a" {
			branchExec = s
		}
	}
	if branchExec == nil {
		t.Fatal("branch step not created")
	}
	branchExec.Status = domain.StepStatusInProgress
	branchExec.ActorID = "a1"
	if err := orch.SubmitStepResult(ctx, branchExec.ExecutionID, StepResult{OutcomeID: "done"}); err != nil {
		t.Fatalf("submit branch: %v", err)
	}
	return repo, runBranch, mainBefore, st
}

// AC 1.
func TestF8_ConvergenceMergesIntoRunBranchNotMain(t *testing.T) {
	repo, runBranch, mainBefore, st := swtDivergenceRun(t, func(string) event.EventRouter { return swtRouter{} })
	if got := swtGit(t, repo, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("F8: main advanced by convergence (%s -> %s)", mainBefore[:8], got[:8])
	}
	if swtOnMain(repo, "UNREVIEWED.md") {
		t.Fatal("F8: divergence content reached main before review")
	}
	if _, err := swtTry(repo, "cat-file", "-e", runBranch+":UNREVIEWED.md"); err != nil {
		t.Fatal("convergence did not merge the divergence branch into the run branch")
	}
	if got := st.divergenceContexts["run-1-div-div-1"].Status; got != domain.DivergenceStatusResolved {
		t.Fatalf("divergence status %s, want resolved", got)
	}
	swtAssertHome(t, repo)
}

// ---- F9 ----

// AC 6.
func TestF9_ConcurrentGovernedMergesSerialize(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		repo := swtRepo(t, "a", "b")
		shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		oa, sa := swtOrch(shared, "a")
		ob, sb := swtOrch(shared, "b")
		var wg sync.WaitGroup
		wg.Add(2)
		var errA, errB error
		go func() { defer wg.Done(); errA = oa.MergeRunBranch(ctx, "run-a") }()
		go func() { defer wg.Done(); errB = ob.MergeRunBranch(ctx, "run-b") }()
		wg.Wait()
		if errA != nil || errB != nil {
			t.Fatalf("iter %d: MergeRunBranch errors a=%v b=%v", i, errA, errB)
		}
		for id, st := range map[string]*mockRunStore{"a": sa, "b": sb} {
			if s := st.runs["run-"+id].Status; s != domain.RunStatusCompleted {
				t.Fatalf("iter %d: run-%s ended %s", i, id, s)
			}
			if !swtOnMain(repo, "code-"+id+".txt") {
				t.Fatalf("iter %d: run-%s content missing on main", i, id)
			}
		}
		swtNoStatusCommitOnMain(t, repo)
		swtAssertHome(t, repo)
	}
}

// swtPauseStore is a copy store whose branch-protection pause the test
// drives: entry B passes preflightCheck's committing check, then waits in
// the policy while entry A runs to completion.
type swtPausePolicy struct {
	paused chan struct{}
	resume chan struct{}
	once   sync.Once
}

func (p *swtPausePolicy) Evaluate(ctx context.Context, req branchprotect.Request) (branchprotect.Decision, []branchprotect.Reason, error) {
	p.once.Do(func() {
		close(p.paused)
		<-p.resume
	})
	return branchprotect.NewPermissive().Evaluate(ctx, req)
}

// AC 6b.
func TestF9_SameRunDoubleEntryNeverFailsACompletedRun(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	repo := swtRepo(t, "a")
	client := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	ms := &mockRunStore{runs: map[string]*domain.Run{"run-a": swtRun("a")}}
	st := swtCopyStore{ms}
	pp := &swtPausePolicy{paused: make(chan struct{}), resume: make(chan struct{})}
	entryA := &Orchestrator{store: st, git: client, events: &mockEventEmitter{}, wfLoader: &stubWorkflowLoader{}, policy: branchprotect.NewPermissive()}
	eventsB := &mockEventEmitter{}
	entryB := &Orchestrator{store: st, git: client, events: eventsB, wfLoader: &stubWorkflowLoader{}, policy: pp}

	errB := make(chan error, 1)
	go func() { errB <- entryB.MergeRunBranch(ctx, "run-a") }()
	<-pp.paused // B has passed the committing check
	if err := entryA.MergeRunBranch(ctx, "run-a"); err != nil {
		t.Fatalf("entry A: %v", err)
	}
	if s := ms.runs["run-a"].Status; s != domain.RunStatusCompleted {
		t.Fatalf("entry A did not complete the run: %s", s)
	}
	if _, err := swtTry(repo, "rev-parse", "--verify", "refs/heads/spine/run/run-a"); err == nil {
		t.Fatal("entry A did not delete the run branch (the race needs it gone)")
	}
	close(pp.resume)
	if err := <-errB; err != nil {
		t.Fatalf("entry B returned %v, want nil (it stands down)", err)
	}
	if s := ms.runs["run-a"].Status; s != domain.RunStatusCompleted {
		t.Fatalf("the completed run flipped to %s through the second entry", s)
	}
	outcomes, _ := ms.ListRepositoryMergeOutcomes(ctx, "run-a")
	if len(outcomes) != 1 || outcomes[0].Status != domain.RepositoryMergeStatusMerged {
		t.Fatalf("primary outcome = %+v, want one merged row", outcomes)
	}
	for _, e := range eventsB.events {
		if e.Type == domain.EventRunFailed {
			t.Fatal("entry B emitted run_failed")
		}
	}
	swtAssertHome(t, repo)
}

// AC 7.
func TestF9_StatusCascadeIsAtomicAgainstConcurrentMerge(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	repo := swtRepo(t, "a", "b")
	shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hookA := &swtHookGit{CLIClient: shared, afterCheckout: func(_ context.Context, b string) {
		if b == "spine/run/run-a" {
			once.Do(func() { close(paused); <-resume })
		}
	}}
	orchA, stA := swtOrch(hookA, "a")
	orchB, stB := swtOrch(shared, "b")
	errA := make(chan error, 1)
	go func() { errA <- orchA.MergeRunBranch(ctx, "run-a") }()
	<-paused
	errB := make(chan error, 1)
	go func() { errB <- orchB.MergeRunBranch(ctx, "run-b") }()
	select {
	case err := <-errB:
		t.Fatalf("F9: run B's merge ran while run A held the working tree (err=%v)", err)
	case <-time.After(500 * time.Millisecond):
	}
	close(resume)
	if err := <-errA; err != nil {
		t.Fatalf("A: %v", err)
	}
	if err := <-errB; err != nil {
		t.Fatalf("B: %v", err)
	}
	if stA.runs["run-a"].Status != domain.RunStatusCompleted || stB.runs["run-b"].Status != domain.RunStatusCompleted {
		t.Fatalf("A=%s B=%s", stA.runs["run-a"].Status, stB.runs["run-b"].Status)
	}
	swtNoStatusCommitOnMain(t, repo)
	swtAssertHome(t, repo)
}

// AC 8.
func TestF9_RunBranchNeverCutFromForeignRunBranch(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	repo := swtRepo(t, "a")
	shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	created := make(chan error, 1)
	var once sync.Once
	var ranEarly error
	hookA := &swtHookGit{CLIClient: shared, afterCheckout: func(_ context.Context, b string) {
		if b == "spine/run/run-a" {
			once.Do(func() {
				// Another request starts run C on the same workspace.
				go func() { created <- shared.CreateBranch(context.Background(), "spine/run/run-c", "HEAD") }()
				select {
				case err := <-created:
					ranEarly = fmt.Errorf("CreateBranch ran while run A held the tree (err=%v)", err)
					created <- err
				case <-time.After(500 * time.Millisecond):
				}
			})
		}
	}}
	orchA, _ := swtOrch(hookA, "a")
	runA := swtGit(t, repo, "rev-parse", "spine/run/run-a")
	if err := orchA.MergeRunBranch(ctx, "run-a"); err != nil {
		t.Fatalf("A: %v", err)
	}
	if ranEarly != nil {
		t.Fatalf("F9: %v", ranEarly)
	}
	if err := <-created; err != nil {
		t.Fatalf("create run-c: %v", err)
	}
	runC := swtGit(t, repo, "rev-parse", "spine/run/run-c")
	firstParent := swtGit(t, repo, "rev-list", "--first-parent", "main")
	if runC == runA || !strings.Contains(firstParent, runC) {
		t.Fatalf("F9: run C cut from %s (run-a=%s), not from main's first-parent history", runC[:8], runA[:8])
	}
}

// AC 9.
func TestF9_PoisonedTreeSelfHealsAtNextMerge(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	logs := swtCaptureLogs(t)
	ctx := context.Background()
	repo := swtRepo(t, "a", "c", "d", "e", "f")
	// Poison exactly as observed in the audit: HEAD on run-a, staged edit.
	swtGit(t, repo, "checkout", "-q", "spine/run/run-a")
	swtWrite(t, repo, "tasks/task-a.md", "---\nid: TASK-a\ntype: Task\ntitle: T\nstatus: Completed\n---\n# T\n")
	swtGit(t, repo, "add", "tasks/task-a.md")
	runATip := swtGit(t, repo, "rev-parse", "spine/run/run-a")
	shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	for _, id := range []string{"c", "d", "e", "f"} {
		o, st := swtOrch(shared, id)
		if err := o.MergeRunBranch(ctx, "run-"+id); err != nil {
			t.Fatalf("run-%s: %v", id, err)
		}
		if s := st.runs["run-"+id].Status; s != domain.RunStatusCompleted {
			t.Fatalf("run-%s ended %s after the poisoning", id, s)
		}
		if !swtOnMain(repo, "code-"+id+".txt") {
			t.Fatalf("run-%s content missing on main", id)
		}
		swtAssertHome(t, repo)
	}
	if swtOnMain(repo, "tasks/task-a.md") || swtOnMain(repo, "code-a.txt") {
		t.Fatal("unapproved run-a content reached main")
	}
	if got := swtGit(t, repo, "rev-parse", "spine/run/run-a"); got != runATip {
		t.Fatal("the restore moved run-a's branch")
	}
	quarantines := 0
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "shared working tree restored") &&
			strings.Contains(line, "quarantine_sha=") && !strings.Contains(line, `quarantine_sha=""`) {
			quarantines++
		}
	}
	if quarantines != 1 {
		t.Fatalf("Warn records with a quarantine sha = %d, want exactly 1\n%s", quarantines, logs.String())
	}
}

// swtCascadeSwap is the shared client that, on the first checkout of the
// run branch (inside the cascade, after its reads), commits onto the
// branch tip a symlink in place of the docs/ directory — a push landing
// between the cascade's read and its write — and records which cascade
// writes succeeded before the refusal.
type swtCascadeSwap struct {
	*git.CLIClient
	t       *testing.T
	repo    string
	branch  string
	swapped bool
	mu      sync.Mutex
	writes  []string // "ok:<path>" / "refused:<path>"
}

func (s *swtCascadeSwap) Checkout(ctx context.Context, branch string) error {
	if branch == s.branch && !s.swapped {
		s.swapped = true
		swtCommitDirSymlink(s.t, s.repo, s.branch, "docs", "docs/b.md", "../outside")
	}
	return s.CLIClient.Checkout(ctx, branch)
}

func (s *swtCascadeSwap) WriteAndStageFile(ctx context.Context, path, content string) error {
	err := s.CLIClient.WriteAndStageFile(ctx, path, content)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.writes = append(s.writes, "refused:"+path)
	} else {
		s.writes = append(s.writes, "ok:"+path)
	}
	return err
}

// stagedBeforeRefusal reports whether a write succeeded before the
// refused one (the partially staged cascade this test is about).
func (s *swtCascadeSwap) stagedBeforeRefusal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.writes {
		if strings.HasPrefix(w, "refused:") {
			return false
		}
		if strings.HasPrefix(w, "ok:") {
			return true
		}
	}
	return false
}

// swtCommitDirSymlink replaces file (the only entry under dir) with a
// symlink dir -> target on branch, with plumbing only.
func swtCommitDirSymlink(t *testing.T, repo, branch, dir, file, target string) {
	t.Helper()
	idx := filepath.Join(t.TempDir(), "index")
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+idx,
			"GIT_AUTHOR_NAME=attacker", "GIT_AUTHOR_EMAIL=a@x",
			"GIT_COMMITTER_NAME=attacker", "GIT_COMMITTER_EMAIL=a@x")
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	blob := run(target, "hash-object", "-w", "--stdin")
	run("", "read-tree", branch)
	run("", "update-index", "--force-remove", file)
	run("", "update-index", "--add", "--cacheinfo", "120000,"+blob+","+dir)
	tree := run("", "write-tree")
	commit := run("", "commit-tree", tree, "-p", branch, "-m", "swap docs/ for a symlink")
	run("", "update-ref", "refs/heads/"+branch, commit)
}

// AC 9b (TASK-004 handoff): a multi-path cascade refused part-way leaves
// staged edits behind; the cascade block's exit restore quarantines them,
// and neither this merge nor the next one is affected.
func TestF9_PartiallyStagedCascadeIsQuarantined(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	for attempt := 0; attempt < 8; attempt++ {
		repo := swtRepo(t, "next")
		const branch = "spine/run/run-p"
		swtGit(t, repo, "checkout", "-q", "-b", branch, "main")
		for i := 0; i < 9; i++ {
			swtWrite(t, repo, fmt.Sprintf("tasks/task-p%d.md", i), fmt.Sprintf(swtTask, fmt.Sprintf("p%d", i)))
		}
		swtWrite(t, repo, "docs/b.md", fmt.Sprintf(swtTask, "pb"))
		swtGit(t, repo, "add", "-A")
		swtGit(t, repo, "commit", "-q", "-m", "work p")
		swtGit(t, repo, "checkout", "-q", "main")
		shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		swap := &swtCascadeSwap{CLIClient: shared, t: t, repo: repo, branch: branch}
		run := &domain.Run{RunID: "run-p", Status: domain.RunStatusCommitting, BranchName: branch,
			TaskPath: "tasks/task-p0.md", TraceID: "trace-run-p-0123456789", CommitMeta: map[string]string{"status": "Completed"}}
		st := &mockRunStore{runs: map[string]*domain.Run{run.RunID: run}}
		o := &Orchestrator{store: st, git: swap, events: &mockEventEmitter{}, wfLoader: &stubWorkflowLoader{}, policy: branchprotect.NewPermissive()}
		if err := o.MergeRunBranch(ctx, run.RunID); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		if !swap.swapped {
			t.Fatal("the cascade never checked the run branch out")
		}
		if s := st.runs[run.RunID].Status; s != domain.RunStatusCompleted {
			t.Fatalf("run ended %s, want completed (a failed cascade must not block the merge)", s)
		}
		swtNoStatusCommitOnMain(t, repo)
		swtAssertHome(t, repo)
		on, ns := swtOrch(shared, "next")
		if err := on.MergeRunBranch(ctx, "run-next"); err != nil || ns.runs["run-next"].Status != domain.RunStatusCompleted {
			t.Fatalf("the next governed merge: err=%v status=%s", err, ns.runs["run-next"].Status)
		}
		swtAssertHome(t, repo)
		if swap.stagedBeforeRefusal() {
			return // the partially staged case was exercised
		}
	}
	t.Fatal("the refused cascade path came first in every attempt; the partially staged case never ran")
}

// ---- events and locks ----

// swtProbe records an Emit made while the shared tree is held: it tries to
// take the tree through a second client and gives up after a second.
type swtProbe struct {
	repo       string
	mu         sync.Mutex
	violations []string
	emitted    int
}

func (p *swtProbe) check(e domain.Event) {
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done <- git.NewCLIClient(p.repo).WithWorkingTree(ctx, func(context.Context) error { return nil })
	}()
	err := <-done
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emitted++
	if err != nil {
		p.violations = append(p.violations, fmt.Sprintf("%s emitted under the tree lock (%v)", e.Type, err))
	}
}

func (p *swtProbe) Emit(_ context.Context, e domain.Event) error { p.check(e); return nil }

func (p *swtProbe) assertClean(t *testing.T, what string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.violations) > 0 {
		t.Fatalf("%s: %v", what, p.violations)
	}
	if p.emitted == 0 {
		t.Fatalf("%s: nothing was emitted; the probe checked nothing", what)
	}
}

// swtTransientPush is the shared client whose Push fails transiently.
type swtTransientPush struct{ *git.CLIClient }

func (swtTransientPush) Push(context.Context, string, string) error {
	return &git.GitError{Kind: git.ErrKindTransient, Op: "push", Message: "network error"}
}

// AC 12.
func TestF9_NoEventsOrRoutingUnderTheTreeLock(t *testing.T) {
	ctx := context.Background()
	t.Run("merge success", func(t *testing.T) {
		t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
		repo := swtRepo(t, "a")
		o, _ := swtOrch(git.NewCLIClient(repo, git.WithHomeBranch("main")), "a")
		probe := &swtProbe{repo: repo}
		o.events = probe
		if err := o.MergeRunBranch(ctx, "run-a"); err != nil {
			t.Fatal(err)
		}
		probe.assertClean(t, "merge success")
	})
	t.Run("merge transient", func(t *testing.T) {
		t.Setenv("SPINE_GIT_AUTO_PUSH", "true")
		repo := swtRepo(t, "a")
		o, st := swtOrch(swtTransientPush{git.NewCLIClient(repo, git.WithHomeBranch("main"))}, "a")
		probe := &swtProbe{repo: repo}
		o.events = probe
		if err := o.MergeRunBranch(ctx, "run-a"); err != nil {
			t.Fatal(err)
		}
		if s := st.runs["run-a"].Status; s != domain.RunStatusCommitting {
			t.Fatalf("run %s, want committing", s)
		}
		probe.mu.Lock()
		defer probe.mu.Unlock()
		if len(probe.violations) > 0 {
			t.Fatalf("transient path: %v", probe.violations)
		}
	})
	t.Run("merge permanent (conflict)", func(t *testing.T) {
		t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
		repo := swtRepo(t, "a")
		swtWrite(t, repo, "code-a.txt", "main's version\n")
		swtGit(t, repo, "add", "-A")
		swtGit(t, repo, "commit", "-q", "-m", "conflicting main")
		o, st := swtOrch(git.NewCLIClient(repo, git.WithHomeBranch("main")), "a")
		st.createdSteps = []*domain.StepExecution{{ExecutionID: "run-a-start-1", RunID: "run-a", StepID: "start", Status: domain.StepStatusCompleted}}
		probe := &swtProbe{repo: repo}
		o.events = probe
		if err := o.MergeRunBranch(ctx, "run-a"); err != nil {
			t.Fatal(err)
		}
		if s := st.runs["run-a"].Status; s != domain.RunStatusFailed {
			t.Fatalf("run %s, want failed", s)
		}
		probe.assertClean(t, "merge permanent")
		swtAssertHome(t, repo)
	})
	t.Run("run start", func(t *testing.T) {
		t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
		repo := swtRepo(t)
		probe := &swtProbe{repo: repo}
		orch := testOrchestrator(&mockArtifactReader{artifact: &domain.Artifact{Type: "task", Path: "tasks/t.md", ID: "TASK-1"}},
			&mockWorkflowResolver{result: &workflow.BindingResult{
				Workflow: &domain.WorkflowDefinition{ID: "wf", Path: "workflows/wf.yaml", Version: "1", EntryStep: "start",
					Steps: []domain.StepDefinition{{ID: "start", Name: "Start"}}},
				CommitSHA: "abc123", VersionLabel: "1"}},
			&mockRunStore{}, nil)
		orch.events = probe
		orch.git = git.NewCLIClient(repo, git.WithHomeBranch("main"))
		if _, err := orch.StartRun(ctx, "tasks/t.md"); err != nil {
			t.Fatal(err)
		}
		probe.assertClean(t, "run start")
	})
	t.Run("convergence", func(t *testing.T) {
		var probe *swtProbe
		swtDivergenceRun(t, func(repo string) event.EventRouter {
			probe = &swtProbe{repo: repo}
			return swtRouter{emit: func(_ context.Context, e domain.Event) { probe.check(e) }}
		})
		probe.assertClean(t, "convergence")
	})
	t.Run("in-place artifact write", func(t *testing.T) {
		t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
		repo := swtRepo(t)
		probe := &swtProbe{repo: repo}
		svc := artifact.NewService(git.NewCLIClient(repo, git.WithHomeBranch("main")),
			swtRouter{emit: func(_ context.Context, e domain.Event) { probe.check(e) }}, repo)
		svc.WithPolicy(branchprotect.NewPermissive())
		const doc = "---\ntype: Governance\ntitle: Probe\nstatus: Living Document\nversion: \"0.1\"\n---\n# Probe\n"
		if _, err := svc.Create(ctx, "governance/probe.md", doc); err != nil {
			t.Fatalf("Create: %v", err)
		}
		probe.assertClean(t, "in-place artifact write")
		swtAssertHome(t, repo)
	})
}

// AC 13.
func TestF9_LockWaitCancellationIsNotAMergeFailure(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	repo := swtRepo(t, "a")
	shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	o, st := swtOrch(shared, "a")
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = shared.WithWorkingTree(context.Background(), func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	err := o.MergeRunBranch(ctx, "run-a")
	cancel()
	close(release)
	<-done
	var we *git.TreeWaitError
	if !errors.As(err, &we) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("MergeRunBranch while the tree is held = %v, want a *git.TreeWaitError wrapping the ctx error", err)
	}
	if outcomes, _ := st.ListRepositoryMergeOutcomes(context.Background(), "run-a"); len(outcomes) != 0 {
		t.Fatalf("a lock wait recorded outcomes: %+v", outcomes)
	}
	if s := st.runs["run-a"].Status; s != domain.RunStatusCommitting {
		t.Fatalf("run %s, want committing", s)
	}
}

// AC 14: the entry restore fails (an untracked file blocks `switch main`);
// the attempt is recorded and routed by its class.
func TestF9_EntryRestoreFailureIsARecordedAttempt(t *testing.T) {
	ctx := context.Background()
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	setup := func(t *testing.T) (string, *git.CLIClient) {
		repo := swtRepo(t, "a")
		swtWrite(t, repo, "notes.md", "tracked on main\n")
		swtGit(t, repo, "add", "notes.md")
		swtGit(t, repo, "commit", "-q", "-m", "notes")
		shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		swtGit(t, repo, "checkout", "-q", "spine/run/run-a") // has no notes.md
		swtWrite(t, repo, "notes.md", "an untracked file in the way\n")
		return repo, shared
	}
	t.Run("legacy workflow fails the run", func(t *testing.T) {
		_, shared := setup(t)
		o, st := swtOrch(shared, "a")
		st.runs["run-a"].CommitMeta = nil
		st.createdSteps = []*domain.StepExecution{{ExecutionID: "run-a-start-1", RunID: "run-a", StepID: "start", Status: domain.StepStatusCompleted}}
		if err := o.MergeRunBranch(ctx, "run-a"); err != nil {
			t.Fatal(err)
		}
		outcomes, _ := st.ListRepositoryMergeOutcomes(ctx, "run-a")
		if len(outcomes) != 1 || outcomes[0].Status != domain.RepositoryMergeStatusFailed || outcomes[0].Attempts != 1 ||
			outcomes[0].FailureClass.IsTransient() {
			t.Fatalf("outcomes = %+v, want one failed, permanent attempt", outcomes)
		}
		if s := st.runs["run-a"].Status; s != domain.RunStatusFailed {
			t.Fatalf("run %s, want failed", s)
		}
	})
	t.Run("publish workflow routes merge_failed", func(t *testing.T) {
		_, shared := setup(t)
		o, st := swtOrch(shared, "a")
		st.runs["run-a"].CommitMeta = nil
		st.runs["run-a"].CurrentStepID = "publish"
		st.createdSteps = []*domain.StepExecution{
			{ExecutionID: "run-a-execute-1", RunID: "run-a", StepID: "execute", Status: domain.StepStatusCompleted, Attempt: 1},
			{ExecutionID: "run-a-publish-1", RunID: "run-a", StepID: "publish", Status: domain.StepStatusInProgress, Attempt: 1},
		}
		o.wfLoader = &mockWorkflowLoader{wfDef: swtPublishWorkflow()}
		o.actors = &mockActorAssigner{}
		o.workflows = &mockWorkflowResolver{}
		o.artifacts = &mockArtifactReader{}
		if err := o.MergeRunBranch(ctx, "run-a"); err != nil {
			t.Fatal(err)
		}
		outcomes, _ := st.ListRepositoryMergeOutcomes(ctx, "run-a")
		if len(outcomes) != 1 || outcomes[0].Attempts != 1 || outcomes[0].Status != domain.RepositoryMergeStatusFailed {
			t.Fatalf("outcomes = %+v", outcomes)
		}
		if r := st.runs["run-a"]; r.Status != domain.RunStatusActive || r.CurrentStepID != "execute" {
			t.Fatalf("run %s at %s, want active at execute (merge_failed)", r.Status, r.CurrentStepID)
		}
	})
}

// swtPublishWorkflow: execute → publish (internal merge), merge_failed →
// execute, as task-default.
func swtPublishWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "wf-publish", EntryStep: "execute",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "done", NextStep: "publish"}}},
			{ID: "publish", Name: "Publish", Type: domain.StepTypeInternal,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly, Handler: "merge"},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "published", Name: "Published", NextStep: "end"},
					{ID: "merge_failed", Name: "Merge failed", NextStep: "execute"},
				}},
		},
	}
}

// AC 24b.
func TestF9_CancelledRequestNeverPoisonsTree(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	repo := swtRepo(t, "b")
	swtGit(t, repo, "config", "filter.slow.smudge", "sleep 2; cat")
	swtGit(t, repo, "config", "filter.slow.clean", "cat")
	swtWrite(t, repo, ".gitattributes", "*.slow filter=slow\n")
	swtGit(t, repo, "add", ".gitattributes")
	swtGit(t, repo, "commit", "-q", "-m", "attributes")
	swtGit(t, repo, "checkout", "-q", "-b", "spine/run/run-a", "main")
	swtWrite(t, repo, "tasks/task-a.md", fmt.Sprintf(swtTask, "a"))
	// git checks entries out in index order: the a/ files are written
	// before b.slow's filter blocks, so a checkout stopped there leaves
	// them behind as untracked files no restore removes (the design
	// check's residue case) — unless the sequence runs to completion.
	for i := 0; i < 40; i++ {
		swtWrite(t, repo, fmt.Sprintf("a/f%02d.txt", i), fmt.Sprintf("%d\n", i))
	}
	swtWrite(t, repo, "b.slow", "slow content\n")
	swtGit(t, repo, "add", "-A")
	swtGit(t, repo, "commit", "-q", "-m", "work a")
	swtGit(t, repo, "checkout", "-q", "main")
	shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))

	oa, sa := swtOrch(shared, "a")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	_ = oa.MergeRunBranch(ctx, "run-a")
	cancel()

	if _, err := os.Stat(filepath.Join(repo, ".git", "index.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".git/index.lock after a cancelled governed merge: %v", err)
	}
	head, _ := swtTry(repo, "symbolic-ref", "-q", "HEAD")
	if all := swtGit(t, repo, "status", "--porcelain"); head != "refs/heads/main" || all != "" {
		t.Fatalf("tree after a cancelled merge: HEAD=%s status (incl. untracked)=%q", head, all)
	}
	if s := sa.runs["run-a"].Status; s != domain.RunStatusCompleted && s != domain.RunStatusCommitting {
		t.Fatalf("run-a ended %s after a client disconnect, want completed or committing", s)
	}
	outcomes, _ := sa.ListRepositoryMergeOutcomes(context.Background(), "run-a")
	for _, o := range outcomes {
		if o.FailureClass == domain.MergeFailureUnknown && o.Status == domain.RepositoryMergeStatusFailed {
			t.Fatalf("a client disconnect was recorded as an unknown (permanent) failure: %+v", o)
		}
	}
	if sa.runs["run-a"].Status == domain.RunStatusCommitting {
		if err := oa.MergeRunBranch(context.Background(), "run-a"); err != nil || sa.runs["run-a"].Status != domain.RunStatusCompleted {
			t.Fatalf("the scheduler's retry of run-a: err=%v status=%s", err, sa.runs["run-a"].Status)
		}
	}
	ob, sb := swtOrch(shared, "b")
	if err := ob.MergeRunBranch(context.Background(), "run-b"); err != nil || sb.runs["run-b"].Status != domain.RunStatusCompleted {
		t.Fatalf("run-b after the disconnect: err=%v status=%s", err, sb.runs["run-b"].Status)
	}
	swtAssertHome(t, repo)
}

// swtTypeResolver records the artifact type StartRun binds a workflow by,
// then stops the run start.
type swtTypeResolver struct {
	mu    sync.Mutex
	types []string
	seen  chan struct{}
}

var errSwtStop = errors.New("stop after binding")

func (r *swtTypeResolver) ResolveWorkflow(_ context.Context, artifactType, _ string) (*workflow.BindingResult, error) {
	r.mu.Lock()
	r.types = append(r.types, artifactType)
	r.mu.Unlock()
	close(r.seen)
	return nil, errSwtStop
}

func (r *swtTypeResolver) ResolveWorkflowForMode(ctx context.Context, artifactType, workType, _ string) (*workflow.BindingResult, error) {
	return r.ResolveWorkflow(ctx, artifactType, workType)
}

// swtGitArtifacts reads tasks through the shared client at the given ref,
// as artifact.Service.Read does.
type swtGitArtifacts struct{ g *git.CLIClient }

func (a swtGitArtifacts) Read(ctx context.Context, path, ref string) (*domain.Artifact, error) {
	b, err := a.g.ReadFile(ctx, ref, path)
	if err != nil {
		return nil, err
	}
	typ := ""
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "type: "); ok {
			typ = v
		}
	}
	return &domain.Artifact{Path: path, Type: domain.ArtifactType(typ)}, nil
}

// AC 25 (engine half).
func TestF9_HeadReadDuringSequenceSeesMain(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	repo := swtRepo(t, "a")
	swtWrite(t, repo, "tasks/shared.md", "---\nid: TASK-s\ntype: Task\ntitle: S\nstatus: Pending\n---\n# S\n")
	swtGit(t, repo, "add", "-A")
	swtGit(t, repo, "commit", "-q", "-m", "shared")
	// run-a's unreviewed branch retypes the shared task.
	swtGit(t, repo, "checkout", "-q", "spine/run/run-a")
	swtWrite(t, repo, "tasks/shared.md", "---\nid: TASK-s\ntype: Spike\ntitle: S\nstatus: Pending\n---\n# S\n")
	swtGit(t, repo, "add", "-A")
	swtGit(t, repo, "commit", "-q", "-m", "retype on the run branch")
	swtGit(t, repo, "checkout", "-q", "main")
	mainOID := swtGit(t, repo, "rev-parse", "refs/heads/main")

	shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	resolver := &swtTypeResolver{seen: make(chan struct{})}
	starter := &Orchestrator{artifacts: swtGitArtifacts{shared}, workflows: resolver, store: &mockRunStore{},
		events: &mockEventEmitter{}, git: shared, wfLoader: &stubWorkflowLoader{}, policy: branchprotect.NewPermissive()}
	var seen string
	var head string
	var headErr, readErr error
	startErr := make(chan error, 1)
	var once sync.Once
	hookA := &swtHookGit{CLIClient: shared, afterCheckout: func(_ context.Context, b string) {
		if b != "spine/run/run-a" {
			return
		}
		once.Do(func() {
			if cur, _ := swtTry(repo, "symbolic-ref", "-q", "HEAD"); cur != "refs/heads/spine/run/run-a" {
				t.Errorf("the hook expected the run branch checked out, HEAD=%s", cur)
			}
			// Other requests, with no ticket.
			var out []byte
			out, readErr = shared.ReadFile(context.Background(), "HEAD", "tasks/shared.md")
			seen = string(out)
			head, headErr = shared.Head(context.Background())
			go func() {
				_, err := starter.StartRun(context.Background(), "tasks/shared.md")
				startErr <- err
			}()
			select {
			case <-resolver.seen:
			case <-time.After(5 * time.Second):
				t.Error("StartRun did not bind a workflow while the sequence held the tree")
			}
		})
	}}
	oa, _ := swtOrch(hookA, "a")
	if err := oa.MergeRunBranch(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	if readErr != nil || !strings.Contains(seen, "type: Task") {
		t.Fatalf("ReadFile(HEAD) during the sequence = %q, %v; want main's type: Task", seen, readErr)
	}
	if headErr != nil || head != mainOID {
		t.Fatalf("Head() during the sequence = %s, %v; want refs/heads/main %s", head, headErr, mainOID)
	}
	if err := <-startErr; !errors.Is(err, errSwtStop) {
		t.Fatalf("StartRun = %v", err)
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.types) != 1 || resolver.types[0] != "Task" {
		t.Fatalf("StartRun bound the workflow for %v, want [Task]", resolver.types)
	}
}

// P3-27 (design-check note on what the bound counts): TASK-005's
// transient VerifyCommit exit records a failed, transient primary outcome
// before retrying, so the retry budget applies to it.
func TestMergeRetry_TransientVerifyCommitIsACountedAttempt(t *testing.T) {
	f := committingFix(t, false, true)
	valid := sha40('b')
	f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: valid, CommitMetaBoundBy: boundByStep("review", bindRunID+"-review-1")}
	f.git.verifyErr = &git.GitError{Kind: git.ErrKindTransient, Op: "verify_commit", Message: "repository locked"}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := f.merge(); err != nil {
			t.Fatalf("MergeRunBranch: %v", err)
		}
		o, err := f.store.GetRepositoryMergeOutcome(context.Background(), f.run.RunID, domain.PrimaryRepositoryID)
		if err != nil {
			t.Fatalf("attempt %d: no primary outcome: %v", attempt, err)
		}
		if o.Attempts != attempt || o.Status != domain.RepositoryMergeStatusFailed || !o.FailureClass.IsTransient() {
			t.Fatalf("attempt %d: outcome %d/%s/%s, want %d/failed/transient", attempt, o.Attempts, o.Status, o.FailureClass, attempt)
		}
		if f.run.Status != domain.RunStatusCommitting {
			t.Fatalf("run %s, want committing", f.run.Status)
		}
	}
}
