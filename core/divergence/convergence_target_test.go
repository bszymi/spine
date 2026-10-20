package divergence_test

// INIT-023 TASK-012, audit F8: convergence merges the selected divergence
// branches into the RUN branch, never into whatever the shared working
// tree has checked out (main), and divergence branches fork from the run
// branch. Inverted from the audit PoC poc/governance/convergence_main_test.go
// (there PASS = main advanced by EvaluateAndCommit) and from the design
// check's probes 5 and 6 (_work/task012-check). Tests use the real
// git.CLIClient with WithHomeBranch("main"), as `spine serve` builds it.

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
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/divergence"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/testutil"
)

// cvStore is a BranchStore that counts divergence-context writes.
type cvStore struct {
	mu       sync.Mutex
	divs     map[string]*domain.DivergenceContext
	branches map[string]*domain.Branch
	updates  int
}

func newCVStore() *cvStore {
	return &cvStore{divs: map[string]*domain.DivergenceContext{}, branches: map[string]*domain.Branch{}}
}

func (s *cvStore) CreateDivergenceContext(_ context.Context, d *domain.DivergenceContext) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *d
	s.divs[d.DivergenceID] = &cp
	return nil
}

func (s *cvStore) UpdateDivergenceContext(_ context.Context, d *domain.DivergenceContext) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates++
	cp := *d
	s.divs[d.DivergenceID] = &cp
	return nil
}

func (s *cvStore) GetDivergenceContext(_ context.Context, id string) (*domain.DivergenceContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.divs[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "no divergence")
	}
	cp := *d
	return &cp, nil
}

func (s *cvStore) CreateBranch(_ context.Context, b *domain.Branch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.branches[b.BranchID] = b
	return nil
}

func (s *cvStore) UpdateBranch(_ context.Context, b *domain.Branch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.branches[b.BranchID] = b
	return nil
}

func (s *cvStore) GetBranch(_ context.Context, id string) (*domain.Branch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.branches[id], nil
}

func (s *cvStore) ListBranchesByDivergence(_ context.Context, id string) ([]domain.Branch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Branch
	for _, b := range s.branches {
		if b.DivergenceID == id {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (s *cvStore) completeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.branches {
		b.Status = domain.BranchStatusCompleted
	}
}

func (s *cvStore) status(id string) domain.DivergenceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.divs[id].Status
}

type cvEvents struct{}

func (cvEvents) Emit(context.Context, domain.Event) error                              { return nil }
func (cvEvents) Subscribe(context.Context, domain.EventType, event.EventHandler) error { return nil }

func cvGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test",
		"-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func cvTry(dir string, args ...string) error {
	return exec.Command("git", append([]string{"-C", dir}, args...)...).Run()
}

