package engine

// INIT-023 TASK-004 AC8 (security audit 2026-10-06, finding F5, cascade
// sink). applyCommitStatus reads each cascade blob with `git show` and only
// then checks the run branch out and writes the rewritten status through
// GitOperator.WriteAndStageFile. A push to the run branch between those
// two steps can replace the task file with a symlink to any file the
// process can write. On Spine 61994fe CLIClient.WriteAndStageFile followed
// the link and overwrote the target with the status-rewritten content.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/worktreefs"
	"github.com/bszymi/spine/testutil"
)

// swapOnCheckout is a real CLIClient that, the first time the run branch is
// checked out, first commits a symlink at taskPath onto that branch's tip —
// the attacker's push landing between the cascade's read and its checkout.
type swapOnCheckout struct {
	*git.CLIClient
	t        *testing.T
	repo     string
	branch   string
	taskPath string
	target   string
	swapped  bool
}

func (s *swapOnCheckout) Checkout(ctx context.Context, branch string) error {
	if branch == s.branch && !s.swapped {
		s.swapped = true
		commitSymlinkOnBranch(s.t, s.repo, s.branch, s.taskPath, s.target)
	}
	return s.CLIClient.Checkout(ctx, branch)
}

// commitSymlinkOnBranch commits path -> target onto branch with plumbing
// only (a private index), so neither the working tree nor the real index
// is touched: exactly what a push does.
func commitSymlinkOnBranch(t *testing.T, repo, branch, path, target string) {
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
	run("", "update-index", "--add", "--cacheinfo", "120000,"+blob+","+path)
	tree := run("", "write-tree")
	commit := run("", "commit-tree", tree, "-p", branch, "-m", "swap task file for a symlink")
	run("", "update-ref", "refs/heads/"+branch, commit)
}

type swapScenario struct {
	repo   string
	victim string
	past   time.Time
	gitOp  *swapOnCheckout
	run    *domain.Run
}

// newSwapScenario builds a repo whose run branch adds one task artifact,
// an outside victim file, and a GitOperator that swaps the task file for a
// link to the victim on the branch tip when the cascade checks it out.
func newSwapScenario(t *testing.T) *swapScenario {
	t.Helper()
	const branch = "spine/run/run-f5"
	const taskPath = "initiatives/init-001/tasks/task-001.md"
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, "README.md", "# repo\n")
	testutil.GitAdd(t, repo, ".", "base")
	testutil.GitCheckout(t, repo, "-b", branch)
	testutil.WriteFile(t, repo, taskPath, "---\nid: TASK-001\ntype: Task\ntitle: T\nstatus: Draft\n---\n# TASK-001\n")
	testutil.GitAdd(t, repo, ".", "add task on run branch")
	testutil.GitCheckout(t, repo, "main")

	victim := filepath.Join(t.TempDir(), "victim.md")
	if err := os.WriteFile(victim, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(victim, past, past); err != nil {
		t.Fatal(err)
	}
	return &swapScenario{
		repo: repo, victim: victim, past: past,
		gitOp: &swapOnCheckout{
			CLIClient: git.NewCLIClient(repo),
			t:         t, repo: repo, branch: branch, taskPath: taskPath, target: victim,
		},
		run: &domain.Run{
			RunID:      "run-f5",
			Status:     domain.RunStatusCommitting,
			BranchName: branch,
			TaskPath:   taskPath,
			TraceID:    "trace-f5-0000000",
			CommitMeta: map[string]string{"status": "Completed"},
		},
	}
}

// assertContained checks the outcome every refused single-path cascade must
// have: the victim untouched (content and mtime), the shared tree clean,
// HEAD back on main.
func (sc *swapScenario) assertContained(t *testing.T) {
	t.Helper()
	if !sc.gitOp.swapped {
		t.Fatal("the swap never happened: the cascade did not check the run branch out")
	}
	b, _ := os.ReadFile(sc.victim)
	fi, _ := os.Stat(sc.victim)
	if string(b) != "orig" || !fi.ModTime().Equal(sc.past) {
		t.Fatalf("cascade wrote through the symlink: victim=%q mtime=%v", b, fi.ModTime())
	}
	gitOut := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", sc.repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// Single-path cascade: the refusal happens before any filesystem
	// change, so the shared tree ends clean with HEAD on main. (A
	// multi-path cascade refused part-way is TASK-012's.)
	if st := gitOut("status", "--porcelain"); st != "" {
		t.Fatalf("working tree not clean after the refused cascade:\n%s", st)
	}
	if head := gitOut("symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("HEAD = %s, want refs/heads/main", head)
	}
}

func TestApplyCommitStatus_SymlinkSwappedAfterRead(t *testing.T) {
	sc := newSwapScenario(t)
	err := (&Orchestrator{git: sc.gitOp}).applyCommitStatus(context.Background(), sc.run, "Completed")
	sc.assertContained(t)
	if !errors.Is(err, worktreefs.ErrUnsafePath) {
		t.Fatalf("want an error wrapping worktreefs.ErrUnsafePath, got %v", err)
	}
}

// Owner question 3 default: the status rewrite stays best-effort — the
// refusal is logged and preflightCheck still hands the run to the merge
// loop (merge.go contract: a failed status rewrite must not block the
// merge). TASK-005 / TASK-017 own any change to that.
func TestPreflightCheck_SymlinkSwappedCascade_ProceedsToMerge(t *testing.T) {
	sc := newSwapScenario(t)
	store := &mockRunStore{runs: map[string]*domain.Run{sc.run.RunID: sc.run}}
	orch := &Orchestrator{store: store, git: sc.gitOp, events: &mockEventEmitter{}, policy: branchprotect.NewPermissive()}
	got, done, err := orch.preflightCheck(context.Background(), sc.run.RunID)
	sc.assertContained(t)
	if err != nil || done || got == nil || got.RunID != sc.run.RunID {
		t.Fatalf("preflightCheck = (%v, %v, %v), want the run handed on to the merge", got, done, err)
	}
}
