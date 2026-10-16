package artifact

// INIT-023 TASK-012 review round 1: the acceptance and follow-up-link
// read-modify-write never reads a run branch a governed sequence has
// checked out in standalone's shared working tree, and never overwrites a
// change that landed between its read and its write.

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/testutil"
)

const sharedTreeTask = "---\nid: TASK-900\ntype: Task\ntitle: Victim\nstatus: Pending\n" +
	"epic: /initiatives/init-001/epics/epic-001/epic.md\ninitiative: /initiatives/init-001/initiative.md\n---\n# Victim\n"

func stGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// stRepo: main holds tasks/victim.md awaiting acceptance; the run branch
// spine/run/r1 rewrites it with an unreviewed payload.
func stRepo(t *testing.T) string {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, "tasks/victim.md", sharedTreeTask)
	stGit(t, repo, "add", "-A")
	stGit(t, repo, "commit", "-q", "-m", "task")
	stGit(t, repo, "checkout", "-q", "-b", "spine/run/r1")
	testutil.WriteFile(t, repo, "tasks/victim.md", strings.Replace(sharedTreeTask, "# Victim\n", "# Victim\nUNREVIEWED-PAYLOAD\n", 1))
	stGit(t, repo, "commit", "-q", "-am", "unreviewed rewrite")
	stGit(t, repo, "checkout", "-q", "main")
	return repo
}

// The review's scenario: while a sequence holds the shared tree on a run
// branch (a convergence merging a divergence branch, the binding-off
// cascade), an operator accepts a task. Before the fix the accept read
// the run branch's file from disk and committed it — payload included —
// onto main.
func TestAcceptTask_DuringAHeldSequenceRecordsMainsOwnTask(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	repo := stRepo(t)
	shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	svc := NewService(shared, nil, repo)
	svc.WithPolicy(branchprotect.NewPermissive())
	runTip := stGit(t, repo, "rev-parse", "spine/run/r1")

	held, release, seqDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		seqDone <- shared.WithWorkingTree(ctx, func(ctx context.Context) error {
			if err := shared.Checkout(ctx, "spine/run/r1"); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	type result struct {
		res *WriteResult
		err error
	}
	accepted := make(chan result, 1)
	go func() {
		r, err := svc.AcceptTask(ctx, "tasks/victim.md", "looks good")
		accepted <- result{r, err}
	}()
	time.Sleep(300 * time.Millisecond) // the accept reaches the tree while the sequence holds it
	close(release)
	if err := <-seqDone; err != nil {
		t.Fatalf("the holding sequence: %v", err)
	}
	r := <-accepted
	if r.err != nil {
		t.Fatalf("AcceptTask: %v", r.err)
	}
	onMain := stGit(t, repo, "show", "main:tasks/victim.md")
	if strings.Contains(onMain, "UNREVIEWED-PAYLOAD") {
		t.Fatalf("the accept committed the run branch's unreviewed file to main:\n%s", onMain)
	}
	if !strings.Contains(onMain, "acceptance: Approved") || !strings.Contains(onMain, "status: Completed") {
		t.Fatalf("main's task is not accepted:\n%s", onMain)
	}
	if got := stGit(t, repo, "rev-parse", "spine/run/r1"); got != runTip {
		t.Fatal("the run branch moved")
	}
	if head := stGit(t, repo, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("tree left on %s", head)
	}
}

// updateFrom commits only over the bytes the new content was derived
// from: a file that changed in between is a conflict, and nothing moves.
func TestUpdateFrom_ConflictWhenTheFileChangedSinceTheRead(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	repo := stRepo(t)
	svc := NewService(git.NewCLIClient(repo, git.WithHomeBranch("main")), nil, repo)
	svc.WithPolicy(branchprotect.NewPermissive())
	before := stGit(t, repo, "rev-parse", "main")
	updated := strings.Replace(sharedTreeTask, "status: Pending", "status: Completed", 1)

	_, err := svc.updateFrom(ctx, "tasks/victim.md", []byte("a stale read\n"), updated)
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrConflict {
		t.Fatalf("updateFrom over a stale read = %v, want ErrConflict", err)
	}
	if got := stGit(t, repo, "rev-parse", "main"); got != before {
		t.Fatal("a conflicting update committed")
	}
	if st := stGit(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("a conflicting update left the tree dirty: %q", st)
	}

	raw, err := svc.readRepoFile(ctx, "tasks/victim.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.updateFrom(ctx, "tasks/victim.md", raw, updated); err != nil {
		t.Fatalf("updateFrom over the current bytes: %v", err)
	}
}