func cvWrite(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cvCommitOn commits files on branch through the raw git CLI (a worker's
// push lands the same refs) and returns the tree to main.
func cvCommitOn(t *testing.T, dir, branch string, files map[string]string, msg string) {
	t.Helper()
	cvGit(t, dir, "checkout", "-q", branch)
	for p, body := range files {
		cvWrite(t, dir, p, body)
	}
	cvGit(t, dir, "add", "-A")
	cvGit(t, dir, "commit", "-q", "-m", msg)
	cvGit(t, dir, "checkout", "-q", "main")
}

type cvEnv struct {
	dir        string
	client     *git.CLIClient
	st         *cvStore
	svc        *divergence.Service
	run        *domain.Run
	mainBefore string
}

func newCVEnv(t *testing.T) *cvEnv {
	t.Helper()
	// The CLIClient's own convergence merges need a committer identity
	// (review round 1: a CI runner has none to guess).
	testutil.HermeticGit(t)
	dir := t.TempDir()
	cvGit(t, dir, "init", "-q", "-b", "main")
	cvWrite(t, dir, "README.md", "seed\n")
	cvGit(t, dir, "add", "-A")
	cvGit(t, dir, "commit", "-q", "-m", "seed")
	client := git.NewCLIClient(dir, git.WithHomeBranch("main"))
	run := &domain.Run{RunID: "run-1", BranchName: "spine/run/task-1-run-1"}
	if err := client.CreateBranch(context.Background(), run.BranchName, "HEAD"); err != nil {
		t.Fatal(err)
	}
	st := newCVStore()
	svc := divergence.NewService(st, client, cvEvents{})
	svc.WithBranchProtectPolicy(branchprotect.New(branchprotect.StaticRules(branchprotect.BootstrapDefaults())))
	return &cvEnv{dir: dir, client: client, st: st, svc: svc, run: run, mainBefore: cvGit(t, dir, "rev-parse", "main")}
}

var cvConv = domain.ConvergenceDefinition{ID: "c1", Strategy: domain.ConvergenceSelectOne, EntryPolicy: domain.EntryPolicyAllTerminal}

// diverge starts a one-branch structured divergence and commits files on
// its branch; the branch is marked completed.
func (e *cvEnv) diverge(t *testing.T, files map[string]string) (*domain.DivergenceContext, string) {
	t.Helper()
	divCtx, err := e.svc.StartDivergence(context.Background(), e.run, domain.DivergenceDefinition{
		ID: "d1", Mode: domain.DivergenceModeStructured,
		Branches: []domain.BranchDefinition{{ID: "a", StartStep: "draft"}},
	}, "c1")
	if err != nil {
		t.Fatalf("StartDivergence: %v", err)
	}
	divBranch := "spine/run-1/" + divCtx.DivergenceID + "/a"
	cvCommitOn(t, e.dir, divBranch, files, "branch work")
	e.st.completeAll()
	return divCtx, divBranch
}

func (e *cvEnv) assertTreeHome(t *testing.T) {
	t.Helper()
	if head := cvGit(t, e.dir, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("shared tree left on %s", head)
	}
	if st := cvGit(t, e.dir, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Fatalf("shared tree left dirty: %q", st)
	}
}

// AC 2.
func TestF8_StandaloneConvergenceTargetsRunBranch(t *testing.T) {
	ctx := context.Background()
	e := newCVEnv(t)
	divCtx, _ := e.diverge(t, map[string]string{"branch-output.md": "divergence content, not yet reviewed\n"})
	if err := e.svc.EvaluateAndCommit(ctx, e.run, divCtx, cvConv); err != nil {
		t.Fatalf("EvaluateAndCommit: %v", err)
	}
	if got := cvGit(t, e.dir, "rev-parse", "main"); got != e.mainBefore {
		t.Fatalf("F8: convergence advanced main %s -> %s", e.mainBefore[:8], got[:8])
	}
	if cvTry(e.dir, "cat-file", "-e", "main:branch-output.md") == nil {
		t.Fatal("F8: divergence content reached main before review")
	}
	if cvTry(e.dir, "cat-file", "-e", e.run.BranchName+":branch-output.md") != nil {
		t.Fatal("the divergence content is not on the run branch")
	}
	if got := e.st.status(divCtx.DivergenceID); got != domain.DivergenceStatusResolved {
		t.Fatalf("divergence status %s, want resolved", got)
	}
	e.assertTreeHome(t)
}

// AC 3, ordered so a nil dereference cannot hide the other cases.
func TestF8_ConvergenceRefusesMissingOrForeignTarget(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		bad  *domain.Run
	}{
		{"other run's spine branch", &domain.Run{RunID: "run-2", BranchName: "spine/run/x-run-2"}},
		{"main", &domain.Run{RunID: "run-1", BranchName: "main"}},
		{"empty branch", &domain.Run{RunID: "run-1"}},
		{"nil run", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCVEnv(t)
			cvGit(t, e.dir, "branch", "spine/run/x-run-2", "main") // the foreign run's branch exists
			divCtx, _ := e.diverge(t, map[string]string{"branch-output.md": "x\n"})
			runTip := cvGit(t, e.dir, "rev-parse", e.run.BranchName)
			otherTip := cvGit(t, e.dir, "rev-parse", "spine/run/x-run-2")
			before := e.st.status(divCtx.DivergenceID)
			updates := e.st.updates
			err := e.svc.EvaluateAndCommit(ctx, tc.bad, divCtx, cvConv)
			var se *domain.SpineError
			if !errors.As(err, &se) || se.Code != domain.ErrPrecondition {
				t.Fatalf("EvaluateAndCommit(%s) = %v, want ErrPrecondition", tc.name, err)
			}
			if cvGit(t, e.dir, "rev-parse", "main") != e.mainBefore ||
				cvGit(t, e.dir, "rev-parse", e.run.BranchName) != runTip ||
				cvGit(t, e.dir, "rev-parse", "spine/run/x-run-2") != otherTip {
				t.Fatal("a ref moved on a refused convergence")
			}
			if got := e.st.status(divCtx.DivergenceID); got != before || got != domain.DivergenceStatusActive {
				t.Fatalf("divergence status %s after a refusal, want %s (active)", got, before)
			}
			if e.st.updates != updates {
				t.Fatalf("a refused convergence wrote the divergence context (%d writes)", e.st.updates-updates)
			}
			// CommitConvergence (exported) refuses the same runs.
			if err := e.svc.CommitConvergence(ctx, tc.bad, divCtx, &divergence.ConvergenceOutput{
				Result: domain.ConvergenceResult{SelectedBranch: divCtx.DivergenceID + "-a"},
			}); !errors.As(err, &se) || se.Code != domain.ErrPrecondition {
				t.Fatalf("CommitConvergence(%s) = %v, want ErrPrecondition", tc.name, err)
			}
			if e.st.updates != updates || cvGit(t, e.dir, "rev-parse", "main") != e.mainBefore {
				t.Fatal("a refused CommitConvergence changed state")
			}
		})
	}
}

// recordingGit is a non-guard client (not a git.WorkingTreeGuard), like
// SMP's adapter.
type recordingGit struct {
	git.GitClient
	merges  []git.MergeOpts
	failErr error
}

func (r *recordingGit) Merge(_ context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	r.merges = append(r.merges, opts)
	return git.MergeResult{}, r.failErr
}

// AC 5.
func TestF8_ConvergenceAbortScope(t *testing.T) {
	ctx := context.Background()
	t.Run("shared tree: a conflict is aborted before the tree is released", func(t *testing.T) {
		e := newCVEnv(t)
		divCtx, _ := e.diverge(t, map[string]string{"README.md": "branch edit\n"})
		cvCommitOn(t, e.dir, e.run.BranchName, map[string]string{"README.md": "run edit after the fork\n"}, "run work")
		// The shared client, still a git.WorkingTreeGuard, recording its
		// merges (review round 1: the exit restore aborts a left-over merge
		// too, so the end state alone cannot show the in-block abort).
		rec := &mergeRecorder{CLIClient: e.client, dir: e.dir}
		svc := divergence.NewService(e.st, rec, cvEvents{})
		svc.WithBranchProtectPolicy(branchprotect.New(branchprotect.StaticRules(branchprotect.BootstrapDefaults())))
		logs := cvCaptureLogs(t)
		err := svc.EvaluateAndCommit(ctx, e.run, divCtx, cvConv)
		if err == nil {
			t.Fatal("a conflicting convergence reported success")
		}
		if cvTry(e.dir, "rev-parse", "-q", "--verify", "MERGE_HEAD") == nil {
			t.Fatal("MERGE_HEAD survived the failed convergence")
		}
		e.assertTreeHome(t)
		if got := e.st.status(divCtx.DivergenceID); got == domain.DivergenceStatusResolved {
			t.Fatal("a failed convergence was resolved")
		}
		rec.mu.Lock()
		calls := append([]recordedMerge(nil), rec.calls...)
		rec.mu.Unlock()
		if len(calls) != 2 || calls[0].opts.Target != e.run.BranchName ||
			calls[1].opts.Source != "--abort" || calls[1].opts.Strategy != "abort" || !calls[1].held {
			t.Fatalf("merges = %+v, want the merge into %s, then its abort while the tree is still held", calls, e.run.BranchName)
		}
		// The exit restore only switches HEAD back home; it never finds a
		// merge left in progress.
		if strings.Contains(logs.String(), "aborted_merge=true") {
			t.Fatalf("the exit restore had to abort the failed convergence's merge:\n%s", logs.String())
		}
	})
	t.Run("non-guard client: one merge with the run branch as target, no abort", func(t *testing.T) {
		for _, failErr := range []error{nil, errors.New("conflict")} {
			rg := &recordingGit{failErr: failErr}
			svc := divergence.NewService(newCVStore(), rg, cvEvents{})
			run := &domain.Run{RunID: "run-1", BranchName: "spine/run/task-1-run-1"}
			divCtx := &domain.DivergenceContext{DivergenceID: "run-1-div-d1", RunID: "run-1", Status: domain.DivergenceStatusConverging}
			_ = svc.CommitConvergence(ctx, run, divCtx, &divergence.ConvergenceOutput{
				Result: domain.ConvergenceResult{SelectedBranch: "run-1-div-d1-a"},
			})
			if len(rg.merges) != 1 || rg.merges[0].Target != run.BranchName ||
				rg.merges[0].Source != "spine/run-1/run-1-div-d1/a" || rg.merges[0].Strategy == "abort" {
				t.Fatalf("failErr=%v: merges = %+v, want exactly one into %s and no abort", failErr, rg.merges, run.BranchName)
			}
		}
	})
}

// AC 27.
func TestF8_DivergenceForksFromRunBranch(t *testing.T) {
	ctx := context.Background()
	e := newCVEnv(t)
	// A run step edits README.md on the run branch before the divergence.
	cvCommitOn(t, e.dir, e.run.BranchName, map[string]string{"README.md": "seed\nrun-step edit\n"}, "run step")
	runTip := cvGit(t, e.dir, "rev-parse", e.run.BranchName)
	divCtx, divBranch := e.diverge(t, map[string]string{"README.md": "seed\nrun-step edit\nbranch edit\n"})
	if base := cvGit(t, e.dir, "merge-base", divBranch, e.run.BranchName); base != runTip {
		t.Fatalf("divergence branch forked from %s, want the run branch tip %s", base[:8], runTip[:8])
	}
	if err := e.svc.EvaluateAndCommit(ctx, e.run, divCtx, cvConv); err != nil {
		t.Fatalf("EvaluateAndCommit: %v", err)
	}
	if got := e.st.status(divCtx.DivergenceID); got != domain.DivergenceStatusResolved {
		t.Fatalf("divergence status %s, want resolved", got)
	}
	if got := cvGit(t, e.dir, "rev-parse", "main"); got != e.mainBefore {
		t.Fatal("main moved")
	}
	e.assertTreeHome(t)

	// The same for an exploratory branch.
	exp, err := e.svc.StartDivergence(ctx, e.run, domain.DivergenceDefinition{ID: "d2", Mode: domain.DivergenceModeExploratory}, "")
	if err != nil {
		t.Fatal(err)
	}
	tip := cvGit(t, e.dir, "rev-parse", e.run.BranchName)
	if _, err := e.svc.CreateExploratoryBranch(ctx, e.run, exp, "x", "draft"); err != nil {
		t.Fatalf("CreateExploratoryBranch: %v", err)
	}
	if got := cvGit(t, e.dir, "rev-parse", "spine/run-1/"+exp.DivergenceID+"/x"); got != tip {
		t.Fatalf("exploratory branch at %s, want the run branch tip %s", got[:8], tip[:8])
	}
	var se *domain.SpineError
	if _, err := e.svc.CreateExploratoryBranch(ctx, &domain.Run{RunID: "run-2", BranchName: "spine/run/x-run-2"}, exp, "y", "draft"); !errors.As(err, &se) || se.Code != domain.ErrPrecondition {
		t.Fatalf("CreateExploratoryBranch for another run = %v, want ErrPrecondition", err)
	}
}

type recordedMerge struct {
	opts git.MergeOpts
	held bool // a second client on the tree could not take it at the time
}

// mergeRecorder is the shared CLIClient recording Merge calls and whether
// the tree was held when each was made.
type mergeRecorder struct {
	*git.CLIClient
	dir   string
	mu    sync.Mutex
	calls []recordedMerge
}

func (r *mergeRecorder) Merge(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	probe, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err := git.NewCLIClient(r.dir).WithWorkingTree(probe, func(context.Context) error { return nil })
	cancel()
	var we *git.TreeWaitError
	r.mu.Lock()
	r.calls = append(r.calls, recordedMerge{opts: opts, held: errors.As(err, &we)})
	r.mu.Unlock()
	return r.CLIClient.Merge(ctx, opts)
}

func cvCaptureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}
